// Package migrate is the migrate verb's core: the one-shot import of a
// buf configuration into pb's files (migrate.md). Each step takes the
// configuration as bufconfig read it and yields pb's files and the
// facts of the migration report — what mapped, what did not and why —
// as values; writing, the tidy and the report's printing are the
// verb's, so every step is a pure function a test holds to the spec.
package migrate

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/greatliontech/pb/internal/check/lintfile"
	"github.com/greatliontech/pb/internal/gitdir"
	"github.com/greatliontech/pb/internal/migrate/bufconfig"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/modfile"
	"github.com/greatliontech/pb/internal/module/workspace"
	"github.com/greatliontech/pb/internal/rootpath"
)

// ErrNoModulePath is wrapped where no module path is given and none
// derives from the repository: the flag is the remedy.
var ErrNoModulePath = errors.New("no module path: pass --module")

// Fact is one line of the migration report (migrate.md, the migration
// report term): the buf fact as its key and value, whether it took a
// pb form, and the text — the form it took, or the reason it took
// none.
type Fact struct {
	Source string
	Mapped bool
	Text   string
}

// The constructors are the one way a fact is made, and each folds its
// texts onto one line (REQ-migrate-report): foreign text — a remote's
// banner in a discovery's error, a buf file's value — never breaks
// the report's line or moves a terminal's cursor.
func mapped(source, form string) Fact {
	return Fact{Source: oneLine(source), Mapped: true, Text: oneLine(form)}
}
func unmapped(source, why string) Fact { return Fact{Source: oneLine(source), Text: oneLine(why)} }

// oneLine folds text onto one printable line. An escape sequence is
// dropped whole: ESC, then a CSI's parameters to a final byte in @
// through ~, or intermediates to a final byte in 0 through ~, or a
// string sequence (OSC, DCS, SOS, APC, PM) to its terminator, ESC \ or
// BEL. A byte outside the sequence's grammar, or the text's end, ends
// the sequence with the byte unconsumed: it is scanned again in its
// own right, so it may open a sequence of its own or reach the line,
// and a truncated sequence swallows nothing after it. A string
// sequence's body admits every byte but a line break or a carriage
// return, which end an unterminated one the same way, so it swallows
// no later line and no later fragment. A letter after ESC [ is a final
// byte, so a stray CSI before ordinary text costs that text's first
// character, the one loss the grammar cannot tell from a real
// sequence. A bare control byte, a carriage return included, becomes a
// space, so a progress fragment never hides the cause after it; and of
// the lines left the first that holds anything is the line, its runs
// of whitespace one space. Multi-byte runes pass as they are: no byte
// of one is below 0x80, and an escape consumes only ASCII. The fold is
// idempotent: a text folded twice is the text folded once, so a fact's
// text may be folded at its site and again by the constructor.
func oneLine(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == 0x1b:
			i++
			switch {
			case i < len(text) && text[i] == '[':
				// The parameter loop runs to 0x3f, so the final byte
				// test below, from 0x30, is CSI's own from @.
				for i++; i < len(text) && text[i] >= 0x20 && text[i] < 0x40; i++ {
				}
			case i < len(text) && strings.IndexByte("]PX_^", text[i]) >= 0:
				// A string sequence: its body runs to ESC \ or BEL,
				// the terminator consumed with it; a line break or a
				// carriage return ends an unterminated one, kept, as
				// does the text's end.
				for i++; i < len(text); i++ {
					if text[i] == 0x07 {
						break
					}
					if text[i] == 0x1b && i+1 < len(text) && text[i+1] == '\\' {
						i++
						break
					}
					if text[i] == '\n' || text[i] == '\r' {
						i--
						break
					}
				}
				continue
			default:
				for ; i < len(text) && text[i] >= 0x20 && text[i] <= 0x2f; i++ {
				}
			}
			if i >= len(text) || text[i] < 0x30 || text[i] > 0x7e {
				i-- // no final byte: the escape ends here, the byte kept
			}
		case c == '\n':
			b.WriteByte('\n')
		case c < 0x20 || c == 0x7f:
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
		}
	}
	for _, line := range strings.Split(b.String(), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			return strings.Join(fields, " ")
		}
	}
	return ""
}

// Source is a buf configuration as read: the buf.yaml at the
// directory, the buf.work.yaml beside it where one lies, and, for a
// v1 workspace, each directory's own buf.yaml keyed by the directory
// cleaned (rootpath.Clean); a directory with none is a module under
// buf's default v1 configuration, as buf reads it.
type Source struct {
	File    *bufconfig.File
	Work    *bufconfig.Work
	Members map[string]*bufconfig.File
}

// Layout is the modules step's result (REQ-migrate-modules): the module
// file for each module, keyed by the module's directory relative to
// the configuration's, cleaned, "." for a module at the directory
// itself; the workspace file where the configuration declares several
// modules, nil for one; and the facts.
type Layout struct {
	Modules   map[string]*modfile.File
	Workspace *workspace.File
	Lint      *lintfile.File // set by Rules
	// CommentIgnores, set by Rules, says per module directory whether
	// buf honored its suppression comments, which the verb rewrites
	// where it did (REQ-migrate-comments).
	CommentIgnores map[string]bool
	Facts          []Fact
}

// declared is one module a buf configuration declares, as the steps
// read it: its directory cleaned and as buf's file spells it, buf's
// module, the file's version, the key that declared it and the file
// whose keys the module's facts cite, the directory's own buf.yaml
// for a v1 workspace (nil where it has none).
type declared struct {
	dir     string
	spelled string
	mod     *bufconfig.Module
	version string
	from    string
	in      string
	file    *bufconfig.File
}

// declarations reads the modules a configuration declares, in the
// order declared: a buf.work.yaml's directories, each with its own
// buf.yaml or under buf's default v1 configuration; a v2 buf.yaml's
// modules; a v1 buf.yaml's one module at ".". A v2 file beside a
// workspace file is two workspaces at once, which buf refuses; a
// workspace directory whose buf.yaml is not v1 does not parse, as
// buf refuses it. A directory declared twice, or one containing
// another's, fails naming both.
func declarations(src *Source) ([]declared, error) {
	if src == nil || (src.File == nil && src.Work == nil) {
		return nil, fmt.Errorf("no buf configuration read")
	}
	if src.Work != nil && src.File != nil && src.File.Version == "v2" {
		return nil, fmt.Errorf("a v2 %s beside a %s: two workspaces at once, which buf refuses", bufconfig.FileName, bufconfig.WorkFileName)
	}
	var decls []declared
	clean := func(spelled, from string) (string, error) {
		dir, err := rootpath.Clean(spelled, "the configuration's directory")
		if err != nil {
			return "", fmt.Errorf("%s: %w", from, err)
		}
		return dir, nil
	}
	if src.Work != nil {
		for i, d := range src.Work.Directories {
			from := fmt.Sprintf("%s directories[%d]", bufconfig.WorkFileName, i)
			dir, err := clean(d, from)
			if err != nil {
				return nil, err
			}
			mod := &bufconfig.Module{Path: "."} // buf's default v1 configuration
			member := src.Members[dir]
			if member != nil {
				if member.Version != "v1" || len(member.Modules) != 1 {
					return nil, fmt.Errorf("%s: %s is not a v1 %s, as a workspace directory's must be", from, path.Join(dir, bufconfig.FileName), bufconfig.FileName)
				}
				mod = &member.Modules[0]
			}
			decls = append(decls, declared{dir: dir, spelled: d, mod: mod, version: "v1", from: from, in: path.Join(dir, bufconfig.FileName), file: member})
		}
	} else {
		for i := range src.File.Modules {
			m := &src.File.Modules[i]
			from := bufconfig.FileName
			if src.File.Version == "v2" {
				from = fmt.Sprintf("%s modules[%d]", bufconfig.FileName, i)
			}
			dir, err := clean(m.Path, from)
			if err != nil {
				return nil, err
			}
			decls = append(decls, declared{dir: dir, spelled: m.Path, mod: m, version: src.File.Version, from: from, in: from, file: src.File})
		}
	}
	// A File is a value any caller may build, and a map keyed by
	// directory would merge two declarations of one directory in
	// silence; a module within another the archive refuses.
	spelled := map[string]string{} // cleaned dir -> the key that declared it
	for _, d := range decls {
		if prior, dup := spelled[d.dir]; dup {
			return nil, fmt.Errorf("%s: a module at %q is declared twice, first by %s", d.from, d.dir, prior)
		}
		for other, prior := range spelled {
			if rootpath.Contains(other, d.dir) || rootpath.Contains(d.dir, other) {
				return nil, fmt.Errorf("%s: module %q lies within module %q (%s): the migration writes no module file the archive would refuse, the outer module never publishable with one beneath it", d.from, d.dir, other, prior)
			}
		}
		spelled[d.dir] = d.from
	}
	return decls, nil
}

// Modules lays out the modules a buf configuration declares
// (REQ-migrate-modules): a v2 buf.yaml's modules, a v1 buf.yaml's one
// module at ".", or a buf.work.yaml's directories, each with its own
// buf.yaml. The path given — the flag's, or the origin's (OriginPath)
// — is the configuration directory's; each module's path is that
// joined with its directory, the directory itself joining nothing.
// buf's module name is reported, never used; excludes and includes
// are unmapped facts. A directory declared twice, one escaping the
// configuration's, one containing another's, a workspace member
// whose buf.yaml is not v1, or a path no module may bear fails.
func Modules(src *Source, modulePath string) (*Layout, error) {
	decls, err := declarations(src)
	if err != nil {
		return nil, err
	}
	if modulePath == "" {
		return nil, ErrNoModulePath
	}
	if err := module.ValidatePath(modulePath); err != nil {
		return nil, err
	}
	l := &Layout{Modules: map[string]*modfile.File{}}
	several := len(decls) > 1
	if several {
		l.Workspace = &workspace.File{}
	}
	for _, d := range decls {
		dir := d.dir
		mp := modulePath
		if dir != "." {
			mp = modulePath + "/" + dir
			if err := module.ValidatePath(mp); err != nil {
				return nil, fmt.Errorf("%s: %w", d.from, err)
			}
		}
		l.Modules[dir] = &modfile.File{Module: mp}
		if several {
			l.Workspace.Use = append(l.Workspace.Use, dir)
		}
		l.Facts = append(l.Facts, mapped(d.from+" "+d.spelled, path.Join(dir, module.ModuleFileName)+" module: "+mp))
		if d.mod.Name != "" {
			l.Facts = append(l.Facts, mapped(d.in+".name "+d.mod.Name, "module: "+mp+" (a BSR name is no place pb fetches from)"))
		}
		excludes := "excludes"
		if d.version == "v1" {
			excludes = "build.excludes"
		}
		for _, key := range []struct {
			name string
			list []string
		}{{excludes, d.mod.Excludes}, {"includes", d.mod.Includes}} {
			for _, e := range key.list {
				l.Facts = append(l.Facts, unmapped(d.in+"."+key.name+" "+e, "a pb module's file set is every regular file under its root"))
			}
		}
	}
	return l, nil
}

// OriginPath derives the module path of a directory from the git
// repository it lies in (REQ-migrate-modules): the `origin` remote's
// URL spelled as a module path, joined with the directory's path within
// the repository. A directory in no repository, a repository with no
// origin, or a URL spelling no module path fails naming the flag.
func OriginPath(dir string) (string, error) {
	repo, rel, err := gitdir.RepoOf(dir)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoModulePath, err)
	}
	remote, err := repo.Remote("origin")
	if err != nil {
		return "", fmt.Errorf("%w: the repository has no origin remote", ErrNoModulePath)
	}
	urls := remote.Config().URLs
	if len(urls) == 0 {
		return "", fmt.Errorf("%w: the origin remote has no URL", ErrNoModulePath)
	}
	p, err := SpellOrigin(urls[0])
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoModulePath, err)
	}
	if rel != "" {
		p = p + "/" + rel
	}
	if err := module.ValidatePath(p); err != nil {
		return "", fmt.Errorf("%w: origin %s: %v", ErrNoModulePath, urls[0], err)
	}
	return p, nil
}

// SpellOrigin spells a remote's URL as a module path
// (REQ-migrate-modules): the host in lower case and the path, the
// scheme, the user, a leading or trailing slash and a trailing `.git`
// dropped — `https://github.com/o/r.git`, `ssh://git@github.com/o/r`
// and git's `git@github.com:o/r.git` all spell `github.com/o/r`. A
// URL with a port, a query or a fragment, or one spelling no module
// path, is an error; the path is read as spelled, never decoded.
func SpellOrigin(raw string) (string, error) {
	var host, p string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("origin %s: %v", raw, err)
		}
		if u.Port() != "" {
			return "", fmt.Errorf("origin %s: a module path has no port", raw)
		}
		if u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
			return "", fmt.Errorf("origin %s: a module path has no query or fragment", raw)
		}
		host, p = u.Hostname(), u.EscapedPath()
	default:
		// git's scp-like form: [user@]host:path, no scheme.
		h, rest, ok := strings.Cut(raw, ":")
		if !ok || strings.Contains(h, "/") {
			return "", fmt.Errorf("origin %s: neither a URL nor git's user@host:path", raw)
		}
		if _, after, ok := strings.Cut(h, "@"); ok {
			h = after
		}
		host, p = h, rest
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	mp := strings.ToLower(host) + "/" + p
	if err := module.ValidatePath(mp); err != nil {
		return "", fmt.Errorf("origin %s: %v", raw, err)
	}
	return mp, nil
}
