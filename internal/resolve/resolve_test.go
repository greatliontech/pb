package resolve

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/greatliontech/pb/internal/lockfile"
	"github.com/greatliontech/pb/internal/modfetch"
	"github.com/greatliontech/pb/internal/modfetchtest"
	"github.com/greatliontech/pb/internal/modfile"
	"github.com/greatliontech/pb/internal/mvs"
	"github.com/greatliontech/pb/internal/workspace"
	"pgregory.net/rapid"
)

var ctx = context.Background()

// driverFixture composes a workspace filesystem with the shared client
// fixture: the workspace declares the root union, the fixture serves
// the external modules.
type driverFixture struct {
	*modfetchtest.Fixture
	fsys fstest.MapFS
}

func newDriver(t *testing.T, files map[string]string) (*Driver, *driverFixture) {
	t.Helper()
	fx := &driverFixture{Fixture: modfetchtest.New(t), fsys: fstest.MapFS{}}
	for p, body := range files {
		fx.fsys[p] = &fstest.MapFile{Data: []byte(body)}
	}
	root, err := workspace.LoadFor(fx.fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	return &Driver{Root: root, Client: fx.client("proxy")}, fx
}

func (fx *driverFixture) client(pbproxy string) *modfetch.Client {
	return &modfetch.Client{
		HTTP:          fx.HTTPClient(),
		Sources:       fx.Sources(pbproxy),
		Cache:         &modfetch.Cache{FS: memfs.New()},
		Lock:          &lockfile.File{},
		ResolveOrigin: fx.Resolve,
		Fetcher:       fx.Fetcher(),
	}
}

// serveModule registers an external module's archive on the proxy host.
func (fx *driverFixture) serveModule(t *testing.T, path, ver string, files map[string]string) {
	t.Helper()
	zip, _ := modfetchtest.ModuleZip(t, files)
	fx.Endpoint(path, ver, "zip", string(zip))
}

func ws(module, deps string) string {
	if deps == "" {
		return "module: " + module + "\n"
	}
	return "module: " + module + "\ndeps:\n" + deps
}

// twoMemberWorkspace is the standard fixture: members a and b, a
// depending on external m1, b on external m2; m1 transitively requires
// m2 at a higher version.
func twoMemberWorkspace(t *testing.T) (*Driver, *driverFixture) {
	d, fx := newDriver(t, map[string]string{
		"pb.work":   "use:\n  - a\n  - b\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		"b/pb.yaml": ws("example.com/b", "  example.com/m2: v1.0.0\n"),
	})
	fx.serveModule(t, "example.com/m1", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m1", "  example.com/m2: v1.2.0\n"),
	})
	fx.serveModule(t, "example.com/m2", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m2", ""),
	})
	fx.serveModule(t, "example.com/m2", "v1.2.0", map[string]string{
		"pb.yaml": ws("example.com/m2", ""),
	})
	return d, fx
}

// Selection runs over the union of the workspace's declarations
// through the verified pipeline (REQ-work-external-resolution,
// REQ-resolve-mvs): the transitive minimum wins over the direct one,
// and local paths never enter the list.
func TestBuildListSelectsOverUnion(t *testing.T) {
	d, _ := twoMemberWorkspace(t)
	list, crossings, err := d.BuildList(ctx)
	if err != nil {
		t.Fatalf("BuildList: %v", err)
	}
	if len(crossings) != 0 {
		t.Fatalf("crossings = %v", crossings)
	}
	got := make([]string, len(list))
	for i, r := range list {
		got[i] = r.Path + "@" + r.Version.String()
	}
	want := []string{"example.com/m1@v1.0.0", "example.com/m2@v1.2.0"}
	if !slices.Equal(got, want) {
		t.Fatalf("build list = %v, want %v", got, want)
	}
}

// A requirement naming a workspace-local path — declared by an
// external module — never reaches the fetch layer and never enters the
// build list (REQ-work-local-resolution's operative half).
func TestLocalPathsNeverFetched(t *testing.T) {
	d, fx := newDriver(t, map[string]string{
		"pb.work":   "use:\n  - a\n  - b\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		"b/pb.yaml": ws("example.com/b", ""),
	})
	// m1 requires the workspace-local example.com/b at a published
	// version that does not exist anywhere.
	fx.serveModule(t, "example.com/m1", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m1", "  example.com/b: v9.9.9\n"),
	})
	list, _, err := d.BuildList(ctx)
	if err != nil {
		t.Fatalf("BuildList: %v", err)
	}
	for _, r := range list {
		if r.Path == "example.com/b" {
			t.Fatalf("workspace-local path in the build list: %v", list)
		}
	}
	for key, n := range fx.Hits {
		if strings.Contains(key, "example.com/b") && n > 0 {
			t.Fatalf("local path fetched: %s hit %d times", key, n)
		}
	}
}

// Graph attributes root edges to the declaring workspace module (bare
// path) and renders every reachable inner edge, lexically sorted and
// deduplicated (REQ-dep-graph).
func TestGraphEdges(t *testing.T) {
	d, _ := twoMemberWorkspace(t)
	edges, err := d.Graph(ctx)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	got := make([]string, len(edges))
	for i, e := range edges {
		got[i] = e.Requirer + " " + e.Path + "@" + e.Version.String()
	}
	want := []string{
		"example.com/a example.com/m1@v1.0.0",
		"example.com/b example.com/m2@v1.0.0",
		"example.com/m1@v1.0.0 example.com/m2@v1.2.0",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("graph = %v, want %v", got, want)
	}
}

// Why returns a shortest chain, lexically least among equals, and nil
// for a path the graph never reaches (REQ-dep-why).
func TestWhyChains(t *testing.T) {
	d, _ := twoMemberWorkspace(t)

	chain, err := d.Why(ctx, "example.com/m2")
	if err != nil {
		t.Fatalf("Why: %v", err)
	}
	// b -> m2 (length 2) beats a -> m1 -> m2 (length 3).
	want := []string{"example.com/b", "example.com/m2@v1.0.0"}
	if !slices.Equal(chain, want) {
		t.Fatalf("chain = %v, want %v", chain, want)
	}

	chain, err = d.Why(ctx, "example.com/m1")
	if err != nil {
		t.Fatalf("Why: %v", err)
	}
	if !slices.Equal(chain, []string{"example.com/a", "example.com/m1@v1.0.0"}) {
		t.Fatalf("chain = %v", chain)
	}

	chain, err = d.Why(ctx, "example.com/absent")
	if err != nil || chain != nil {
		t.Fatalf("Why(absent) = %v, %v, want nil, nil", chain, err)
	}
}

// Selection never consults the version listing: whatever @v/list
// serves — absence, garbage, failure, or arbitrary content — the build
// list over declared requirements is identical
// (REQ-proxy-list-advisory).
func TestSelectionIndependentOfListProperty(t *testing.T) {
	d, _ := twoMemberWorkspace(t)
	want, _, err := d.BuildList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rapid.Check(t, func(rt *rapid.T) {
		d2, fx2 := twoMemberWorkspace(t)
		for _, m := range []string{"example.com/m1", "example.com/m2"} {
			key := modfetchtest.ProxyHost + "/" + m + "/@v/list"
			switch rapid.IntRange(0, 2).Draw(rt, m) {
			case 0: // absent — the default
			case 1:
				fx2.Endpoints[key] = rapid.SliceOfN(rapid.Byte(), 0, 64).Draw(rt, m+"-body")
			case 2:
				fx2.Status[key] = rapid.SampledFrom([]int{404, 410, 500, 503}).Draw(rt, m+"-status")
			}
		}
		got, _, err := d2.BuildList(ctx)
		if err != nil {
			rt.Fatalf("BuildList under list variation: %v", err)
		}
		if !slices.Equal(got, want) {
			rt.Fatalf("list state changed selection: %v vs %v", got, want)
		}
	})
}

// The build list is a pure function of the requirements and pinned
// module files — identical through the proxy and through the origin
// (REQ-resolve-determinism at the driver altitude).
func TestSelectionSourceIndependent(t *testing.T) {
	d, _ := twoMemberWorkspace(t)
	viaProxy, _, err := d.BuildList(ctx)
	if err != nil {
		t.Fatal(err)
	}

	dDirect, fx := newDriver(t, map[string]string{
		"pb.work":   "use:\n  - a\n  - b\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		"b/pb.yaml": ws("example.com/b", "  example.com/m2: v1.0.0\n"),
	})
	// Serve the same modules from an origin repository: m1 and m2 are
	// subtrees of one repo with subtree-namespace tags.
	repoFiles := map[string]string{
		"m1/pb.yaml": ws("example.com/m1", "  example.com/m2: v1.2.0\n"),
		"m2/pb.yaml": ws("example.com/m2", ""),
	}
	commit := fx.CommitFor(repoFiles, modfetchtest.GitWhen)
	fx.Repo.Ref("refs/tags/m1/v1.0.0", commit)
	fx.Repo.Ref("refs/tags/m2/v1.0.0", commit)
	fx.Repo.Ref("refs/tags/m2/v1.2.0", commit)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")
	fx.Subtrees["example.com/m1"] = "m1"
	fx.Subtrees["example.com/m2"] = "m2"
	dDirect.Client = fx.client("direct")

	viaDirect, _, err := dDirect.BuildList(ctx)
	if err != nil {
		t.Fatalf("direct BuildList: %v", err)
	}
	if !slices.Equal(viaProxy, viaDirect) {
		t.Fatalf("proxy list %v != direct list %v", viaProxy, viaDirect)
	}
}

// A tampered artifact anywhere in the graph fails resolution — the
// driver inherits the pipeline's no-transport-trust posture end to end
// (REQ-proxy-client-verification).
func TestTamperedGraphNodeFailsResolution(t *testing.T) {
	d, fx := twoMemberWorkspace(t)
	if _, _, err := d.BuildList(ctx); err != nil {
		t.Fatal(err)
	}
	// The proxy rewrites m2@v1.2.0 after pinning; a fresh cache forces
	// the refetch.
	zip, _ := modfetchtest.ModuleZip(t, map[string]string{
		"pb.yaml": ws("example.com/m2", "  example.com/mx: v1.0.0\n"),
	})
	fx.Endpoints[modfetchtest.ProxyHost+"/example.com/m2/@v/v1.2.0.zip"] = zip
	c2 := fx.client("proxy")
	c2.Lock = d.Client.Lock
	d.Client = c2
	if _, _, err := d.BuildList(ctx); err == nil {
		t.Fatal("a rewritten pinned artifact was accepted")
	} else if !strings.Contains(err.Error(), "digest") {
		t.Fatalf("err = %v, want the digest conviction", err)
	}
}

// Crossings propagate: an unaccepted major crossing fails with the
// build list still returned for diagnostics
// (REQ-resolve-major-crossing pass-through).
func TestCrossingsPropagate(t *testing.T) {
	d, fx := newDriver(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/hi: v1.0.0\n  example.com/lo: v1.0.0\n"),
	})
	fx.serveModule(t, "example.com/hi", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/hi", "  example.com/x: v2.0.0\n"),
	})
	fx.serveModule(t, "example.com/lo", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/lo", "  example.com/x: v1.0.0\n"),
	})
	fx.serveModule(t, "example.com/x", "v2.0.0", map[string]string{
		"pb.yaml": ws("example.com/x", ""),
	})
	fx.serveModule(t, "example.com/x", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/x", ""),
	})
	list, crossings, err := d.BuildList(ctx)
	var ce *mvs.CrossingError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want CrossingError", err)
	}
	if len(crossings) != 1 || crossings[0].Path != "example.com/x" {
		t.Fatalf("crossings = %v", crossings)
	}
	if len(list) == 0 {
		t.Fatal("diagnostic list not returned")
	}
}

// External modules disagreeing about a workspace-local path's major —
// even across majors — never fail selection: the local override is
// unconditional, no external version answers for the path, and the
// union's exclusion of locals means the root could never accept the
// crossing the error would demand (REQ-work-local-resolution against
// REQ-resolve-major-crossing).
func TestLocalPathMajorDisagreementIsNotACrossing(t *testing.T) {
	d, fx := newDriver(t, map[string]string{
		"pb.work":     "use:\n  - lib\n  - app\n",
		"lib/pb.yaml": ws("example.com/lib", ""),
		"app/pb.yaml": ws("example.com/app", "  example.com/m1: v1.0.0\n  example.com/m2: v1.0.0\n"),
	})
	fx.serveModule(t, "example.com/m1", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m1", "  example.com/lib: v2.0.0\n"),
	})
	fx.serveModule(t, "example.com/m2", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m2", "  example.com/lib: v1.0.0\n"),
	})
	list, crossings, err := d.BuildList(ctx)
	if err != nil {
		t.Fatalf("BuildList: %v", err)
	}
	if len(crossings) != 0 {
		t.Fatalf("crossings = %v, want none for a local path", crossings)
	}
	for _, r := range list {
		if r.Path == "example.com/lib" {
			t.Fatalf("local path in list: %v", list)
		}
	}
}

// A real external crossing still fails when a local-path disagreement
// rides alongside it: filtering local crossings never launders an
// external one.
func TestExternalCrossingSurvivesLocalFilter(t *testing.T) {
	d, fx := newDriver(t, map[string]string{
		"pb.work":     "use:\n  - lib\n  - app\n",
		"lib/pb.yaml": ws("example.com/lib", ""),
		"app/pb.yaml": ws("example.com/app", "  example.com/m1: v1.0.0\n  example.com/m2: v1.0.0\n"),
	})
	fx.serveModule(t, "example.com/m1", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m1", "  example.com/lib: v2.0.0\n  example.com/x: v2.0.0\n"),
	})
	fx.serveModule(t, "example.com/m2", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m2", "  example.com/lib: v1.0.0\n  example.com/x: v1.0.0\n"),
	})
	fx.serveModule(t, "example.com/x", "v2.0.0", map[string]string{"pb.yaml": ws("example.com/x", "")})
	fx.serveModule(t, "example.com/x", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/x", "")})
	_, crossings, err := d.BuildList(ctx)
	var ce *mvs.CrossingError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want CrossingError for the external path", err)
	}
	if len(ce.Crossings) != 1 || ce.Crossings[0].Path != "example.com/x" {
		t.Fatalf("error crossings = %v, want exactly example.com/x", ce.Crossings)
	}
	if len(crossings) != 1 || crossings[0].Path != "example.com/x" {
		t.Fatalf("crossings = %v, want exactly example.com/x", crossings)
	}
}

// Root-level failures propagate through every driver surface: a
// hand-crafted root with an invalid declared version (representable in
// the data model, unproducible through parsing) fails BuildList and
// Graph rather than resolving over garbage.
func TestDriverPropagatesRootErrors(t *testing.T) {
	bad := &workspace.Root{Dir: ".", Modules: []workspace.Module{{
		Dir:  ".",
		File: &modfile.File{Module: "example.com/a", Deps: map[string]string{"example.com/m": "not-a-version"}},
	}}}
	fx := modfetchtest.New(t)
	d := &Driver{Root: bad, Client: (&driverFixture{Fixture: fx}).client("proxy")}
	if _, _, err := d.BuildList(ctx); err == nil {
		t.Fatal("BuildList resolved over an invalid declared version")
	}
	if _, err := d.Graph(ctx); err == nil {
		t.Fatal("Graph rendered over an invalid declared version")
	}
	if _, err := d.Why(ctx, "example.com/m"); err == nil {
		t.Fatal("Why walked over an invalid declared version")
	}
}

// A graph node no source serves fails Graph and Why with the loader's
// error, never a silent partial graph.
func TestGraphOnMissingModuleFails(t *testing.T) {
	d, _ := newDriver(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/absent: v1.0.0\n"),
	})
	if _, err := d.Graph(ctx); err == nil {
		t.Fatal("Graph rendered with an unresolvable node")
	}
	if _, err := d.Why(ctx, "example.com/absent"); err == nil {
		t.Fatal("Why walked with an unresolvable node")
	}
}

// Graph ordering discriminates every comparator the edge sort uses:
// requirer first, then path within one requirer.
func TestGraphOrderingWithinRequirer(t *testing.T) {
	d, fx := newDriver(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/z: v1.0.0\n  example.com/b: v1.0.0\n"),
	})
	fx.serveModule(t, "example.com/z", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/z", "  example.com/b: v1.1.0\n  example.com/a2: v1.0.0\n"),
	})
	fx.serveModule(t, "example.com/b", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/b", "")})
	fx.serveModule(t, "example.com/b", "v1.1.0", map[string]string{"pb.yaml": ws("example.com/b", "")})
	fx.serveModule(t, "example.com/a2", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/a2", "")})
	edges, err := d.Graph(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(edges))
	for i, e := range edges {
		got[i] = e.Requirer + " " + e.Path + "@" + e.Version.String()
	}
	want := []string{
		"example.com/a example.com/b@v1.0.0",
		"example.com/a example.com/z@v1.0.0",
		"example.com/z@v1.0.0 example.com/a2@v1.0.0",
		"example.com/z@v1.0.0 example.com/b@v1.1.0",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("graph = %v, want %v", got, want)
	}
}

// Why terminates on cyclic requirement graphs and starts only from
// workspace modules: a mid-graph node must never seed a chain, and a
// cycle must not loop the walk.
func TestWhyCyclesAndStartDiscipline(t *testing.T) {
	d, fx := newDriver(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
	})
	// m1 <-> m2 cycle; m3 hangs off m2, and a longer tail (m4 -> m5)
	// sits lexically after the cycle edge so the walk must survive a
	// dequeued revisit before reaching fresh nodes.
	fx.serveModule(t, "example.com/m1", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m1", "  example.com/m2: v1.0.0\n"),
	})
	fx.serveModule(t, "example.com/m2", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m2", "  example.com/m1: v1.0.0\n  example.com/m3: v1.0.0\n  example.com/m4: v1.0.0\n"),
	})
	fx.serveModule(t, "example.com/m3", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m3", ""),
	})
	fx.serveModule(t, "example.com/m4", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m4", "  example.com/m5: v1.0.0\n"),
	})
	fx.serveModule(t, "example.com/m5", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m5", ""),
	})

	chain, err := d.Why(ctx, "example.com/m3")
	if err != nil {
		t.Fatalf("Why: %v", err)
	}
	want := []string{"example.com/a", "example.com/m1@v1.0.0", "example.com/m2@v1.0.0", "example.com/m3@v1.0.0"}
	if !slices.Equal(chain, want) {
		t.Fatalf("chain = %v, want %v (chains start at workspace modules only)", chain, want)
	}
	// m5 sits beyond a node whose duplicate enqueue precedes it: the
	// walk must skip the revisit and keep going.
	chain, err = d.Why(ctx, "example.com/m5")
	if err != nil {
		t.Fatalf("Why(m5): %v", err)
	}
	if !slices.Equal(chain, []string{"example.com/a", "example.com/m1@v1.0.0", "example.com/m2@v1.0.0", "example.com/m4@v1.0.0", "example.com/m5@v1.0.0"}) {
		t.Fatalf("chain = %v", chain)
	}
	// A missing target drains the cyclic queue without looping.
	if chain, err := d.Why(ctx, "example.com/absent"); err != nil || chain != nil {
		t.Fatalf("Why(absent) over a cycle = %v, %v", chain, err)
	}
}

// Graph output is a pure function of the graph: repeated calls agree
// exactly — map iteration and traversal order never leak through the
// sort (REQ-resolve-determinism).
func TestGraphDeterministicAcrossCalls(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(2, 5).Draw(rt, "deps")
		deps := ""
		for i := 0; i < n; i++ {
			deps += "  example.com/d" + string(rune('a'+i)) + ": v1.0.0\n"
		}
		d, fx := newDriver(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", deps),
		})
		for i := 0; i < n; i++ {
			name := "example.com/d" + string(rune('a'+i))
			fx.serveModule(t, name, "v1.0.0", map[string]string{"pb.yaml": ws(name, "")})
		}
		first, err := d.Graph(ctx)
		if err != nil {
			rt.Fatal(err)
		}
		if !slices.IsSortedFunc(first, func(a, b mvs.Edge) int {
			if c := strings.Compare(a.Requirer, b.Requirer); c != 0 {
				return c
			}
			return strings.Compare(a.Path, b.Path)
		}) {
			rt.Fatalf("graph not sorted: %v", first)
		}
		second, err := d.Graph(ctx)
		if err != nil {
			rt.Fatal(err)
		}
		if !slices.Equal(first, second) {
			rt.Fatalf("graph differs across calls:\n%v\n%v", first, second)
		}
	})
}

// The use list orders workspace modules anti-lexically: graph edges
// must still come out lexically by requirer — insertion grouping never
// substitutes for the sort.
func TestGraphOrderAcrossRequirers(t *testing.T) {
	d, fx := newDriver(t, map[string]string{
		"pb.work":   "use:\n  - zz\n  - aa\n",
		"zz/pb.yaml": ws("example.com/zz", "  example.com/dep: v1.0.0\n"),
		"aa/pb.yaml": ws("example.com/aa", "  example.com/dep: v1.0.0\n"),
	})
	fx.serveModule(t, "example.com/dep", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/dep", "")})
	edges, err := d.Graph(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(edges))
	for i, e := range edges {
		got[i] = e.Requirer + " " + e.Path + "@" + e.Version.String()
	}
	want := []string{
		"example.com/aa example.com/dep@v1.0.0",
		"example.com/zz example.com/dep@v1.0.0",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("graph = %v, want lexical requirer order %v", got, want)
	}
}
