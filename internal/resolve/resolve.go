// Package resolve drives dependency resolution for a resolution root:
// version selection over the workspace's requirement union through the
// fetch-verify pipeline, and the requirement-graph views the dep verbs
// render. The driver composes decisions owned elsewhere — workspace
// membership and locality (internal/workspace), selection
// (internal/mvs), artifact acquisition and verification
// (internal/modfetch) — and adds exactly the wiring the specs place
// between them: local paths never reach the fetch layer, selection
// seeds from exactly the declared union, and graph rendering attributes
// root edges to the workspace module that declared them.
package resolve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/greatliontech/pb/internal/modfetch"
	"github.com/greatliontech/pb/internal/mvs"
	"github.com/greatliontech/pb/internal/version"
	"github.com/greatliontech/pb/internal/workspace"
)

// Driver resolves one workspace's dependency graph. Not safe for
// concurrent use, matching the pipeline beneath it.
type Driver struct {
	Root   *workspace.Root
	Client *modfetch.Client
}

// load is the selection loader (REQ-resolve-mvs): the verified module
// file of each reached pair, through the pipeline's pin-and-cache
// discipline. A workspace-local path contributes no requirements and is
// never fetched — the local override is unconditional
// (REQ-work-local-resolution), and every workspace module's
// declarations already ride the root union
// (REQ-work-external-resolution), so fetching a published copy could
// only answer for content the workspace overrides.
func (d *Driver) load(ctx context.Context) mvs.LoadFunc {
	return func(path string, v version.Version) ([]mvs.Requirement, error) {
		if _, local := d.Root.IsLocal(path); local {
			return nil, nil
		}
		mf, err := d.Client.Module(ctx, path, v)
		if err != nil {
			return nil, err
		}
		reqs := make([]mvs.Requirement, 0, len(mf.Deps))
		for p, ver := range mf.Deps {
			parsed, err := version.Parse(ver)
			if err != nil {
				return nil, fmt.Errorf("requirement %s@%s of %s@%s: %w", p, ver, path, v, err)
			}
			reqs = append(reqs, mvs.Requirement{Path: p, Version: parsed})
		}
		return reqs, nil
	}
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
	slices.SortFunc(edges, func(a, b mvs.Edge) int {
		if c := strings.Compare(a.Requirer, b.Requirer); c != 0 {
			return c
		}
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return version.Compare(a.Version, b.Version)
	})
	// Defensive: no producible duplicate exists — workspace edges are
	// map-derived per module, inner edges expand each pair once, and
	// bare-path requirers cannot collide with path@version ones.
	edges = slices.Compact(edges)
	return edges, nil
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
				return chain, nil
			}
			queue = append(queue, state{node: nodeName, chain: chain})
		}
	}
	return nil, nil
}
