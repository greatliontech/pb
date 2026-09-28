package dep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/go-git/go-billy/v6/util"

	"github.com/greatliontech/pb/internal/atomicfile"
	"github.com/greatliontech/pb/internal/module/archive"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/module/workspace"
	"github.com/greatliontech/pb/internal/proto/compile"
	"github.com/greatliontech/pb/internal/proto/modfiles"
)

// ExportOptions are the export verb's flags (export.md).
type ExportOptions struct {
	// All exports every protobuf file of every module of the build,
	// not the import closure alone (REQ-export-selection).
	All bool
	// Exclude names build-list modules whose files are not written —
	// and nothing else: the closure is computed over the whole build,
	// so what an excluded module's files import is written whenever
	// its own module is not excluded (REQ-export-exclusion). A path
	// naming no build-list module, naming a workspace module, or
	// given twice fails the export before anything is written.
	Exclude []string
}

// Export is the export verb (export.md): the build compiled as
// generation compiles it (REQ-export-build), the import closure or,
// under All, every file of every module selected (REQ-export-selection),
// the excluded modules' files then set aside (REQ-export-exclusion),
// the selected paths of every module held together to the archive's
// path and case-collision rules (REQ-export-layout), and the tree
// written whole into dir — each file at its import path, a regular
// file whatever its source's mode or kind (REQ-export-materialization)
// — then the report, one line per module in build order, an excluded
// module's saying so, and one for the whole (REQ-export-report).
//
// dir is the output directory as the session's working tree names it;
// symbolic links on the way are resolved here, and the tree is written
// at the resolved path, the one judged: absent or an empty directory,
// outside every workspace module's directory, judged before the build
// is resolved so a refusal costs no fetch (REQ-export-output). given
// is the directory as the user spelled it, for the report.
func Export(ctx context.Context, s *Session, dir, given string, opts ExportOptions, out io.Writer) error {
	dir, err := s.exportTarget(dir)
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}
	// A path given twice or naming a workspace module is refused before
	// the build is resolved, the workspace known already; whether a path
	// names a module of the build needs the build list.
	for i, e := range opts.Exclude {
		if slices.Contains(opts.Exclude[:i], e) {
			return fmt.Errorf("export: --exclude %s given twice", e)
		}
		if slices.ContainsFunc(s.Root.Modules, func(m workspace.Module) bool { return m.File.Module == e }) {
			return fmt.Errorf("export: --exclude %s names a workspace module, whose files are the export's subject", e)
		}
		// The build read a replaced path's replacement, which no pin
		// under that path names: nothing the consumer could supply and
		// hold to a pin stands for it.
		if s.Root.Replaced(e) {
			return fmt.Errorf("export: --exclude %s names a replaced path, read from %s: no pin under it names what the build used", e, s.Root.Source(e, version.Version{}))
		}
	}
	_, mods, err := s.Modules(ctx)
	if err != nil {
		return err
	}
	excluded := make([]bool, len(mods))
	for _, e := range opts.Exclude {
		i := slices.IndexFunc(mods, func(m modfiles.Module) bool { return m.Path == e })
		if i < 0 {
			return fmt.Errorf("export: --exclude %s names no module of the build", e)
		}
		excluded[i] = true
	}
	compiled, err := compile.Compile(ctx, mods)
	if err != nil {
		return err
	}
	// Compile refused any ambiguity, so the index cannot fail here.
	index, err := compile.Providers(mods)
	if err != nil {
		return err
	}
	// The selection, per module in build order, each module's paths
	// sorted: Protos and Closure both are.
	selected := make([][]string, len(mods))
	if opts.All {
		for i, m := range mods {
			selected[i] = m.Protos()
		}
	} else {
		for _, p := range compiled.Closure() {
			// The linker and the index are two sources; a file the
			// compile reached that no module provides is a fault,
			// never an empty file.
			i, ok := index[p]
			if !ok {
				return fmt.Errorf("export: %s is reached by the compile but provided by no module", p)
			}
			selected[i] = append(selected[i], p)
		}
	}
	// Exclusion prunes what is written, never what was walked.
	for i := range selected {
		if excluded[i] {
			selected[i] = nil
		}
	}
	var paths []string
	for _, ps := range selected {
		paths = append(paths, ps...)
	}
	if err := archive.ValidatePaths(paths); err != nil {
		var c *archive.CollisionError
		if errors.As(err, &c) {
			return fmt.Errorf("export: the tree cannot hold both %s of %s and %s of %s: %w", c.Paths[0], mods[index[c.Paths[0]]].Label(), c.Paths[1], mods[index[c.Paths[1]]].Label(), err)
		}
		return fmt.Errorf("export: %w", err)
	}
	// Every written file is a regular file at mode 0644: the bytes are
	// a build-list module's from its verified archive — ZipFiles holds
	// regular files alone, a link or a submodule entry never among
	// them — or a workspace module's as generation compiles them
	// (module-archive.md REQ-archive-no-exec-materialization).
	err = atomicfile.WriteDir(s.WS, dir, ".pb-export-", func(tmp string) error {
		for i, ps := range selected {
			for _, p := range ps {
				if err := util.WriteFile(s.WS, path.Join(tmp, p), mods[i].Files[p], 0o644); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}
	total := 0
	for i, m := range mods {
		if excluded[i] {
			fmt.Fprintf(out, "%s: excluded\n", m.Label())
			continue
		}
		fmt.Fprintf(out, "%s: %d file(s)\n", m.Label(), len(selected[i]))
		total += len(selected[i])
	}
	fmt.Fprintf(out, "exported %d file(s) to %s\n", total, given)
	return nil
}

// exportTarget judges the output directory before anything is
// resolved (REQ-export-output). Symbolic links on the way are
// resolved here, a dangling one refused, so the judgment is the
// caller's spelling as the filesystem reads it; the resolved path lies
// outside every workspace module's directory — the files under one
// are that module's on the next load, and a single-module workspace's
// module directory is the workspace root — judged on the clean path
// and, for every existing ancestor, on the directory's identity, so a
// spelling a case-insensitive filesystem reads as a module directory
// is refused as the module's own; the parent exists; and the
// directory is absent, or empty — a symbolic link, a file or a
// non-empty directory is refused. The resolved path is returned: the
// one judged is the one written. The link resolution and the parent's
// judgment are shared with the build (outputPath), the containment
// rule judged between them so a path inside a module is refused as
// such whether or not its parent exists.
func (s *Session) exportTarget(dir string) (string, error) {
	resolved, err := s.resolveLinks(dir)
	if err != nil {
		return "", err
	}
	modules := make([]string, len(s.Root.Modules))
	for i, m := range s.Root.Modules {
		modules[i] = path.Join(s.Root.Dir, m.Dir)
		if within(resolved, modules[i]) {
			return "", fmt.Errorf("%s lies inside the workspace module %s at %s: its files would be the module's on the next load", dir, m.File.Module, modules[i])
		}
	}
	for p := resolved; ; p = path.Dir(p) {
		fi, err := s.WS.Stat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return "", err
		default:
			for i, md := range modules {
				if mfi, err := s.WS.Stat(md); err == nil && sameDirectory(fi, mfi) {
					return "", fmt.Errorf("%s lies inside the workspace module %s at %s (%s is that directory): its files would be the module's on the next load", dir, s.Root.Modules[i].File.Module, md, p)
				}
			}
		}
		if p == "." || p == "/" {
			break
		}
	}
	if err := s.parentDirectory(resolved, dir); err != nil {
		return "", err
	}
	fi, err := s.WS.Lstat(resolved)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return resolved, nil
	case err != nil:
		return "", err
	case fi.Mode()&fs.ModeSymlink != 0:
		return "", fmt.Errorf("%s is a symbolic link", dir)
	case !fi.IsDir():
		return "", fmt.Errorf("%s exists and is not a directory", dir)
	}
	entries, err := s.WS.ReadDir(resolved)
	if err != nil {
		return "", err
	}
	if len(entries) != 0 {
		return "", fmt.Errorf("%s exists and is not empty", dir)
	}
	return resolved, nil
}

// outputPath is an output path as a verb writes it: the symbolic
// links among its ancestors resolved (resolveLinks), and its parent
// an existing directory (parentDirectory) — the build's judgment
// whole, the export's around its containment rule (REQ-export-output,
// REQ-build-output). The last component is left to the verb's own
// judgment.
func (s *Session) outputPath(p string) (string, error) {
	resolved, err := s.resolveLinks(p)
	if err != nil {
		return "", err
	}
	if err := s.parentDirectory(resolved, p); err != nil {
		return "", err
	}
	return resolved, nil
}

// parentDirectory requires the parent of resolved to be an existing
// directory, given the path as the user spelled it: absence, a
// non-directory and any other failure told apart.
func (s *Session) parentDirectory(resolved, given string) error {
	parent := path.Dir(resolved)
	if parent == "." {
		return nil
	}
	fi, err := s.WS.Stat(parent)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("the parent directory of %s does not exist", given)
	case err != nil:
		return fmt.Errorf("the parent directory of %s: %w", given, err)
	case !fi.IsDir():
		return fmt.Errorf("the parent of %s is not a directory", given)
	}
	return nil
}

// resolveLinks follows every symbolic link among dir's ancestors, the
// nearest first, restarting from the link's target — a target the
// tree names, a relative one against the link's own directory, an
// absolute one against the tree's root — until none remains; a
// dangling link, or a chain past forty links, is refused. The last
// component is left as it is: a link there is judged by the caller.
func (s *Session) resolveLinks(dir string) (string, error) {
	for range 40 {
		found := false
		for p := path.Dir(dir); p != "." && p != "/"; p = path.Dir(p) {
			fi, err := s.WS.Lstat(p)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return "", err
			}
			if fi.Mode()&fs.ModeSymlink == 0 {
				continue
			}
			target, err := s.WS.Readlink(p)
			if err != nil {
				return "", err
			}
			if path.IsAbs(target) {
				target = strings.TrimPrefix(path.Clean(target), "/")
			} else {
				target = path.Join(path.Dir(p), target)
			}
			if target == ".." || strings.HasPrefix(target, "../") {
				return "", fmt.Errorf("%s passes through %s, a symbolic link leading out of the tree", dir, p)
			}
			if _, err := s.WS.Stat(target); err != nil {
				return "", fmt.Errorf("%s passes through %s, a dangling symbolic link", dir, p)
			}
			dir = path.Join(target, strings.TrimPrefix(dir, p+"/"))
			found = true
			break
		}
		if !found {
			return dir, nil
		}
	}
	return "", fmt.Errorf("%s passes through more than forty symbolic links", dir)
}

// within reports whether dir lies at or under base, both clean,
// slash-separated paths relative to one root.
func within(dir, base string) bool {
	base = path.Clean(base)
	if base == "." {
		return dir != ".." && !strings.HasPrefix(dir, "../")
	}
	return dir == base || strings.HasPrefix(dir, base+"/")
}
