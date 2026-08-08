package mvs

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/greatliontech/pb/internal/version"
)

func v(t testing.TB, s string) version.Version {
	t.Helper()
	ver, err := version.Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return ver
}

func req(t testing.TB, path, ver string) Requirement {
	return Requirement{Path: path, Version: v(t, ver)}
}

// graph maps "path@version" to declared requirements.
type graph map[string][]Requirement

func (g graph) load(path string, ver version.Version) ([]Requirement, error) {
	return g[path+"@"+ver.String()], nil
}

func names(list []Requirement) string {
	parts := make([]string, len(list))
	for i, r := range list {
		parts[i] = r.Path + "@" + r.Version.String()
	}
	return strings.Join(parts, " ")
}

func TestBuildListGolden(t *testing.T) {
	g := graph{
		"a.io/a@v1.0.0": {req(t, "a.io/b", "v1.2.0"), req(t, "a.io/c", "v1.0.0")},
		"a.io/b@v1.2.0": {req(t, "a.io/c", "v1.1.0")},
		"a.io/c@v1.0.0": nil,
		"a.io/c@v1.1.0": nil,
	}
	list, crossings, err := BuildList([]Requirement{req(t, "a.io/a", "v1.0.0")}, g.load)
	if err != nil || len(crossings) != 0 {
		t.Fatalf("err=%v crossings=%v", err, crossings)
	}
	if got, want := names(list), "a.io/a@v1.0.0 a.io/b@v1.2.0 a.io/c@v1.1.0"; got != want {
		t.Fatalf("list = %s, want %s", got, want)
	}
}

// Superseded pairs stay requirement sources: the graph from the package
// doc, where selected-only sourcing has no consistent answer.
func TestSupersededVersionsKeepRequirements(t *testing.T) {
	g := graph{
		"a.io/b@v1.2.0": {req(t, "a.io/c", "v1.9.0")},
		"a.io/b@v1.8.0": nil,
		"a.io/c@v1.9.0": {req(t, "a.io/b", "v1.8.0")},
	}
	list, _, err := BuildList([]Requirement{req(t, "a.io/b", "v1.2.0"), req(t, "a.io/c", "v1.9.0")}, g.load)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(list), "a.io/b@v1.8.0 a.io/c@v1.9.0"; got != want {
		t.Fatalf("list = %s, want %s", got, want)
	}
}

func TestRequirementCycle(t *testing.T) {
	g := graph{
		"a.io/a@v1.0.0": {req(t, "a.io/b", "v1.0.0")},
		"a.io/b@v1.0.0": {req(t, "a.io/a", "v1.0.0")},
	}
	list, _, err := BuildList([]Requirement{req(t, "a.io/a", "v1.0.0")}, g.load)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(list), "a.io/a@v1.0.0 a.io/b@v1.0.0"; got != want {
		t.Fatalf("list = %s, want %s", got, want)
	}
}

func TestLoadErrorContext(t *testing.T) {
	boom := errors.New("boom")
	load := func(path string, ver version.Version) ([]Requirement, error) {
		if path == "a.io/b" {
			return nil, boom
		}
		return []Requirement{req(t, "a.io/b", "v1.0.0")}, nil
	}
	_, _, err := BuildList([]Requirement{req(t, "a.io/a", "v1.0.0")}, load)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped boom", err)
	}
	for _, part := range []string{"a.io/b@v1.0.0", "required by a.io/a@v1.0.0"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("err %q does not name %q", err, part)
		}
	}
}

// A transitive requirement dragging a module across a major the root never
// asked for fails; recording the major in the root accepts it as a
// warning (REQ-resolve-major-crossing).
func TestMajorCrossing(t *testing.T) {
	g := graph{
		"a.io/a@v1.0.0": {req(t, "a.io/m", "v1.2.0")},
		"a.io/b@v2.1.0": {req(t, "a.io/m", "v3.0.0")},
		"a.io/m@v1.2.0": nil,
		"a.io/m@v3.0.0": nil,
	}
	root := []Requirement{req(t, "a.io/a", "v1.0.0"), req(t, "a.io/b", "v2.1.0")}

	list, crossings, err := BuildList(root, g.load)
	if !errors.Is(err, ErrMajorCrossing) {
		t.Fatalf("err = %v, want ErrMajorCrossing", err)
	}
	if list == nil {
		t.Fatal("crossing error must still return the list for diagnostics")
	}
	for _, part := range []string{
		"a.io/m v3.0.0", "required by a.io/b@v2.1.0",
		"v1.2.0 required by a.io/a@v1.0.0",
		"root does not require a.io/m at v3",
		"record the selected major",
	} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q does not state %q", err, part)
		}
	}
	if len(crossings) != 1 || crossings[0].RootAccepted {
		t.Fatalf("crossings = %+v, want one unaccepted", crossings)
	}

	// The root recording the selected major turns the failure into a
	// warning-carrying success.
	accepted := append(slicesClone(root), req(t, "a.io/m", "v3.0.0"))
	list, crossings, err = BuildList(accepted, g.load)
	if err != nil {
		t.Fatalf("accepted crossing: %v", err)
	}
	if got, want := names(list), "a.io/a@v1.0.0 a.io/b@v2.1.0 a.io/m@v3.0.0"; got != want {
		t.Fatalf("list = %s, want %s", got, want)
	}
	if len(crossings) != 1 || !crossings[0].RootAccepted {
		t.Fatalf("crossings = %+v, want one accepted", crossings)
	}
	if c := crossings[0]; c.Path != "a.io/m" || c.Selected.String() != "v3.0.0" ||
		c.Crossed.Version.String() != "v1.2.0" || c.Crossed.Requirer != "a.io/a@v1.0.0" {
		t.Fatalf("crossing detail = %+v", c)
	}
}

// The crossing report's exact shape under contention: Forcing is a
// requirement at the selected version even when later edges exist, Crossed
// is the lowest below-major requirement with ties broken by requirer (the
// root sorting first), a root requirement at the WRONG major does not
// accept, and multiple crossings join the failure with "; ".
func TestCrossingReportDetail(t *testing.T) {
	g := graph{
		"x.io/b@v1.0.0": {req(t, "x.io/m", "v3.0.0"), req(t, "x.io/n", "v2.0.0")},
		"x.io/c@v1.0.0": {req(t, "x.io/m", "v1.0.0"), req(t, "x.io/n", "v1.0.0")},
		"x.io/d@v1.0.0": {req(t, "x.io/m", "v1.0.0")},
		"x.io/m@v1.0.0": nil, "x.io/m@v1.2.0": nil, "x.io/m@v3.0.0": nil,
		"x.io/n@v1.0.0": nil, "x.io/n@v2.0.0": nil,
	}
	root := []Requirement{
		req(t, "x.io/b", "v1.0.0"), req(t, "x.io/c", "v1.0.0"), req(t, "x.io/d", "v1.0.0"),
		req(t, "x.io/m", "v1.2.0"), // below selected major: does not accept
		req(t, "x.io/n", "v1.0.0"), // ties with c's n requirement: root wins
	}
	list, crossings, err := BuildList(root, g.load)
	if !errors.Is(err, ErrMajorCrossing) {
		t.Fatalf("err = %v", err)
	}
	if got, want := names(list), "x.io/b@v1.0.0 x.io/c@v1.0.0 x.io/d@v1.0.0 x.io/m@v3.0.0 x.io/n@v2.0.0"; got != want {
		t.Fatalf("list = %s, want %s", got, want)
	}
	if len(crossings) != 2 {
		t.Fatalf("crossings = %+v", crossings)
	}
	m, n := crossings[0], crossings[1]
	if m.Path != "x.io/m" || m.RootAccepted ||
		m.Forcing.Requirer != "x.io/b@v1.0.0" || m.Forcing.Version.String() != "v3.0.0" ||
		m.Crossed.Requirer != "x.io/c@v1.0.0" || m.Crossed.Version.String() != "v1.0.0" {
		t.Errorf("m crossing = %+v", m)
	}
	if n.Path != "x.io/n" || n.RootAccepted ||
		n.Forcing.Requirer != "x.io/b@v1.0.0" ||
		n.Crossed.Requirer != "" || n.Crossed.Version.String() != "v1.0.0" {
		t.Errorf("n crossing = %+v", n)
	}
	text := err.Error()
	for _, part := range []string{"; ", "required by the root", "required by x.io/c@v1.0.0", "at v3", "at v2"} {
		if !strings.Contains(text, part) {
			t.Errorf("error %q does not contain %q", text, part)
		}
	}
	// The separator joins entries; it never leads.
	if !strings.HasPrefix(text, "selecting ") {
		t.Errorf("error %q does not start with its first entry", text)
	}
}

// The lowest crossed requirement wins even when a later BFS edge carries a
// higher version from a lexically smaller requirer: level-2 requirers are
// not globally sorted, so the tie-break must fire only on version equality.
func TestCrossedNotStolenByLaterRequirer(t *testing.T) {
	g := graph{
		"x.io/b@v1.0.0": {req(t, "x.io/m", "v3.0.0")},
		"x.io/z@v1.0.0": {req(t, "x.io/a", "v1.0.0"), req(t, "x.io/m", "v1.0.0")},
		"x.io/a@v1.0.0": {req(t, "x.io/m", "v1.5.0")},
		"x.io/m@v1.0.0": nil, "x.io/m@v1.5.0": nil, "x.io/m@v3.0.0": nil,
	}
	root := []Requirement{req(t, "x.io/b", "v1.0.0"), req(t, "x.io/z", "v1.0.0")}
	_, crossings, err := BuildList(root, g.load)
	if !errors.Is(err, ErrMajorCrossing) {
		t.Fatalf("err = %v", err)
	}
	if len(crossings) != 1 {
		t.Fatalf("crossings = %+v", crossings)
	}
	c := crossings[0]
	if c.Crossed.Version.String() != "v1.0.0" || c.Crossed.Requirer != "x.io/z@v1.0.0" {
		t.Fatalf("crossed = %+v, want v1.0.0 from x.io/z@v1.0.0", c.Crossed)
	}
}

// On a version tie the lexically smaller requirer wins even when its edge
// arrives later in the walk (a level-2 module can sort below a level-1
// one).
func TestCrossedTieTakesSmallerRequirer(t *testing.T) {
	g := graph{
		"x.io/b@v1.0.0": {req(t, "x.io/m", "v3.0.0")},
		"x.io/z@v1.0.0": {req(t, "x.io/a", "v1.0.0"), req(t, "x.io/m", "v1.0.0")},
		"x.io/a@v1.0.0": {req(t, "x.io/m", "v1.0.0")},
		"x.io/m@v1.0.0": nil, "x.io/m@v3.0.0": nil,
	}
	root := []Requirement{req(t, "x.io/b", "v1.0.0"), req(t, "x.io/z", "v1.0.0")}
	_, crossings, err := BuildList(root, g.load)
	if !errors.Is(err, ErrMajorCrossing) {
		t.Fatalf("err = %v", err)
	}
	if len(crossings) != 1 || crossings[0].Crossed.Requirer != "x.io/a@v1.0.0" {
		t.Fatalf("crossed = %+v, want the tie broken toward x.io/a@v1.0.0", crossings)
	}
}

// sortedReqs orders by path then version and never mutates its input.
func TestSortedReqs(t *testing.T) {
	// Path order contradicts version order, so a comparator that drops
	// either key produces a different result.
	in := []Requirement{
		req(t, "x.io/b", "v1.0.0"),
		req(t, "x.io/a", "v9.0.0"),
		req(t, "x.io/a", "v2.0.0"),
	}
	got := sortedReqs(in)
	if names(got) != "x.io/a@v2.0.0 x.io/a@v9.0.0 x.io/b@v1.0.0" {
		t.Errorf("sortedReqs = %s", names(got))
	}
	if names(in) != "x.io/b@v1.0.0 x.io/a@v9.0.0 x.io/a@v2.0.0" {
		t.Errorf("sortedReqs mutated its input: %s", names(in))
	}
}

// Raising v0 to v1 is a major crossing like any other, including when the
// crossed requirement is a v0 pseudo-version.
func TestZeroMajorCrossing(t *testing.T) {
	g := graph{
		"a.io/a@v1.0.0": {req(t, "a.io/m", "v0.0.0-20260101000000-abcdef123456")},
		"a.io/b@v1.0.0": {req(t, "a.io/m", "v1.0.0")},
		"a.io/m@v0.0.0-20260101000000-abcdef123456": nil,
		"a.io/m@v1.0.0": nil,
	}
	root := []Requirement{req(t, "a.io/a", "v1.0.0"), req(t, "a.io/b", "v1.0.0")}
	_, crossings, err := BuildList(root, g.load)
	if !errors.Is(err, ErrMajorCrossing) {
		t.Fatalf("v0 to v1 crossing must fail unaccepted, got %v", err)
	}
	if len(crossings) != 1 || crossings[0].Crossed.Version.String() != "v0.0.0-20260101000000-abcdef123456" {
		t.Fatalf("crossings = %+v", crossings)
	}
	accepted := append(slicesClone(root), req(t, "a.io/m", "v1.0.0"))
	_, crossings, err = BuildList(accepted, g.load)
	if err != nil {
		t.Fatalf("root-accepted v0 to v1 crossing: %v", err)
	}
	// Acceptance keeps the crossing reported as a warning, never silent.
	if len(crossings) != 1 || !crossings[0].RootAccepted ||
		crossings[0].Crossed.Version.String() != "v0.0.0-20260101000000-abcdef123456" {
		t.Fatalf("accepted crossing not reported: %+v", crossings)
	}
}

// A root requirement at a lower version of the selected major still
// accepts: acceptance is major-level, matching the pb.yaml gesture.
func TestCrossingAcceptanceIsMajorLevel(t *testing.T) {
	g := graph{
		"a.io/a@v1.0.0": {req(t, "a.io/m", "v1.2.0")},
		"a.io/b@v2.1.0": {req(t, "a.io/m", "v3.1.0")},
		"a.io/m@v1.2.0": nil,
		"a.io/m@v3.0.0": nil,
		"a.io/m@v3.1.0": nil,
	}
	root := []Requirement{
		req(t, "a.io/a", "v1.0.0"),
		req(t, "a.io/b", "v2.1.0"),
		req(t, "a.io/m", "v3.0.0"), // lower than selected v3.1.0, same major
	}
	_, crossings, err := BuildList(root, g.load)
	if err != nil {
		t.Fatalf("major-level acceptance: %v", err)
	}
	if len(crossings) != 1 || !crossings[0].RootAccepted {
		t.Fatalf("crossings = %+v", crossings)
	}
}

func slicesClone(rs []Requirement) []Requirement {
	out := make([]Requirement, len(rs))
	copy(out, rs)
	return out
}

// genUniverse builds a random requirement universe and a root over it.
func genUniverse(t *rapid.T) (graph, []Requirement) {
	paths := []string{"x.io/a", "x.io/b", "x.io/c", "x.io/d"}
	vers := []string{"v0.0.0-20260101000000-abcdef123456", "v0.1.0", "v1.0.0", "v1.1.0", "v2.0.0", "v3.0.0"}
	g := graph{}
	for _, p := range paths {
		for _, ver := range vers {
			n := rapid.IntRange(0, 2).Draw(t, "n"+p+ver)
			var reqs []Requirement
			for i := range n {
				rp := rapid.SampledFrom(paths).Draw(t, fmt.Sprintf("rp%s%s%d", p, ver, i))
				rv := rapid.SampledFrom(vers).Draw(t, fmt.Sprintf("rv%s%s%d", p, ver, i))
				reqs = append(reqs, Requirement{Path: rp, Version: mustV(t, rv)})
			}
			g[p+"@"+ver] = reqs
		}
	}
	rootN := rapid.IntRange(1, 4).Draw(t, "rootN")
	var root []Requirement
	for i := range rootN {
		rp := rapid.SampledFrom(paths).Draw(t, fmt.Sprint("rootp", i))
		rv := rapid.SampledFrom(vers).Draw(t, fmt.Sprint("rootv", i))
		root = append(root, Requirement{Path: rp, Version: mustV(t, rv)})
	}
	return g, root
}

func mustV(t *rapid.T, s string) version.Version {
	ver, err := version.Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return ver
}

// The two REQ-resolve-mvs clauses, checked directly: every requirement in
// the reachable graph is satisfied (never older), and every selection is
// some requirement (never newer).
func TestSelectionSoundAndMinimalProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		g, root := genUniverse(t)
		list, _, err := BuildList(root, g.load)
		if err != nil && !errors.Is(err, ErrMajorCrossing) {
			t.Fatal(err)
		}
		selected := map[string]version.Version{}
		for _, r := range list {
			selected[r.Path] = r.Version
		}
		// Recompute the reachable edge set independently.
		type node struct{ p, v string }
		seen := map[node]bool{}
		var walk func(reqs []Requirement)
		required := map[string][]version.Version{}
		walk = func(reqs []Requirement) {
			for _, r := range reqs {
				required[r.Path] = append(required[r.Path], r.Version)
				n := node{r.Path, r.Version.String()}
				if seen[n] {
					continue
				}
				seen[n] = true
				walk(g[r.Path+"@"+r.Version.String()])
			}
		}
		walk(root)
		for path, versions := range required {
			sel, ok := selected[path]
			if !ok {
				t.Fatalf("required path %s missing from the list", path)
			}
			isSome := false
			for _, rv := range versions {
				if version.Compare(sel, rv) < 0 {
					t.Fatalf("%s selected %s below requirement %s", path, sel, rv)
				}
				if version.Compare(sel, rv) == 0 {
					isSome = true
				}
			}
			if !isSome {
				t.Fatalf("%s selected %s which no requirement names", path, sel)
			}
		}
		if len(selected) != len(required) {
			t.Fatalf("list has %d paths, reachable graph requires %d", len(selected), len(required))
		}
	})
}

// The build list is independent of requirement order (REQ-resolve-
// determinism): shuffling the root and every declared requirement list
// changes nothing, including the crossing report and the error text.
func TestDeterminismProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		g, root := genUniverse(t)
		list1, cross1, err1 := BuildList(root, g.load)

		rng := rand.New(rand.NewSource(int64(rapid.IntRange(0, 1<<30).Draw(t, "seed"))))
		shuffledRoot := slicesClone(root)
		rng.Shuffle(len(shuffledRoot), func(i, j int) {
			shuffledRoot[i], shuffledRoot[j] = shuffledRoot[j], shuffledRoot[i]
		})
		g2 := graph{}
		for k, reqs := range g {
			c := slicesClone(reqs)
			rng.Shuffle(len(c), func(i, j int) { c[i], c[j] = c[j], c[i] })
			g2[k] = c
		}
		list2, cross2, err2 := BuildList(shuffledRoot, g2.load)

		if names(list1) != names(list2) {
			t.Fatalf("list depends on input order:\n%s\n%s", names(list1), names(list2))
		}
		if fmt.Sprintf("%+v", cross1) != fmt.Sprintf("%+v", cross2) {
			t.Fatalf("crossings depend on input order:\n%+v\n%+v", cross1, cross2)
		}
		e1, e2 := fmt.Sprint(err1), fmt.Sprint(err2)
		if e1 != e2 {
			t.Fatalf("error depends on input order:\n%s\n%s", e1, e2)
		}
	})
}

// Adding a root requirement never lowers any selection (MVS monotonicity).
func TestMonotonicityProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		g, root := genUniverse(t)
		list1, _, err1 := BuildList(root, g.load)
		if err1 != nil && !errors.Is(err1, ErrMajorCrossing) {
			t.Fatal(err1)
		}
		extraPath := rapid.SampledFrom([]string{"x.io/a", "x.io/b", "x.io/c", "x.io/d"}).Draw(t, "extraP")
		extraVer := rapid.SampledFrom([]string{"v1.0.0", "v1.1.0", "v2.0.0", "v3.0.0"}).Draw(t, "extraV")
		list2, _, err2 := BuildList(append(slicesClone(root), Requirement{Path: extraPath, Version: mustV(t, extraVer)}), g.load)
		if err2 != nil && !errors.Is(err2, ErrMajorCrossing) {
			t.Fatal(err2)
		}
		sel2 := map[string]version.Version{}
		for _, r := range list2 {
			sel2[r.Path] = r.Version
		}
		for _, r := range list1 {
			after, ok := sel2[r.Path]
			if !ok {
				t.Fatalf("%s left the list after adding a requirement", r.Path)
			}
			if version.Compare(after, r.Version) < 0 {
				t.Fatalf("%s lowered from %s to %s by adding a requirement", r.Path, r.Version, after)
			}
		}
	})
}
