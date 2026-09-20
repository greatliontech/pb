package dep

import (
	"context"
	"fmt"
	"path"
	"slices"

	"github.com/greatliontech/pb/internal/module"

	"github.com/go-git/go-billy/v6/helper/iofs"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/modfile"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/proto/importcheck"
	"github.com/greatliontech/pb/internal/proto/modfiles"
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

	// The import-relevant view of every module, from the shared file-set
	// loader (modfiles): workspace modules from the working tree,
	// externals from their verified archives.
	mods, err := modfiles.Load(ctx, iofs.New(s.WS), s.Root, list, func(ctx context.Context, modPath string, v version.Version) ([]byte, error) {
		return s.Client.Zip(ctx, modPath, v)
	})
	if err != nil {
		return false, err
	}
	// A malformed file is named by a path the user can find: a workspace
	// file by its place in the tree, an external by module and version.
	views, err := importcheck.Views(mods, func(m modfiles.Module, p string) string {
		if m.Local {
			return path.Join(s.Root.Dir, m.Dir, p)
		}
		return fmt.Sprintf("%s@%s: %s", m.Path, m.Version, p)
	})
	if err != nil {
		return false, err
	}
	provider := map[string]string{} // proto file -> module path
	for _, v := range views {
		for f := range v.Files {
			// First provider wins, deterministically: views are added in
			// deterministic order (workspace use order, then build-list
			// order), and within a view every file maps to the same
			// module path, so map-iteration order cannot change the
			// assignment. Cross-module duplicates are not tidy's to
			// adjudicate — any provider serves for attribution, and
			// generation owns the conflict.
			if _, ok := provider[f]; !ok {
				provider[f] = v.Path
			}
		}
	}

	// Satisfaction over the whole set (REQ-resolve-unsatisfied-imports):
	// tidy never invents a module path for an unsatisfied import.
	if err := importcheck.Check(views); err != nil {
		return false, err
	}

	// Rewrite each workspace module's declarations to exactly its
	// direct imports.
	for _, m := range s.Root.Modules {
		var view *importcheck.Module
		for i := range views {
			if views[i].Path == m.File.Module {
				view = &views[i]
				break
			}
		}
		want := map[string]string{}
		for _, imports := range view.Files {
			for _, imp := range imports {
				if importcheck.WellKnown(imp) {
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
		if err := writeFile(s.WS, path.Join(s.Root.Dir, m.Dir, module.ModuleFileName), b); err != nil {
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
