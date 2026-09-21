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

func mapped(source, form string) Fact  { return Fact{Source: source, Mapped: true, Text: form} }
func unmapped(source, why string) Fact { return Fact{Source: source, Text: why} }

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
	Facts     []Fact
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
	if src == nil || (src.File == nil && src.Work == nil) {
		return nil, fmt.Errorf("no buf configuration read")
	}
	if src.Work != nil && src.File != nil && src.File.Version == "v2" {
		return nil, fmt.Errorf("a v2 %s beside a %s: two workspaces at once, which buf refuses", bufconfig.FileName, bufconfig.WorkFileName)
	}
	if modulePath == "" {
		return nil, ErrNoModulePath
	}
	if err := module.ValidatePath(modulePath); err != nil {
		return nil, err
	}
	type declared struct {
		dir     string // cleaned
		spelled string // as buf's file spells it, for the report
		mod     *bufconfig.Module
		version string
		from    string // the key that declared it, for the report
		in      string // the file whose keys the module's facts cite
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
			if member := src.Members[dir]; member != nil {
				if member.Version != "v1" || len(member.Modules) != 1 {
					return nil, fmt.Errorf("%s: %s is not a v1 %s, as a workspace directory's must be", from, path.Join(dir, bufconfig.FileName), bufconfig.FileName)
				}
				mod = &member.Modules[0]
			}
			decls = append(decls, declared{dir: dir, spelled: d, mod: mod, version: "v1", from: from, in: path.Join(dir, bufconfig.FileName)})
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
			decls = append(decls, declared{dir: dir, spelled: m.Path, mod: m, version: src.File.Version, from: from, in: from})
		}
	}
	l := &Layout{Modules: map[string]*modfile.File{}}
	several := len(decls) > 1
	if several {
		l.Workspace = &workspace.File{}
	}
	spelled := map[string]string{} // cleaned dir -> the key that declared it
	for _, d := range decls {
		dir := d.dir
		// A File is a value any caller may build, and the map would
		// merge two declarations of one directory in silence.
		if prior, dup := spelled[dir]; dup {
			return nil, fmt.Errorf("%s: a module at %q is declared twice, first by %s", d.from, dir, prior)
		}
		for other, prior := range spelled {
			if rootpath.Contains(other, dir) || rootpath.Contains(dir, other) {
				return nil, fmt.Errorf("%s: module %q lies within module %q (%s): the migration writes no module file the archive would refuse, the outer module never publishable with one beneath it", d.from, dir, other, prior)
			}
		}
		spelled[dir] = d.from
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
