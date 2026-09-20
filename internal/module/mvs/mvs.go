// Package mvs computes minimal version selection over a requirement graph
// (REQ-resolve-mvs): the selected version of each module path is the
// maximum version required for that path across the resolution root and
// every (path, version) pair reached transitively through requirements.
// Superseded pairs stay in the graph deliberately: sourcing requirements
// only from selected versions has no consistent solution on some graphs
// (root→B@v1.2, B@v1.2→C, C→B@v1.8, B@v1.8→{} leaves B satisfiable at
// neither v1.2 nor v1.8 once C's membership depends on which B is
// selected), while the reachable-graph form is total, unique, and
// order-independent.
//
// The loader is injected, so the build list is a pure function of the
// root requirements and the loaded module files (REQ-resolve-determinism):
// no fetching, caching, or ordering concern lives here.
package mvs

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/greatliontech/pb/internal/module/version"
)

// ErrMajorCrossing is wrapped when selection raises a module's major
// version above a requirement without the resolution root requiring the
// selected major (REQ-resolve-major-crossing).
var ErrMajorCrossing = errors.New("major version crossing")

// Requirement is one declared dependency: a module path at a minimum
// version.
type Requirement struct {
	Path    string
	Version version.Version
}

// LoadFunc returns the requirements a module declares at a version — read
// from its pinned module file. It must be a pure lookup: BuildList's
// determinism is exactly the loader's.
type LoadFunc func(path string, v version.Version) ([]Requirement, error)

// Edge is one requirement with its requirer: the empty Requirer is the
// resolution root, otherwise "path@version" of the requiring module.
type Edge struct {
	Requirer string
	Path     string
	Version  version.Version
}

// Crossing reports a module selected above the major version some
// requirement declared (REQ-resolve-major-crossing). Forcing is a
// requirement at the selected version; Crossed is the lowest requirement
// below the selected major. RootAccepted reports whether the resolution
// root itself requires the selected major — an accepted crossing is a
// warning, an unaccepted one fails the build list.
type Crossing struct {
	Path         string
	Selected     version.Version
	Forcing      Edge
	Crossed      Edge
	RootAccepted bool
}

// CrossingError is the failure carrying every unaccepted crossing.
type CrossingError struct {
	Crossings []Crossing
}

func (e *CrossingError) Error() string {
	var b strings.Builder
	for i, c := range e.Crossings {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b,
			"selecting %s %s (required by %s) crosses major above %s required by %s, and the root does not require %s at v%s — record the selected major in the root's requirements to accept it",
			c.Path, c.Selected, requirerName(c.Forcing.Requirer),
			c.Crossed.Version, requirerName(c.Crossed.Requirer),
			c.Path, c.Selected.Major())
	}
	return b.String()
}

func (e *CrossingError) Unwrap() error { return ErrMajorCrossing }

func requirerName(r string) string {
	if r == "" {
		return "the root"
	}
	return r
}

// Graph walks the reachable requirement graph (REQ-resolve-mvs's node
// set) and returns every edge in traversal order: the root's
// requirements first, then each reached pair's declarations, a pair's
// requirements expanded once however many edges reach it. BuildList
// selects over exactly this edge set — one definition of reachability
// serves selection and the graph-rendering consumers above it.
func Graph(root []Requirement, load LoadFunc) ([]Edge, error) {
	type node struct {
		path string
		ver  string
	}
	var edges []Edge
	visited := make(map[node]bool)
	queue := make([]Edge, 0, len(root))
	for _, r := range sortedReqs(root) {
		queue = append(queue, Edge{Requirer: "", Path: r.Path, Version: r.Version})
	}
	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]
		edges = append(edges, e)
		n := node{e.Path, e.Version.String()}
		if visited[n] {
			continue
		}
		visited[n] = true
		reqs, err := load(e.Path, e.Version)
		if err != nil {
			return nil, fmt.Errorf("loading requirements of %s@%s (required by %s): %w",
				e.Path, e.Version, requirerName(e.Requirer), err)
		}
		requirer := e.Path + "@" + e.Version.String()
		for _, r := range sortedReqs(reqs) {
			queue = append(queue, Edge{Requirer: requirer, Path: r.Path, Version: r.Version})
		}
	}
	return edges, nil
}

// BuildList selects one version per reachable module path. The returned
// list is sorted by path; crossings holds every major crossing, accepted
// (warnings for the caller to report) and, when any crossing is
// unaccepted, the error is a *CrossingError naming them all — the list is
// still returned for diagnostics.
func BuildList(root []Requirement, load LoadFunc) (list []Requirement, crossings []Crossing, err error) {
	edges, err := Graph(root, load)
	if err != nil {
		return nil, nil, err
	}

	selected := make(map[string]version.Version)
	for _, e := range edges {
		if cur, ok := selected[e.Path]; !ok || version.Compare(e.Version, cur) > 0 {
			selected[e.Path] = e.Version
		}
	}
	for path, v := range selected {
		list = append(list, Requirement{Path: path, Version: v})
	}
	slices.SortFunc(list, func(a, b Requirement) int { return strings.Compare(a.Path, b.Path) })

	crossings = findCrossings(edges, selected)
	var unaccepted []Crossing
	for _, c := range crossings {
		if !c.RootAccepted {
			unaccepted = append(unaccepted, c)
		}
	}
	if len(unaccepted) > 0 {
		return list, crossings, &CrossingError{Crossings: unaccepted}
	}
	return list, crossings, nil
}

// findCrossings walks the recorded edges once per path: a crossing exists
// when some requirement's major sits below the selected major. Forcing is
// the first edge at the selected version, Crossed the lowest requirement
// below the selected major (ties on version broken by requirer name) —
// both deterministic because the edge order is.
func findCrossings(edges []Edge, selected map[string]version.Version) []Crossing {
	byPath := make(map[string][]Edge)
	for _, e := range edges {
		byPath[e.Path] = append(byPath[e.Path], e)
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	slices.Sort(paths)

	var out []Crossing
	for _, p := range paths {
		sel := selected[p]
		var forcing, crossed *Edge
		rootAccepted := false
		for i := range byPath[p] {
			e := &byPath[p][i]
			if e.Requirer == "" && version.CompareMajor(e.Version, sel) == 0 {
				rootAccepted = true
			}
			if forcing == nil && version.Compare(e.Version, sel) == 0 {
				forcing = e
			}
			if version.CompareMajor(e.Version, sel) < 0 {
				if crossed == nil ||
					version.Compare(e.Version, crossed.Version) < 0 ||
					(version.Compare(e.Version, crossed.Version) == 0 && e.Requirer < crossed.Requirer) {
					crossed = e
				}
			}
		}
		if crossed != nil {
			out = append(out, Crossing{
				Path: p, Selected: sel,
				Forcing: *forcing, Crossed: *crossed,
				RootAccepted: rootAccepted,
			})
		}
	}
	return out
}

func sortedReqs(reqs []Requirement) []Requirement {
	out := slices.Clone(reqs)
	slices.SortFunc(out, func(a, b Requirement) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return version.Compare(a.Version, b.Version)
	})
	return out
}
