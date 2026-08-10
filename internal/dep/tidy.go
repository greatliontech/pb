package dep

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/go-git/go-billy/v6/helper/iofs"
	"github.com/greatliontech/pb/internal/archive"
	"github.com/greatliontech/pb/internal/lockfile"
	"github.com/greatliontech/pb/internal/modfile"
	"github.com/greatliontech/pb/internal/protoimport"
	"github.com/greatliontech/pb/internal/workspace"
)

// tidyRounds bounds the tidy fixpoint. Rewriting declarations to the
// selected versions preserves every selection (each declaration moves
// up to a version the graph already selects), so the graph is stable
// after one changed round and later rounds only verify; the bound
// turns a broken oracle into an error instead of a spin.
const tidyRounds = 10

// Tidy makes each workspace module's declared dependencies exactly the
// modules its own protobuf imports are satisfied by, at the versions
// the tidied graph selects (REQ-dep-tidy): unused declarations drop,
// directly-imported build-list modules gain declarations, an import
// satisfied by nothing fails per REQ-resolve-unsatisfied-imports (tidy
// never invents a dependency), workspace-local imports keep their
// existing declared version (and fail when none exists to keep), and
// pins for pairs outside the tidied graph are removed. Idempotent: a
// second run changes nothing. Module-file rewrites are per-file
// atomic, not transactional: a failed round may leave some files
// rewritten; rerunning after fixing the cause converges.
func Tidy(ctx context.Context, s *Session) error {
	for round := 0; round < tidyRounds; round++ {
		changed, err := tidyOnce(ctx, s)
		if err != nil {
			return err
		}
		if !changed {
			return s.SaveLock()
		}
	}
	return fmt.Errorf("dep tidy: no fixpoint after %d rounds", tidyRounds)
}

func tidyOnce(ctx context.Context, s *Session) (changed bool, err error) {
	list, _, err := s.Driver.BuildList(ctx)
	if err != nil {
		return false, err
	}
	selected := map[string]string{}
	for _, r := range list {
		selected[r.Path] = r.Version.String()
	}

	// The import-relevant view of every module: workspace modules from
	// the working tree, externals from their verified archives.
	type moduleFiles struct {
		path  string
		files map[string][]string // proto file -> imports
	}
	var views []moduleFiles
	provider := map[string]string{} // proto file -> module path
	addView := func(modPath string, protos map[string][]string) {
		views = append(views, moduleFiles{path: modPath, files: protos})
		for f := range protos {
			// First provider wins, deterministically: views are added in
			// deterministic order (workspace use order, then build-list
			// order), and within a view every file maps to the same
			// module path, so map-iteration order cannot change the
			// assignment. Cross-module duplicates are not tidy's to
			// adjudicate — any provider serves for attribution, and
			// generation owns the conflict.
			if _, ok := provider[f]; !ok {
				provider[f] = modPath
			}
		}
	}

	for _, m := range s.Root.Modules {
		protos, err := workspaceProtos(s, m)
		if err != nil {
			return false, err
		}
		addView(m.File.Module, protos)
	}
	for _, r := range list {
		zip, err := s.Client.Zip(ctx, r.Path, r.Version)
		if err != nil {
			return false, err
		}
		files, err := archive.ZipFiles(bytes.NewReader(zip), int64(len(zip)))
		if err != nil {
			return false, err
		}
		protos := map[string][]string{}
		// Sorted iteration keeps this loop's control flow a function of
		// the file set alone.
		for _, p := range slices.Sorted(maps.Keys(files)) {
			if !strings.HasSuffix(p, ".proto") {
				continue
			}
			imports, err := protoimport.Imports(p, files[p])
			if err != nil {
				return false, fmt.Errorf("%s@%s: %s: %w", r.Path, r.Version, p, err)
			}
			protos[p] = imports
		}
		addView(r.Path, protos)
	}

	// Satisfaction over the whole set (REQ-resolve-unsatisfied-imports):
	// tidy never invents a module path for an unsatisfied import.
	check := make([]protoimport.Module, len(views))
	for i, v := range views {
		check[i] = protoimport.Module{Path: v.path, Files: v.files}
	}
	if err := protoimport.Check(check); err != nil {
		return false, err
	}

	// Rewrite each workspace module's declarations to exactly its
	// direct imports.
	for _, m := range s.Root.Modules {
		var view *moduleFiles
		for i := range views {
			if views[i].path == m.File.Module {
				view = &views[i]
				break
			}
		}
		want := map[string]string{}
		for _, imports := range view.files {
			for _, imp := range imports {
				if protoimport.WellKnown(imp) {
					continue
				}
				p, ok := provider[imp]
				if !ok || p == m.File.Module {
					continue
				}
				if _, isLocal := s.Root.IsLocal(p); isLocal {
					v, declared := m.File.Deps[p]
					if !declared {
						return false, fmt.Errorf("dep tidy: %s imports workspace module %s but declares no version for it — tidy cannot invent one; add the dependency with the version consumers should require", m.File.Module, p)
					}
					want[p] = v
					continue
				}
				want[p] = selected[p]
			}
		}
		if depsEqual(m.File.Deps, want) {
			continue
		}
		changed = true
		nf := &modfile.File{Module: m.File.Module, Deps: want}
		b, err := modfile.Encode(nf)
		if err != nil {
			return false, err
		}
		if err := writeFile(s.WS, path.Join(s.Root.Dir, m.Dir, modfile.ModuleFileName), b); err != nil {
			return false, err
		}
		m.File.Deps = want
	}
	if changed {
		return true, nil
	}

	// Stable: prune pins outside the tidied requirement graph.
	edges, err := s.Driver.Graph(ctx)
	if err != nil {
		return false, err
	}
	reachable := map[string]bool{}
	for _, e := range edges {
		reachable[e.Path+"@"+e.Version.String()] = true
	}
	kept := slices.DeleteFunc(slices.Clone(s.Lock.Modules), func(p lockfile.ModulePin) bool {
		return !reachable[p.Path+"@"+p.Version]
	})
	if len(kept) != len(s.Lock.Modules) {
		s.Lock.Modules = kept
	}
	return false, nil
}

// workspaceProtos walks a workspace module's directory for protobuf
// files and their imports, module-root-relative — the include root of
// a workspace module is its own directory.
func workspaceProtos(s *Session, m workspace.Module) (map[string][]string, error) {
	fsys := iofs.New(s.WS)
	base := path.Join(s.Root.Dir, m.Dir)
	protos := map[string][]string{}
	err := fs.WalkDir(fsys, base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// A nested module's files belong to the nested module.
			if p != base {
				if _, err := fs.Stat(fsys, path.Join(p, modfile.ModuleFileName)); err == nil {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(p, ".proto") {
			return nil
		}
		rel := strings.TrimPrefix(p, base+"/")
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		imports, err := protoimport.Imports(rel, b)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		protos[rel] = imports
		return nil
	})
	if err != nil {
		return nil, err
	}
	return protos, nil
}

func depsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
