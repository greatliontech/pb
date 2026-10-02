package dep

import (
	"context"
	"errors"
	"path"
	"strings"

	"github.com/bufbuild/protocompile/linker"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/workspace"
	"github.com/greatliontech/pb/internal/proto/compile"
	"github.com/greatliontech/pb/internal/proto/modfiles"
	"github.com/greatliontech/pb/internal/source/fetch"
)

// Overlay is the bytes a judgement reads in place of the tree's at
// each path, root-relative: an editor's open documents (lsp.md
// REQ-lsp-overlay). A path under a module's directory the tree does
// not hold is read as a file of that module.
type Overlay map[string][]byte

// apply puts the overlay's bytes into the modules the build read
// from the tree — a workspace module's, a directory replacement's —
// each path to the module that owns it (FileOf), the bytes keyed as
// the module's files are, by their path from the module's directory;
// a path no module owns is left out.
func (o Overlay) apply(s *Session, mods []modfiles.Module) {
	for p, b := range o {
		at, rel, ok := s.FileOf(mods, p)
		if !ok {
			continue
		}
		m := mods[at]
		if m.Files == nil {
			m.Files = map[string][]byte{}
			mods[at] = m
		}
		m.Files[rel] = b
	}
}

// FileOf is the module of the build that owns a root-relative path,
// and the path's name within it: the module — a workspace module's
// or a directory replacement's, read from the tree — whose
// directory is the path's longest prefix, unless a directory between
// that one and the file holds a module file, which makes the file a
// nested module's, no module of the build's (modfiles.WorkspaceFiles
// skips a nested module's tree). The one answer to which module a
// tree path falls in: the overlay and the language server's
// classification both read it.
func (s *Session) FileOf(mods []modfiles.Module, rel string) (i int, file string, ok bool) {
	at := -1
	for j, m := range mods {
		if !m.Local && m.Dir == "" {
			continue
		}
		if !under(m.Dir, rel) {
			continue
		}
		if at < 0 || len(m.Dir) > len(mods[at].Dir) {
			at = j
		}
	}
	if at < 0 {
		return -1, "", false
	}
	file = rel
	if d := mods[at].Dir; d != "" {
		file = strings.TrimPrefix(rel, d+"/")
	}
	// A module file in a directory below the module's and above the
	// file makes the file a nested module's.
	for dir := path.Dir(file); dir != "." && dir != "/"; dir = path.Dir(dir) {
		if _, err := s.WS.Stat(path.Join(s.Root.Dir, mods[at].Dir, dir, module.ModuleFileName)); err == nil {
			return -1, "", false
		}
	}
	return at, file, true
}

// under reports whether p lies under dir, the root's own directory
// ("") holding every path.
func under(dir, p string) bool {
	return dir == "" || p == dir || strings.HasPrefix(p, dir+"/")
}

// Judgement is one judgement of the build the language server
// publishes (lsp.md REQ-lsp-diagnostics): the modules as read, with
// the overlay in place; the compile's errors where the build does not
// compile — every error, under compile.CompileAll, an ambiguous
// provider's among them with no file — or else the checked schema
// and the lint findings pb lint would print, admitted as the verb
// admits them; the count of lint rules enabled, zero meaning the
// verb would say nothing.
type Judgement struct {
	Mods     []modfiles.Module
	Compile  []compile.Error
	Files    linker.Files
	Findings []check.Finding
	Rules    int
}

// Compiles reports whether the build compiled.
func (j *Judgement) Compiles() bool { return len(j.Compile) == 0 }

// Judge judges the build with the overlay in place as pb lint judges
// its own: the build assembled as the verb assembles it over the same
// session, compiled — every error collected where it does not compile
// — and every enabled lint rule evaluated over the checked modules
// under the selections governing them, the findings admitted as the
// verb admits them (lsp.md REQ-lsp-parity). An operational failure —
// the session's resolution, a lint file that does not parse — is the
// error; a pair the lockfile does not pin is a *fetch.UnpinnedError
// under a read-only session.
func Judge(ctx context.Context, s *Session, overlay Overlay) (*Judgement, error) {
	run, err := assembleRun(ctx, s, check.KindLint, overlay)
	if err != nil {
		return nil, err
	}
	j := &Judgement{Mods: run.mods, Rules: run.rules}
	result, err := compile.CompileAll(ctx, run.mods)
	if err != nil {
		var errs *compile.Errors
		if !errors.As(err, &errs) {
			return nil, err
		}
		j.Compile = errs.List
		return j, nil
	}
	run.compiled(result)
	j.Files = result.Files
	if run.rules == 0 {
		return j, nil
	}
	if j.Findings, err = run.lintFindings(); err != nil {
		return nil, err
	}
	return j, nil
}

// Pair is a module path at a version, as a requirement names it.
type Pair struct {
	Path, Version string
}

func (p Pair) String() string { return p.Path + "@" + p.Version }

// Unpinned lists the pairs the build requires that the lockfile does
// not pin, among those the resolution root declares: each workspace
// module's requirements, read through the root's replacements, that
// no module of the workspace satisfies, and each external ruleset the
// lint file imports — the pairs a read-only session refuses before
// resolving (lsp.md REQ-lsp-unpinned). A pair a declared one requires
// in turn is known only once the declared one is pinned and read, so
// the list is of the first tier; the session's resolution names the
// next as it meets it.
func (s *Session) Unpinned() ([]Pair, error) {
	var out []Pair
	seen := map[Pair]bool{}
	reqs, err := s.Root.Requirements()
	if err != nil {
		return nil, err
	}
	for _, r := range reqs {
		if _, local := s.Root.IsLocal(r.Path); local {
			continue
		}
		src := s.Root.Source(r.Path, r.Version)
		if src.Module != nil {
			continue
		}
		p := Pair{Path: src.Path, Version: src.Version.String()}
		if _, ok := s.Lock.ModulePins().Module(p.Path, p.Version); ok || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	lf, err := s.LintFile()
	if err != nil {
		return nil, err
	}
	for _, imp := range lf.Rulesets {
		if imp.Version == "" {
			continue
		}
		p := Pair{Path: imp.Path, Version: imp.Version}
		if _, ok := s.Lock.RulesetPins().Module(p.Path, p.Version); ok || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, nil
}

// UnpinnedIn tells the pair an error of the session names unpinned,
// where it is a read-only client's refusal.
func UnpinnedIn(err error) (Pair, bool) {
	var u *fetch.UnpinnedError
	if errors.As(err, &u) {
		return Pair{Path: u.Path, Version: u.Version}, true
	}
	return Pair{}, false
}

// LockPath is the lockfile's path within the working tree, whether or
// not one exists.
func (s *Session) LockPath() string { return path.Join(s.Root.Dir, workspace.LockFileName) }

// BuildHome is the file a judgement of the build as a whole lands on
// (lsp.md, the build home term): the workspace file where one governs
// the session, else the module file of the module the session's
// directory lies in — the root's, a single-module root being its
// module's directory. The path is within the working tree.
func (s *Session) BuildHome() string {
	if s.Root.File != nil {
		return path.Join(s.Root.Dir, workspace.FileName)
	}
	return path.Join(s.Root.Dir, module.ModuleFileName)
}

// ModuleFile is the module file of the workspace module at dir
// (root-relative), within the working tree.
func (s *Session) ModuleFile(dir string) string {
	return path.Join(s.Root.Dir, dir, module.ModuleFileName)
}

// RootRel is the root-relative path of a path within the working
// tree, or false where the path lies outside the resolution root.
func (s *Session) RootRel(tree string) (string, bool) {
	if s.Root.Dir == "" || s.Root.Dir == "." {
		return tree, true
	}
	if tree == s.Root.Dir {
		return "", true
	}
	if !strings.HasPrefix(tree, s.Root.Dir+"/") {
		return "", false
	}
	return strings.TrimPrefix(tree, s.Root.Dir+"/"), true
}
