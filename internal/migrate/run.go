package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/util"
	"github.com/greatliontech/pb/internal/atomicfile"
	"github.com/greatliontech/pb/internal/check/lintfile"
	"github.com/greatliontech/pb/internal/migrate/bufconfig"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/modfile"
	"github.com/greatliontech/pb/internal/module/workspace"
	"github.com/greatliontech/pb/internal/plugin/genfile"
	"github.com/greatliontech/pb/internal/rootpath"
)

// ErrUnmapped is the verb's status where any fact went unmapped: the
// report printed and the files written, the exit status 1
// (REQ-migrate-report).
var ErrUnmapped = errors.New("some facts went unmapped: see the report")

// Invocation is what the verb draws on beside the configuration: the
// working tree and the configuration's directory within it, the
// directory's path on the host for the origin where no module path
// is given, the replacements, the discovery of versions, the tidy
// that ends the verb, and the report's writer.
type Invocation struct {
	WS           billy.Filesystem
	Dir          string
	HostDir      string
	ModulePath   string
	Replacements Replacements
	Discovery    Discovery
	// PluginTags lists a plugin repository's tags, for a versionless
	// plugin (REQ-migrate-gen); nil is no registry access.
	PluginTags TagLister
	Tidy       func(ctx context.Context) error
	Out        io.Writer
}

// ReadSource reads the buf configuration at a directory: the buf.yaml
// and the buf.work.yaml beside it, each where it lies, a v1
// workspace's directories' own buf.yaml files, the buf.gen.yaml and
// the buf.lock; a directory holding neither buf.yaml nor
// buf.work.yaml holds no configuration; a file that does not parse
// under its own version fails naming it (REQ-migrate-verb).
func ReadSource(ws billy.Filesystem, dir string) (*Source, *bufconfig.Gen, *bufconfig.Lock, error) {
	read := func(name string) ([]byte, bool, error) {
		b, err := util.ReadFile(ws, path.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("reading %s: %w", name, err)
		}
		return b, true, nil
	}
	src := &Source{}
	if b, ok, err := read(bufconfig.FileName); err != nil {
		return nil, nil, nil, err
	} else if ok {
		f, err := bufconfig.ParseFile(b)
		if err != nil {
			return nil, nil, nil, err
		}
		src.File = f
	}
	if b, ok, err := read(bufconfig.WorkFileName); err != nil {
		return nil, nil, nil, err
	} else if ok {
		w, err := bufconfig.ParseWork(b)
		if err != nil {
			return nil, nil, nil, err
		}
		src.Work = w
		src.Members = map[string]*bufconfig.File{}
		for _, d := range w.Directories {
			member, err := rootpath.Clean(d, "the configuration's directory")
			if err != nil {
				return nil, nil, nil, fmt.Errorf("%s: %w", bufconfig.WorkFileName, err)
			}
			b, ok, err := read(path.Join(member, bufconfig.FileName))
			if err != nil {
				return nil, nil, nil, err
			}
			if !ok {
				continue
			}
			f, err := bufconfig.ParseFile(b)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("%s: %w", path.Join(member, bufconfig.FileName), err)
			}
			src.Members[member] = f
			// A v1 workspace keeps its lock per module.
			if b, ok, err := read(path.Join(member, bufconfig.LockFileName)); err != nil {
				return nil, nil, nil, err
			} else if ok {
				l, err := bufconfig.ParseLock(b)
				if err != nil {
					return nil, nil, nil, fmt.Errorf("%s: %w", path.Join(member, bufconfig.LockFileName), err)
				}
				if src.MemberLocks == nil {
					src.MemberLocks = map[string]*bufconfig.Lock{}
				}
				src.MemberLocks[member] = l
			}
		}
	}
	if src.File == nil && src.Work == nil {
		return nil, nil, nil, fmt.Errorf("no buf configuration at %s: neither %s nor %s", path.Clean(dir), bufconfig.FileName, bufconfig.WorkFileName)
	}
	entries, err := ws.ReadDir(dir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reading %s: %w", path.Clean(dir), err)
	}
	for _, e := range entries {
		if !e.IsDir() && bufconfig.IsGenTemplate(e.Name()) {
			src.Templates = append(src.Templates, e.Name())
		}
	}
	sort.Strings(src.Templates)
	var gen *bufconfig.Gen
	if b, ok, err := read(bufconfig.GenFileName); err != nil {
		return nil, nil, nil, err
	} else if ok {
		if gen, err = bufconfig.ParseGen(b); err != nil {
			return nil, nil, nil, err
		}
	}
	var lock *bufconfig.Lock
	if b, ok, err := read(bufconfig.LockFileName); err != nil {
		return nil, nil, nil, err
	} else if ok {
		if lock, err = bufconfig.ParseLock(b); err != nil {
			return nil, nil, nil, err
		}
	}
	return src, gen, lock, nil
}

// Run is the verb (REQ-migrate-verb): the configuration read, the
// module path taken from the flag or the origin, the steps run —
// modules, dependencies, rules, generation — every key no step
// models an unmapped fact naming it, pb's files written beside the
// configuration where none exists already, the suppression comments
// rewritten over the proto files of each module buf honored them in,
// the tidy run over the result, the report printed — from the first
// write on, whatever fails, the files kept and the cause named. The
// status is nil where every fact mapped, ErrUnmapped where any did
// not (REQ-migrate-report).
func Run(ctx context.Context, d Invocation) error {
	src, gen, lock, err := ReadSource(d.WS, d.Dir)
	if err != nil {
		return err
	}
	modulePath := d.ModulePath
	if modulePath == "" {
		if modulePath, err = OriginPath(d.HostDir); err != nil {
			return err
		}
	}
	l, err := Modules(src, modulePath)
	if err != nil {
		return err
	}
	facts := l.Facts
	more, err := Deps(ctx, d.Discovery, src, lock, d.Replacements, l)
	if err != nil {
		return err
	}
	facts = append(facts, more...)
	if more, err = Rules(src, l); err != nil {
		return err
	}
	facts = append(facts, more...)
	// The templates beside the file are read as it is, their entries
	// following its own (REQ-migrate-gen).
	var templates []Template
	for _, name := range src.Templates {
		b, err := util.ReadFile(d.WS, path.Join(d.Dir, name))
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}
		g, err := bufconfig.ParseGen(b)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		templates = append(templates, Template{Name: name, Gen: g})
	}
	stat := func(rel string) (exists, isDir bool) {
		fi, err := d.WS.Stat(path.Join(d.Dir, rel))
		if err != nil {
			return false, false
		}
		return true, fi.IsDir()
	}
	if gen != nil || len(templates) > 0 {
		if more, err = Gen(ctx, gen, templates, d.Replacements, l, d.PluginTags, stat); err != nil {
			return err
		}
		facts = append(facts, more...)
	} else if err := unusedReplacements("plugin", d.Replacements.Plugins, nil, "no "+bufconfig.GenFileName+" lies at the directory"); err != nil {
		return err
	}
	facts = append(facts, unmodeledFacts(src, gen, templates, lock)...)
	// The files, each refused where one exists already, then written.
	type file struct {
		name string
		data []byte
	}
	var files []file
	add := func(name string, data []byte, err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		files = append(files, file{name, data})
		return nil
	}
	for _, dir := range sortedKeys(l.Modules) {
		b, err := modfile.Encode(l.Modules[dir])
		if err := add(path.Join(dir, module.ModuleFileName), b, err); err != nil {
			return err
		}
	}
	if l.Workspace != nil {
		b, err := workspace.Encode(l.Workspace)
		if err := add(workspace.FileName, b, err); err != nil {
			return err
		}
	}
	b, err := lintfile.Encode(l.Lint)
	if err := add(lintfile.FileName, b, err); err != nil {
		return err
	}
	if l.Gen != nil {
		b, err := genfile.Encode(l.Gen)
		if err := add(genfile.FileName, b, err); err != nil {
			return err
		}
	}
	// The refusal set: every file to be written, and the lockfile the
	// tidy writes at the configuration's directory, the resolution
	// root — one in a module's directory below refused the same, no
	// workspace admitting it — checked before the first write.
	refused := make([]string, 0, len(files)+len(l.Modules)+1)
	for _, f := range files {
		refused = append(refused, f.name)
	}
	refused = append(refused, workspace.LockFileName)
	for _, dir := range sortedKeys(l.Modules) {
		if dir != "." {
			refused = append(refused, path.Join(dir, workspace.LockFileName))
		}
	}
	for _, name := range refused {
		if _, err := d.WS.Stat(path.Join(d.Dir, name)); err == nil {
			return fmt.Errorf("%s exists: the migration writes no file over one", name)
		} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	// From the first write on, the report is printed whatever follows:
	// the files are on disk and the user reads what was done.
	report := func(final error) error {
		unmapped := false
		for _, f := range facts {
			form := " -> "
			if !f.Mapped {
				form, unmapped = " !! ", true
			}
			if _, err := fmt.Fprintln(d.Out, f.Source+form+f.Text); err != nil {
				return err
			}
		}
		if final != nil {
			return fmt.Errorf("the files are written; %w", final)
		}
		if unmapped {
			return ErrUnmapped
		}
		return nil
	}
	for _, f := range files {
		if err := atomicfile.Write(d.WS, path.Join(d.Dir, f.name), ".pb-", 0o644, f.data); err != nil {
			return report(fmt.Errorf("writing %s: %w", f.name, err))
		}
		facts = append(facts, mapped(f.name, "written"))
	}
	more, err = rewriteComments(d.WS, d.Dir, l)
	facts = append(facts, more...)
	if err != nil {
		return report(fmt.Errorf("the comments' rewriting failed: %w", err))
	}
	if err := d.Tidy(ctx); err != nil {
		return report(fmt.Errorf("the tidy ending the migration failed: %w", err))
	}
	if _, err := d.WS.Stat(path.Join(d.Dir, workspace.LockFileName)); err == nil {
		facts = append(facts, mapped(workspace.LockFileName, "written by the tidy"))
	}
	return report(nil)
}

// unmodeledFacts is every key the readers passed over, in every buf
// file read, an unmapped fact naming it by file and key
// (REQ-migrate-verb); a plugin entry's own keys are the generation
// step's.
func unmodeledFacts(src *Source, gen *bufconfig.Gen, templates []Template, lock *bufconfig.Lock) []Fact {
	var facts []Fact
	note := func(file string, keys []bufconfig.Unmodeled) {
		for _, k := range keys {
			facts = append(facts, unmapped(file+" "+string(k), "a key the migration does not model"))
		}
	}
	if src.File != nil {
		note(bufconfig.FileName, src.File.Unmodeled)
	}
	if src.Work != nil {
		note(bufconfig.WorkFileName, src.Work.Unmodeled)
	}
	for _, dir := range sortedKeys(src.Members) {
		note(path.Join(dir, bufconfig.FileName), src.Members[dir].Unmodeled)
	}
	if gen != nil {
		note(bufconfig.GenFileName, gen.Unmodeled)
	}
	for _, t := range templates {
		note(t.Name, t.Gen.Unmodeled)
	}
	if lock != nil {
		note(bufconfig.LockFileName, lock.Unmodeled)
	}
	for _, dir := range sortedKeys(src.MemberLocks) {
		note(path.Join(dir, bufconfig.LockFileName), src.MemberLocks[dir].Unmodeled)
	}
	return facts
}

// rewriteComments rewrites the suppression comments of every regular
// proto file under each module buf honored them in
// (REQ-migrate-comments): a file with any rewritten is written back
// and reported with its count; a rewritten directive pb does not
// read where it stands, one naming a rule whose finding carries no
// position, and one in a block comment are each an unmapped fact
// naming the file and line. The facts made before a failure are
// returned with it, the report owing them.
func rewriteComments(ws billy.Filesystem, dir string, l *Layout) ([]Fact, error) {
	var facts []Fact
	for _, mod := range sortedKeys(l.CommentIgnores) {
		if !l.CommentIgnores[mod] {
			continue
		}
		root := path.Join(dir, mod)
		var protos []string
		err := util.Walk(ws, root, func(p string, info fs.FileInfo, err error) error {
			if err != nil {
				return err
			}
			// A regular file alone: a symbolic link is carried as a link
			// by a module's file set, never followed, and stays one.
			if info.Mode().IsRegular() && strings.HasSuffix(p, ".proto") {
				protos = append(protos, p)
			}
			return nil
		})
		if err != nil {
			return facts, fmt.Errorf("walking %s: %w", root, err)
		}
		sort.Strings(protos)
		for _, p := range protos {
			name := p
			if dir != "." {
				name = strings.TrimPrefix(p, dir+"/")
			}
			b, err := util.ReadFile(ws, p)
			if err != nil {
				return facts, fmt.Errorf("reading %s: %w", name, err)
			}
			r := RewriteComments(b)
			if r.Rewritten == 0 && len(r.Block) == 0 {
				continue
			}
			if r.Rewritten > 0 {
				if err := atomicfile.Write(ws, p, ".pb-", 0o644, r.Text); err != nil {
					return facts, fmt.Errorf("writing %s: %w", name, err)
				}
				facts = append(facts, mapped(name, fmt.Sprintf("%d suppression comments rewritten to pb:ignore", r.Rewritten)))
			}
			for _, line := range r.Displaced {
				facts = append(facts, unmapped(fmt.Sprintf("%s:%d", name, line), "a directive pb reads not where it stands: pb reads the comment block leading a finding's line, a declaration's first, a file rule's the block leading the file's first line of code; no finding sits on an option, reserved, extensions, import, package, syntax or edition line, nor on a body's closing brace"))
			}
			for _, line := range r.Unplaced {
				facts = append(facts, unmapped(fmt.Sprintf("%s:%d", name, line), "names a package or set rule, whose finding carries no position: no comment suppresses it, an ignore entry does"))
			}
			for _, line := range r.Block {
				facts = append(facts, unmapped(fmt.Sprintf("%s:%d", name, line), "a directive in a block comment, which pb reads not"))
			}
		}
	}
	return facts, nil
}
