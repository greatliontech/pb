// Package workspace implements the resolution root: a workspace file
// (pb.work) groups local modules so they resolve against each other's
// working copies instead of published versions, with one lockfile at
// the root; absent a workspace file, a module's own root is a
// single-module workspace in all but name (REQ-work-default).
//
// The package owns the decision surfaces the resolver consumes: which
// module paths are local (IsLocal — the unconditional override of
// REQ-work-local-resolution) and what requirement set external version
// selection runs over (Requirements — the union of the workspace
// modules' declarations, REQ-work-external-resolution). Local paths
// never enter that union: the override is unconditional, so offering a
// local path to external selection could only fetch a published copy
// of a module the workspace already provides.
package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/greatliontech/pb/internal/module"

	"github.com/goccy/go-yaml/ast"
	"github.com/greatliontech/pb/internal/contractfile"
	"github.com/greatliontech/pb/internal/module/modfile"
	"github.com/greatliontech/pb/internal/module/mvs"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/rootpath"
)

// FileName is the workspace file's name at the workspace root.
const FileName = "pb.work"

// LockFileName is the lockfile's name at the resolution root — a pure
// function of the root and nothing else (REQ-work-lockfile): no API
// takes a per-module lockfile location.
const LockFileName = "pb.lock"

// ErrInvalid marks a workspace file violating the schema; ErrNoRoot
// marks a directory under no module or workspace at all.
var (
	ErrInvalid = errors.New("invalid workspace file")
	ErrNoRoot  = errors.New("no module or workspace root")
)

// File is a parsed workspace file: the use list, in file order, and
// the replacements, by the module path each replaces.
type File struct {
	Use     []string
	Replace map[string]Replacement
}

// Replacement is the module pair a replaced module path reads from
// (REQ-work-replace): another module's path at an exact version, whose
// requirements and files stand for every version of the replaced path.
type Replacement struct {
	Path    string
	Version version.Version
}

// String spells the replacement as the workspace file writes it.
func (r Replacement) String() string { return r.Path + "@" + r.Version.String() }

// Parse decodes and validates workspace-file bytes (REQ-work-schema):
// the top-level key use, a non-empty list of relative directory paths,
// and optionally replace, a non-empty mapping from module path to
// `<module path>@<version>`.
func Parse(data []byte) (*File, error) {
	mapping, err := contractfile.Doc(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if mapping == nil {
		return nil, fmt.Errorf("%w: missing use key", ErrInvalid)
	}
	f := &File{}
	for _, kv := range mapping.Values {
		key := kv.Key.(*ast.StringNode).Value // admissibility guarantees string keys
		if key == "replace" {
			replace, err := parseReplace(kv.Value)
			if err != nil {
				return nil, err
			}
			f.Replace = replace
			continue
		}
		if key != "use" {
			return nil, fmt.Errorf("%w: unknown key %q", ErrInvalid, key)
		}
		seq, ok := kv.Value.(*ast.SequenceNode)
		if !ok {
			return nil, fmt.Errorf("%w: use must be a list", ErrInvalid)
		}
		entries := make([]string, len(seq.Values))
		for i, n := range seq.Values {
			s, ok := n.(*ast.StringNode)
			if !ok {
				return nil, fmt.Errorf("%w: use[%d] must be a string", ErrInvalid, i)
			}
			entries[i] = s.Value
		}
		dirs, err := cleanUse(entries)
		if err != nil {
			return nil, err
		}
		f.Use = dirs
	}
	if len(f.Use) == 0 {
		return nil, fmt.Errorf("%w: missing use key", ErrInvalid)
	}
	return f, nil
}

// parseReplace reads the replace mapping: each key a module path, each
// value `<module path>@<version>`, held to validateReplace.
func parseReplace(n ast.Node) (map[string]Replacement, error) {
	m, ok := n.(*ast.MappingNode)
	if !ok || len(m.Values) == 0 {
		return nil, fmt.Errorf("%w: replace must be a non-empty mapping", ErrInvalid)
	}
	replace := make(map[string]Replacement, len(m.Values))
	for _, kv := range m.Values {
		replaced := kv.Key.(*ast.StringNode).Value
		s, ok := kv.Value.(*ast.StringNode)
		if !ok {
			return nil, fmt.Errorf("%w: replace %q must be a module path at a version", ErrInvalid, replaced)
		}
		with, err := parseReplacement(s.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: replace %q: %v", ErrInvalid, replaced, err)
		}
		replace[replaced] = with
	}
	if err := validateReplace(replace); err != nil {
		return nil, err
	}
	return replace, nil
}

// parseReplacement reads `<module path>@<version>`: a module path has
// no @ (REQ-resolve-path-syntax), so the one separator is its last.
func parseReplacement(s string) (Replacement, error) {
	at := strings.LastIndex(s, "@")
	if at <= 0 {
		return Replacement{}, fmt.Errorf("replacement %q is not <module path>@<version>", s)
	}
	p, v := s[:at], s[at+1:]
	if err := module.ValidatePath(p); err != nil {
		return Replacement{}, fmt.Errorf("replacement %q: %v", s, err)
	}
	ver, err := version.Parse(v)
	if err != nil {
		return Replacement{}, fmt.Errorf("replacement %q: version %q is not a tagged release or pseudo-version", s, v)
	}
	return Replacement{Path: p, Version: ver}, nil
}

// validateReplace holds the replacements to the file's shape: a
// replaced path is a module path (REQ-work-schema), and no replacement
// names a replaced path (REQ-work-replace-names) — the graph reads
// through one step, never a chain, and a path naming itself is the
// one-step chain.
func validateReplace(replace map[string]Replacement) error {
	for _, replaced := range slices.Sorted(maps.Keys(replace)) {
		with := replace[replaced]
		if err := module.ValidatePath(replaced); err != nil {
			return fmt.Errorf("%w: replace key %q: %v", ErrInvalid, replaced, err)
		}
		if _, chained := replace[with.Path]; chained {
			return fmt.Errorf("%w: replace %q names %s, which is itself replaced", ErrInvalid, replaced, with.Path)
		}
	}
	return nil
}

// Encode renders the file canonically (REQ-work-emission): UTF-8, LF,
// two-space indent, the use entries cleaned and sorted in raw-byte
// order, each spelled as contractfile.Spell has it — a directory
// named like a number quoted, so the file reads it back as the
// directory under every reader. The file is validated first and the
// rendering held to its reading — Encode never emits what Parse
// rejects or reads as a different file.
func Encode(f *File) ([]byte, error) {
	if f == nil || len(f.Use) == 0 {
		return nil, fmt.Errorf("%w: missing use key", ErrInvalid)
	}
	dirs, err := cleanUse(f.Use)
	if err != nil {
		return nil, err
	}
	slices.Sort(dirs)
	// The replacements are validated by the read-back alone: an empty
	// mapping renders as no key, which reads back as none.
	want := &File{Use: dirs, Replace: f.Replace}
	return contractfile.Emit(func(w *contractfile.Writer) {
		w.List("use", dirs)
		if len(f.Replace) > 0 {
			w.Mapping("replace", func() {
				for _, replaced := range slices.Sorted(maps.Keys(f.Replace)) {
					w.Scalar(replaced, f.Replace[replaced].String())
				}
			})
		}
	}, Parse, want, func(a, b *File) bool {
		return slices.Equal(a.Use, b.Use) && maps.EqualFunc(a.Replace, b.Replace, func(x, y Replacement) bool {
			return x.Path == y.Path && version.Compare(x.Version, y.Version) == 0 && x.Version.String() == y.Version.String()
		})
	}, ErrInvalid)
}

// cleanUse validates the use entries as one list: each cleaned, no
// two the same directory.
func cleanUse(entries []string) ([]string, error) {
	dirs := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for i, s := range entries {
		dir, err := cleanUseDir(s)
		if err != nil {
			return nil, fmt.Errorf("%w: use[%d]: %v", ErrInvalid, i, err)
		}
		if seen[dir] {
			return nil, fmt.Errorf("%w: use[%d]: duplicate directory %q", ErrInvalid, i, dir)
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	return dirs, nil
}

// cleanUseDir validates a use entry: a relative, root-contained
// directory path, cleaned in place. "." is the workspace root itself.
func cleanUseDir(s string) (string, error) {
	c, err := rootpath.Clean(s, "the workspace root")
	if err != nil {
		return "", fmt.Errorf("use directory: %w", err)
	}
	return c, nil
}

// Module is one workspace module: its root-relative directory and its
// parsed module file.
type Module struct {
	Dir  string
	File *modfile.File
}

// Root is a loaded resolution root.
type Root struct {
	Dir     string // directory of the root within the loaded fs
	File    *File  // nil for the single-module default
	Modules []Module
}

// Find walks up from dir toward the filesystem root and returns the
// nearest directory containing a workspace file or, absent one on the
// whole walk, the nearest containing a module file — the single-module
// default (REQ-work-default). A workspace anywhere above wins over a
// nearer module file: the module is inside the workspace.
func Find(fsys fs.FS, dir string) (rootDir string, isWorkspace bool, err error) {
	moduleDir, haveModule := "", false
	for d := path.Clean(dir); ; d = path.Dir(d) {
		if ok, err := exists(fsys, d, FileName); err != nil {
			return "", false, err
		} else if ok {
			return d, true, nil
		}
		if !haveModule {
			if ok, err := exists(fsys, d, module.ModuleFileName); err != nil {
				return "", false, err
			} else if ok {
				moduleDir, haveModule = d, true
			}
		}
		if d == "." {
			break
		}
	}
	if haveModule {
		return moduleDir, false, nil
	}
	return "", false, fmt.Errorf("%w above %q", ErrNoRoot, dir)
}

func exists(fsys fs.FS, dir, name string) (bool, error) {
	_, err := fs.Stat(fsys, path.Join(dir, name))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// Load materializes the resolution root at rootDir: with a workspace
// file, every use entry must be the root of a declared module (a
// module file it parses), and no used module may carry a lockfile of
// its own — the workspace has exactly one, at the root
// (REQ-work-lockfile). Without one, the directory's own module file is
// the single workspace module.
func Load(fsys fs.FS, rootDir string) (*Root, error) {
	rootDir = path.Clean(rootDir)
	wf, err := readWorkFile(fsys, rootDir)
	if err != nil {
		return nil, err
	}
	r := &Root{Dir: rootDir, File: wf}
	use := []string{"."}
	if wf != nil {
		use = wf.Use
	}
	seenPath := map[string]string{}
	for _, dir := range use {
		mdir := path.Join(rootDir, dir)
		data, err := fs.ReadFile(fsys, path.Join(mdir, module.ModuleFileName))
		if err != nil {
			return nil, fmt.Errorf("workspace: use directory %q is not a declared module root: %v", dir, err)
		}
		mf, err := modfile.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("workspace: module at %q: %v", dir, err)
		}
		if prev, dup := seenPath[mf.Module]; dup {
			return nil, fmt.Errorf("workspace: module path %q declared by both %q and %q: the local override would be ambiguous", mf.Module, prev, dir)
		}
		seenPath[mf.Module] = dir
		// The root's own lockfile is the workspace lockfile; the
		// single-module default always uses ".", so the directory test
		// alone exempts exactly the root.
		if dir != "." {
			if ok, err := exists(fsys, mdir, LockFileName); err != nil {
				return nil, err
			} else if ok {
				return nil, fmt.Errorf("workspace: module at %q carries its own lockfile; a workspace has exactly one, at the root", dir)
			}
		}
		r.Modules = append(r.Modules, Module{Dir: dir, File: mf})
	}
	// A replacement answers for an external path with another external
	// pair (REQ-work-replace-names): a workspace module already answers
	// for its own path through the local override, and a workspace
	// module named as a replacement would pin the working copy's path
	// to a published version, which that override never lets a build
	// read.
	if wf != nil {
		for _, replaced := range slices.Sorted(maps.Keys(wf.Replace)) {
			if dir, local := seenPath[replaced]; local {
				return nil, fmt.Errorf("workspace: replace %q names the workspace module at %q, which the local override already answers for", replaced, dir)
			}
			if dir, local := seenPath[wf.Replace[replaced].Path]; local {
				return nil, fmt.Errorf("workspace: replace %q names %s, the workspace module at %q, as a pinned replacement", replaced, wf.Replace[replaced], dir)
			}
		}
	}
	return r, nil
}

// Source names the pair whose module file and archive answer for a
// build-list pair: the root's replacement where its workspace file
// replaces the path (REQ-work-replace), every version of the path
// alike, else the pair itself. Every reader of a pair — selection's
// requirement loader, the file sets' archive loader, download and the
// lockfile's reachable pins — asks this one function, so a replaced
// path's requirements and its files can never come from two different
// pairs, and the replaced path itself is never fetched. A pair is
// replaced exactly when the source's path differs: a path never
// replaces itself (REQ-work-replace-names). Only the workspace file
// carries replacements: a module file never does, so a replacement
// never reaches a consumer of the workspace's published modules.
func (r *Root) Source(modulePath string, v version.Version) (string, version.Version) {
	if r.File != nil {
		if with, ok := r.File.Replace[modulePath]; ok {
			return with.Path, with.Version
		}
	}
	return modulePath, v
}

func readWorkFile(fsys fs.FS, rootDir string) (*File, error) {
	data, err := fs.ReadFile(fsys, path.Join(rootDir, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// IsLocal reports whether modulePath is a workspace module — the
// unconditional local override of REQ-work-local-resolution — and its
// root-relative directory.
func (r *Root) IsLocal(modulePath string) (dir string, ok bool) {
	for _, m := range r.Modules {
		if m.File.Module == modulePath {
			return m.Dir, true
		}
	}
	return "", false
}

// Requirements is the requirement set external version selection runs
// over (REQ-work-external-resolution): the union of every workspace
// module's declared edges — every distinct (path, version) pair, not a
// per-path maximum, because reachable-graph MVS traverses superseded
// pairs' own requirements too and a collapsed edge would silently drop
// its transitive minimums from selection. Workspace-local paths are
// excluded: their override is unconditional, so external selection
// over them could only fetch a published copy alongside the working
// copy. The result is sorted by path, then version.
func (r *Root) Requirements() ([]mvs.Requirement, error) {
	type edge struct{ path, version string }
	seen := map[edge]bool{}
	var union []mvs.Requirement
	for _, m := range r.Modules {
		// Sorted key iteration keeps construction deterministic, so the
		// final order is a function of the declarations alone.
		for _, p := range slices.Sorted(maps.Keys(m.File.Deps)) {
			v := m.File.Deps[p]
			if _, local := r.IsLocal(p); local {
				continue
			}
			if seen[edge{p, v}] {
				continue
			}
			seen[edge{p, v}] = true
			ver, err := version.Parse(v)
			if err != nil {
				return nil, fmt.Errorf("workspace: requirement %s@%s: %v", p, v, err)
			}
			union = append(union, mvs.Requirement{Path: p, Version: ver})
		}
	}
	slices.SortFunc(union, func(a, b mvs.Requirement) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return version.Compare(a.Version, b.Version)
	})
	return union, nil
}

// ErrNotMember marks a directory whose own module a governing workspace
// does not use (REQ-work-membership).
var ErrNotMember = errors.New("module is not used by the governing workspace")

// LoadFor locates and loads the resolution root governing dir — Find
// then Load — and enforces membership (REQ-work-membership): when the
// governing root is a workspace and dir sits inside a module of its
// own, that module must be one the workspace uses; resolving from an
// unlisted module would neither treat it as local nor carry its
// requirements, answering for the wrong root. A directory inside the
// workspace but under no module operates against the workspace itself.
func LoadFor(fsys fs.FS, dir string) (*Root, error) {
	rootDir, isWorkspace, err := Find(fsys, dir)
	if err != nil {
		return nil, err
	}
	root, err := Load(fsys, rootDir)
	if err != nil {
		return nil, err
	}
	if !isWorkspace {
		return root, nil
	}
	moduleDir, hasModule, err := nearestModule(fsys, dir, rootDir)
	if err != nil {
		return nil, err
	}
	if !hasModule {
		return root, nil
	}
	rel := "."
	if moduleDir != rootDir {
		rel = strings.TrimPrefix(moduleDir, rootDir+"/")
	}
	for _, m := range root.Modules {
		if m.Dir == rel {
			return root, nil
		}
	}
	return nil, fmt.Errorf("%w: module at %q is inside the workspace at %q but not in its use list", ErrNotMember, moduleDir, rootDir)
}

// nearestModule walks up from dir to rootDir looking for the nearest
// module file — the module dir itself belongs to, if any.
func nearestModule(fsys fs.FS, dir, rootDir string) (string, bool, error) {
	for d := path.Clean(dir); ; d = path.Dir(d) {
		ok, err := exists(fsys, d, module.ModuleFileName)
		if err != nil {
			return "", false, err
		}
		if ok {
			return d, true, nil
		}
		if d == rootDir || d == "." {
			return "", false, nil
		}
	}
}
