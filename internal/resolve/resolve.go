// Package resolve drives dependency resolution for a resolution root:
// version selection over the workspace's requirement union through the
// fetch-verify pipeline, and the requirement-graph views the dep verbs
// render. The driver composes decisions owned elsewhere — workspace
// membership and locality (internal/module/workspace), selection
// (internal/module/mvs), artifact acquisition and verification
// (internal/source/fetch) — and adds exactly the wiring the specs place
// between them: local paths never reach the fetch layer, selection
// seeds from exactly the declared union, and graph rendering attributes
// root edges to the workspace module that declared them. Every read of
// a build-list pair's content — its module file for selection, its
// artifacts for download — goes through the driver, and the file
// sets' loader (internal/proto/modfiles) reads the same way: each asks
// workspace.Root.Source what answers for the pair, so a replaced path
// is read from its replacement everywhere and fetched nowhere. A dep
// verb never hands a build-list pair to the fetch client itself.
package resolve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/greatliontech/pb/internal/module/mvs"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/module/workspace"
	"github.com/greatliontech/pb/internal/source/fetch"
)

// Driver resolves one workspace's dependency graph. Not safe for
// concurrent use, matching the pipeline beneath it.
type Driver struct {
	Root   *workspace.Root
	Client *fetch.Client
}

// load is the selection loader (REQ-resolve-mvs): the verified module
// file of each reached pair, through the pipeline's pin-and-cache
// discipline, or — for a pair the workspace replaces with a directory
// — that directory's module file as the root loaded it
// (REQ-work-replace-dir). A workspace-local path contributes no
// requirements and is never fetched — the local override is
// unconditional (REQ-work-local-resolution), and every workspace
// module's declarations already ride the root union
// (REQ-work-external-resolution), so fetching a published copy could
// only answer for content the workspace overrides.
func (d *Driver) load(ctx context.Context) mvs.LoadFunc {
	return func(path string, v version.Version) ([]mvs.Requirement, error) {
		if _, local := d.Root.IsLocal(path); local {
			return nil, nil
		}
		src := d.Root.Source(path, v)
		var deps map[string]string
		if src.Module != nil {
			deps = src.Module.File.Deps
		} else {
			mf, err := d.Client.Module(ctx, src.Path, src.Version)
			if err != nil {
				return nil, named(d.Root, path, v, err)
			}
			deps = mf.Deps
		}
		reqs := make([]mvs.Requirement, 0, len(deps))
		for p, ver := range deps {
			parsed, err := version.Parse(ver)
			if err != nil {
				return nil, fmt.Errorf("requirement %s@%s of %s: %w", p, ver, d.Root.Label(path, v), err)
			}
			reqs = append(reqs, mvs.Requirement{Path: p, Version: parsed})
		}
		return reqs, nil
	}
}

// Download fetches, verifies and pins the source of a build-list pair
// into the module cache — the pair itself, or its pinned replacement
// in its place — and returns the source; a directory replacement is
// the working tree's and fetches nothing (REQ-dep-download,
// REQ-work-replace, REQ-work-replace-dir).
func (d *Driver) Download(ctx context.Context, r mvs.Requirement) (workspace.Source, error) {
	src := d.Root.Source(r.Path, r.Version)
	if src.Module != nil {
		return src, nil
	}
	return src, named(d.Root, r.Path, r.Version, d.Client.Download(ctx, src.Path, src.Version))
}

// named prefixes a fetch failure with the build-list pair it was for
// where that pair is replaced: the failure names the source fetched,
// and the prefix says what it stands for.
func named(root *workspace.Root, path string, v version.Version, err error) error {
	if err == nil || !root.Replaced(path) {
		return err
	}
	return fmt.Errorf("%s: %w", root.Label(path, v), err)
}

// BuildList selects one version per reachable non-local module path
// (REQ-resolve-mvs) over the workspace's requirement union
// (REQ-work-external-resolution). Workspace-local paths are excluded
// from the returned list: their override is unconditional, so no
// external version answers for them.
func (d *Driver) BuildList(ctx context.Context) (list []mvs.Requirement, crossings []mvs.Crossing, err error) {
	root, err := d.Root.Requirements()
	if err != nil {
		return nil, nil, err
	}
	list, crossings, err = mvs.BuildList(root, d.load(ctx))
	// A *mvs.CrossingError still carries the list for diagnostics; the
	// local-path filter applies to whatever list came back, preserving
	// that contract through the wrapper.
	local := func(path string) bool {
		_, l := d.Root.IsLocal(path)
		return l
	}
	list = slices.DeleteFunc(list, func(r mvs.Requirement) bool { return local(r.Path) })
	// A major crossing about a workspace-local path is not a crossing:
	// the local override is unconditional ("regardless of what version
	// any requirement declares"), no external version answers for the
	// path, and — locals being excluded from the union — the root could
	// never accept it. Selection must not fail on a disagreement that
	// cannot make the working copy wrong.
	crossings = slices.DeleteFunc(crossings, func(c mvs.Crossing) bool { return local(c.Path) })
	var ce *mvs.CrossingError
	if errors.As(err, &ce) {
		kept := slices.DeleteFunc(slices.Clone(ce.Crossings), func(c mvs.Crossing) bool { return local(c.Path) })
		if len(kept) == 0 {
			err = nil
		} else {
			err = &mvs.CrossingError{Crossings: kept}
		}
	}
	return list, crossings, err
}

// Graph returns the requirement graph's edges (REQ-dep-graph): one
// edge per declaration of each workspace module, the requirer being
// the declaring module's bare path (its local working copy has no
// version), then every edge of the reachable graph the union induces
// (mvs.Graph), requirers as path@version. Sorted lexically by
// requirer, path, version — a pure function of the graph
// (REQ-resolve-determinism).
func (d *Driver) Graph(ctx context.Context) ([]mvs.Edge, error) {
	var edges []mvs.Edge
	for _, m := range d.Root.Modules {
		for p, ver := range m.File.Deps {
			parsed, err := version.Parse(ver)
			if err != nil {
				return nil, fmt.Errorf("workspace module %s: requirement %s@%s: %w", m.File.Module, p, ver, err)
			}
			edges = append(edges, mvs.Edge{Requirer: m.File.Module, Path: p, Version: parsed})
		}
	}
	root, err := d.Root.Requirements()
	if err != nil {
		return nil, err
	}
	inner, err := mvs.Graph(root, d.load(ctx))
	if err != nil {
		return nil, err
	}
	for _, e := range inner {
		if e.Requirer == "" {
			// The union's root edges are the workspace declarations
			// already rendered above, attributed to their declaring
			// modules.
			continue
		}
		edges = append(edges, e)
	}
	// Sorted lexically by the line graph prints (REQ-dep-graph): the
	// whole line, so a path that prefixes a sibling's sorts as its
	// spelling does, not as a field.
	slices.SortFunc(edges, func(a, b mvs.Edge) int {
		return strings.Compare(a.Requirer+" "+a.Path+"@"+a.Version.String(), b.Requirer+" "+b.Path+"@"+b.Version.String())
	})
	// Defensive: no producible duplicate exists — workspace edges are
	// map-derived per module, inner edges expand each pair once, and
	// bare-path requirers cannot collide with path@version ones.
	edges = slices.Compact(edges)
	return edges, nil
}

// Declared projects the graph's edges onto what each module of the
// build declares at the version the build list selects: a workspace
// module's declarations under its bare path, a build-list module's
// under its path from the edges its selected version requires alone
// — the graph carries every visited version's edges, and a module's
// gap is judged against its own declarations, not an older version's
// (dep-verbs.md REQ-dep-tidy). Paths sorted, a pure function of the
// edges and the list.
func Declared(edges []mvs.Edge, list []mvs.Requirement) map[string][]string {
	selected := make(map[string]string, len(list))
	for _, r := range list {
		selected[r.Path] = r.Path + "@" + r.Version.String()
	}
	declared := map[string][]string{}
	for _, e := range edges {
		p, _, versioned := strings.Cut(e.Requirer, "@")
		if versioned && selected[p] != e.Requirer {
			continue
		}
		declared[p] = append(declared[p], e.Path)
	}
	for p := range declared {
		slices.Sort(declared[p])
		declared[p] = slices.Compact(declared[p])
	}
	return declared
}

// Why returns a shortest requirement chain from a workspace module to
// the named path through the requirement graph (REQ-dep-why) — among
// equal-length chains the lexically least — or nil when the path is
// not needed. Chain elements are the graph's node names: the
// workspace module's bare path first, then path@version steps, ending
// at the first node whose path is the target.
func (d *Driver) Why(ctx context.Context, target string) ([]string, error) {
	edges, err := d.Graph(ctx)
	if err != nil {
		return nil, err
	}
	return WhyOver(edges, target), nil
}

// WhyOver answers Why over an already-rendered edge set, so several
// targets share one graph computation.
func WhyOver(edges []mvs.Edge, target string) []string {
	// Adjacency over node names; edges are lexically sorted already, so
	// BFS explores lexically least chains first at equal depth. The
	// sort's version tie-break never decides sibling order: a module
	// file's deps map admits one edge per path per requirer, so path
	// order alone orders same-requirer siblings, and cross-parent ties
	// are decided by earlier chain elements.
	next := map[string][]mvs.Edge{}
	var starts []string
	seenStart := map[string]bool{}
	for _, e := range edges {
		next[e.Requirer] = append(next[e.Requirer], e)
		if !strings.Contains(e.Requirer, "@") && !seenStart[e.Requirer] {
			seenStart[e.Requirer] = true
			starts = append(starts, e.Requirer)
		}
	}
	type state struct {
		node  string
		chain []string
	}
	queue := make([]state, 0, len(starts))
	for _, s := range starts {
		queue = append(queue, state{node: s, chain: []string{s}})
	}
	visited := map[string]bool{}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur.node] {
			continue
		}
		visited[cur.node] = true
		for _, e := range next[cur.node] {
			nodeName := e.Path + "@" + e.Version.String()
			chain := append(slices.Clone(cur.chain), nodeName)
			if e.Path == target {
				return chain
			}
			queue = append(queue, state{node: nodeName, chain: chain})
		}
	}
	return nil
}
