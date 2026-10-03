package dep

import (
	"context"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"

	"github.com/greatliontech/pb/internal/module"

	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/modfile"
	"github.com/greatliontech/pb/internal/proto/importcheck"
	"github.com/greatliontech/pb/internal/proto/modfiles"
	"github.com/greatliontech/pb/internal/resolve"
)

// tidyRounds bounds the tidy fixpoint. Rewriting declarations to the
// selected versions preserves every selection (each declaration moves
// up to a version the graph already selects), so the graph is stable
// after one changed round and later rounds only verify; the bound
// turns a broken oracle into an error instead of a spin.
const tidyRounds = 10

// Tidy makes each workspace module's declared dependencies exactly the
// modules its own protobuf imports are satisfied by, and those the
// imports of every module it reaches are satisfied by where the
// reached module's own declarations do not name the provider — a
// synthesized module declares nothing, so the workspace module
// declaring it carries all its files need; a declaring module that
// under-declares has its gap carried the same way, the consumer's
// declaration the one that can, and out names the module carried for
// — at the versions the tidied graph selects (REQ-dep-tidy): unused
// declarations drop, directly-imported build-list modules gain
// declarations, an import satisfied by nothing fails per
// REQ-resolve-unsatisfied-imports (tidy never invents a dependency),
// workspace-local imports keep their
// existing declared version (and fail when none exists to keep), and
// pins for pairs outside the tidied graph are removed — the module
// pins the requirement graph reaches no more, and the ruleset pins no
// import of the lint file names; the imports themselves tidy leaves
// alone: a ruleset is no protobuf dependency, so no module file
// declares one (REQ-dep-ruleset-declarations). Idempotent: a
// second run changes nothing. Module-file rewrites are per-file
// atomic, not transactional: a failed round may leave some files
// rewritten; rerunning after fixing the cause converges.
func Tidy(ctx context.Context, s *Session, out io.Writer) error {
	imported, err := s.importedRulesets(ctx)
	if err != nil {
		return err
	}
	for round := 0; round < tidyRounds; round++ {
		changed, carried, err := tidyOnce(ctx, s, imported)
		if err != nil {
			return err
		}
		if !changed {
			// The stable round's carries are the report, once: every
			// declaration kept for a reached module's gap, each module
			// it is carried for named, in one order.
			for _, line := range carried {
				fmt.Fprintln(out, line)
			}
			return s.SaveLock()
		}
	}
	return fmt.Errorf("dep tidy: no fixpoint after %d rounds", tidyRounds)
}

func tidyOnce(ctx context.Context, s *Session, imported map[string]bool) (changed bool, carried []string, err error) {
	// The import-relevant view of every module, from the shared file-set
	// loader (modfiles): workspace modules from the working tree,
	// externals from their verified archives.
	list, mods, err := s.Modules(ctx)
	if err != nil {
		return false, nil, err
	}
	selected := map[string]string{}
	for _, r := range list {
		selected[r.Path] = r.Version.String()
	}
	// A malformed file is named by a path the user can find: a file of
	// the working tree — a workspace module's or a directory
	// replacement's — by its place in the tree, a fetched one by module
	// and version.
	views, err := importcheck.Views(mods, func(m modfiles.Module, p string) string {
		if m.FromTree() {
			return path.Join(s.Root.Dir, m.Dir, p)
		}
		return fmt.Sprintf("%s@%s: %s", m.Path, m.Version, p)
	})
	if err != nil {
		return false, nil, err
	}
	// What each module declares at its selected version, from the
	// requirement graph's edges: the one source of a module's
	// declarations, a synthesized module's empty.
	edges, err := s.Driver.Graph(ctx)
	if err != nil {
		return false, nil, err
	}
	declared := resolve.Declared(edges, list)
	declares := make(map[string]map[string]bool, len(declared))
	for p, ps := range declared {
		declares[p] = make(map[string]bool, len(ps))
		for _, q := range ps {
			declares[p][q] = true
		}
	}
	viewOf := make(map[string]*importcheck.Module, len(views))
	for i := range views {
		viewOf[views[i].Path] = &views[i]
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
		return false, nil, err
	}

	// Rewrite each workspace module's declarations to exactly its
	// direct imports.
	carries := map[string]bool{}
	for _, m := range s.Root.Modules {
		view := viewOf[m.File.Module]
		// The module's own imports' providers, and — walking every
		// module it reaches, by declaration or by carry — the providers
		// of what a reached module's files import beyond its own
		// declarations: a synthesized module declares nothing, so all
		// its files need is carried here; a declaring module that
		// under-declares has its gap carried the same way, the one place
		// a declaration can live, and the report names it.
		want := map[string]string{}
		var walk []string
		visited := map[string]bool{}
		reach := func(p string) {
			if !visited[p] {
				visited[p] = true
				walk = append(walk, p)
			}
		}
		require := func(from string, imports []string) error {
			for _, imp := range imports {
				if modfiles.WellKnown(imp) {
					continue
				}
				p, ok := provider[imp]
				if !ok || p == m.File.Module || p == from {
					continue
				}
				if from != m.File.Module && declares[from][p] {
					// A reached module's own declaration is the edge; the
					// workspace module's own imports are always its own
					// to declare.
					reach(p)
					continue
				}
				if from != m.File.Module {
					carries[fmt.Sprintf("%s carries %s for %s, whose files import it undeclared", m.File.Module, p, label(from, selected))] = true
				}
				if _, seen := want[p]; seen {
					continue
				}
				if _, isLocal := s.Root.IsLocal(p); isLocal {
					v, declared := m.File.Deps[p]
					if !declared {
						if from != m.File.Module {
							return fmt.Errorf("dep tidy: %s needs %s, whose files import workspace module %s, but declares no version for it — tidy cannot invent one; add the dependency with the version consumers should require", m.File.Module, from, p)
						}
						return fmt.Errorf("dep tidy: %s imports workspace module %s but declares no version for it — tidy cannot invent one; add the dependency with the version consumers should require", m.File.Module, p)
					}
					want[p] = v
				} else {
					want[p] = selected[p]
				}
				reach(p)
			}
			return nil
		}
		// Files in sorted order: the walk, and so the report, a
		// function of the file set alone.
		for _, f := range slices.Sorted(maps.Keys(view.Files)) {
			if err := require(m.File.Module, view.Files[f]); err != nil {
				return false, nil, err
			}
		}
		for len(walk) > 0 {
			p := walk[0]
			walk = walk[1:]
			if _, isLocal := s.Root.IsLocal(p); isLocal {
				continue // a workspace module's declarations are its own to tidy
			}
			for _, q := range declared[p] {
				reach(q)
			}
			if v := viewOf[p]; v != nil {
				for _, f := range slices.Sorted(maps.Keys(v.Files)) {
					if err := require(p, v.Files[f]); err != nil {
						return false, nil, err
					}
				}
			}
		}
		if depsEqual(m.File.Deps, want) {
			continue
		}
		changed = true
		if len(want) == 0 {
			// No declaration left: the file declares none, which the
			// module file spells by no deps key, never an empty mapping.
			want = nil
		}
		nf := &modfile.File{Module: m.File.Module, Deps: want}
		b, err := modfile.Encode(nf)
		if err != nil {
			return false, nil, err
		}
		if err := writeFile(s.WS, path.Join(s.Root.Dir, m.Dir, module.ModuleFileName), b); err != nil {
			return false, nil, err
		}
		m.File.Deps = want
	}
	carried = slices.Sorted(maps.Keys(carries))
	if changed {
		return true, nil, nil
	}

	// Stable: prune pins outside the tidied requirement graph.
	reachable := map[string]bool{}
	for _, e := range edges {
		// A replaced pair's pin is its replacement's: the pair the
		// build reads through it, never the replaced pair, which is
		// never fetched (REQ-work-replace); a directory replacement
		// has none (REQ-work-replace-dir), its spelling matching no
		// pin key.
		reachable[s.Root.Source(e.Path, e.Version).String()] = true
	}
	kept := slices.DeleteFunc(slices.Clone(s.Lock.Modules), func(p lockfile.ModulePin) bool {
		return !reachable[p.Path+"@"+p.Version]
	})
	if len(kept) != len(s.Lock.Modules) {
		s.Lock.Modules = kept
	}
	// A ruleset pin stays while an import names its pair — the pair
	// read through the replacements, as the pin was made.
	keptRulesets := slices.DeleteFunc(slices.Clone(s.Lock.Rulesets), func(p lockfile.ModulePin) bool {
		return !imported[p.Path+"@"+p.Version]
	})
	if len(keptRulesets) != len(s.Lock.Rulesets) {
		s.Lock.Rulesets = keptRulesets
	}
	return false, carried, nil
}

// importedRulesets is the pairs the imports pin, the lint file's
// through the rule files' own: each fetched pair through the
// workspace's replacements, a working-tree import's none.
func (s *Session) importedRulesets(ctx context.Context) (map[string]bool, error) {
	loaded, err := s.closure(ctx)
	if err != nil {
		return nil, err
	}
	pairs := map[string]bool{}
	for _, src := range loaded.Fetched {
		pairs[src.String()] = true
	}
	return pairs, nil
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

// label names a build-list module with its selected version in the
// tidy's report.
func label(p string, selected map[string]string) string {
	if v, ok := selected[p]; ok {
		return p + "@" + v
	}
	return p
}
