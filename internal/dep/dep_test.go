package dep

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/greatliontech/pb/internal/check/lintfile"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/proto/importcheck"
	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/pb/internal/source/fetch"
	"github.com/greatliontech/pb/internal/testing/fetchtest"
	"github.com/greatliontech/pb/internal/testing/fetchtest/assemble"
)

var ctx = context.Background()

// depFixture is a writable workspace tree over the shared client
// fixture.
type depFixture struct {
	*fetchtest.Fixture
	ws billy.Filesystem
}

func newDep(t *testing.T, files map[string]string) *depFixture {
	t.Helper()
	return newDepOn(t, memfs.New(), files)
}

// newDepOn is newDep over a caller-chosen filesystem — a real one
// where in-memory semantics (implicit directories) would hide a write
// path's behavior.
func newDepOn(t *testing.T, fs billy.Filesystem, files map[string]string) *depFixture {
	t.Helper()
	fx := &depFixture{Fixture: fetchtest.New(t), ws: fs}
	for p, body := range files {
		if err := util.WriteFile(fx.ws, p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return fx
}

func (fx *depFixture) client(pbproxy string) *fetch.Client {
	return assemble.Client(fx.Fixture, pbproxy)
}

func (fx *depFixture) session(t *testing.T, dir string) *Session {
	t.Helper()
	s, err := Load(Config{WS: fx.ws, Dir: dir, Client: fx.client("proxy")})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (fx *depFixture) write(t *testing.T, name, content string) {
	t.Helper()
	if err := util.WriteFile(fx.ws, name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (fx *depFixture) read(t *testing.T, name string) string {
	t.Helper()
	b, err := util.ReadFile(fx.ws, name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (fx *depFixture) serve(t *testing.T, path, ver string, files map[string]string) {
	t.Helper()
	zip, _ := fetchtest.ModuleZip(t, files)
	fx.Endpoint(path, ver, "zip", string(zip))
}

func ws(module, deps string) string {
	if deps == "" {
		return "module: " + module + "\n"
	}
	return "module: " + module + "\ndeps:\n" + deps
}

// Init writes exactly one canonical module file and refuses to
// overwrite or accept an invalid path (REQ-dep-init).
func TestInit(t *testing.T) {
	fx := newDep(t, nil)
	if err := Init(fx.ws, "m", "example.com/m"); err != nil {
		t.Fatal(err)
	}
	if got := fx.read(t, "m/pb.yaml"); got != "module: example.com/m\n" {
		t.Fatalf("pb.yaml = %q", got)
	}
	if err := Init(fx.ws, "m", "example.com/other"); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("re-init err = %v", err)
	}
	if got := fx.read(t, "m/pb.yaml"); got != "module: example.com/m\n" {
		t.Fatalf("re-init changed the file: %q", got)
	}
	if err := Init(fx.ws, "x", "Not A Path"); err == nil {
		t.Fatal("invalid path accepted")
	}
	if _, err := fx.ws.Stat("x/pb.yaml"); err == nil {
		t.Fatal("invalid init wrote a file")
	}
}

// Download pins and caches the whole build list and writes the
// lockfile at the root; a second run fetches nothing and rewrites
// nothing (REQ-dep-download, REQ-lock-canonical-emission).
func TestDownloadVerb(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
	})
	fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m1", "  example.com/m2: v1.0.0\n"),
	})
	fx.serve(t, "example.com/m2", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m2", "")})
	fx.Endpoint("example.com/m1", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	fx.Endpoint("example.com/m2", "v1.0.0", "info", `{"version":"v1.0.0"}`)

	s := fx.session(t, ".")
	var out bytes.Buffer
	if err := Download(ctx, s, &out); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := out.String(); got != "example.com/m1@v1.0.0\nexample.com/m2@v1.0.0\n" {
		t.Fatalf("output = %q", got)
	}
	lockBytes := fx.read(t, "pb.lock")
	if !strings.Contains(lockBytes, "example.com/m1") || !strings.Contains(lockBytes, "example.com/m2") {
		t.Fatalf("lockfile = %q", lockBytes)
	}

	// Second run through a fresh session: same cache, no new fetches
	// of version-addressed artifacts, lockfile byte-identical.
	cache := s.Client.Cache
	s2 := fx.session(t, ".")
	s2.Client.Cache = cache
	before := map[string]int{}
	for k, v := range fx.Hits {
		before[k] = v
	}
	if err := Download(ctx, s2, &out); err != nil {
		t.Fatal(err)
	}
	for k, v := range fx.Hits {
		if strings.Contains(k, "@v/") && v != before[k] {
			t.Fatalf("second download refetched %s", k)
		}
	}
	if fx.read(t, "pb.lock") != lockBytes {
		t.Fatal("second download rewrote the lockfile")
	}
}

// Graph prints one edge per line in the spec's format; Why prints
// shortest chains and not-needed answers, sharing one graph
// computation (REQ-dep-graph, REQ-dep-why).
func TestGraphAndWhyVerbs(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
	})
	fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m1", "  example.com/m2: v1.0.0\n"),
	})
	fx.serve(t, "example.com/m2", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m2", "")})

	s := fx.session(t, ".")
	var out bytes.Buffer
	if err := Graph(ctx, s, &out); err != nil {
		t.Fatal(err)
	}
	want := "example.com/a example.com/m1@v1.0.0\nexample.com/m1@v1.0.0 example.com/m2@v1.0.0\n"
	if out.String() != want {
		t.Fatalf("graph = %q, want %q", out.String(), want)
	}

	out.Reset()
	if err := Why(ctx, s, &out, "example.com/m2", "example.com/absent"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "example.com/a\nexample.com/m1@v1.0.0\nexample.com/m2@v1.0.0\n") {
		t.Fatalf("why output = %q", got)
	}
	if !strings.Contains(got, "(module example.com/absent is not needed)") {
		t.Fatalf("why output = %q", got)
	}
}

// Tidy reconciles declarations with imports (REQ-dep-tidy): unused
// declarations drop, directly-imported transitive modules gain
// declarations at selected versions, workspace-local imports keep
// their declared version, stale pins are pruned, and a second run
// changes nothing.
func TestTidy(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":       "use:\n  - a\n  - lib\n",
		"a/pb.yaml":     ws("example.com/a", "  example.com/m1: v1.0.0\n  example.com/unused: v1.0.0\n  example.com/lib: v0.1.0\n"),
		"a/x.proto":     "syntax = \"proto3\";\nimport \"m1.proto\";\nimport \"m2.proto\";\nimport \"lib.proto\";\n",
		"lib/pb.yaml":   ws("example.com/lib", ""),
		"lib/lib.proto": "syntax = \"proto3\";\n",
	})
	fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{
		"pb.yaml":  ws("example.com/m1", "  example.com/m2: v1.2.0\n"),
		"m1.proto": "syntax = \"proto3\";\nimport \"m2.proto\";\n",
	})
	fx.serve(t, "example.com/m2", "v1.2.0", map[string]string{
		"pb.yaml":  ws("example.com/m2", ""),
		"m2.proto": "syntax = \"proto3\";\n",
	})
	fx.serve(t, "example.com/unused", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/unused", ""),
	})

	s := fx.session(t, ".")
	if err := Tidy(ctx, s); err != nil {
		t.Fatalf("Tidy: %v", err)
	}
	got := fx.read(t, "a/pb.yaml")
	want := "module: example.com/a\ndeps:\n  example.com/lib: v0.1.0\n  example.com/m1: v1.0.0\n  example.com/m2: v1.2.0\n"
	if got != want {
		t.Fatalf("tidied a/pb.yaml = %q, want %q", got, want)
	}
	// The unused module's pin is pruned.
	if strings.Contains(fx.read(t, "pb.lock"), "example.com/unused") {
		t.Fatal("stale pin survived tidy")
	}

	// Idempotence: a second run through a fresh session changes no
	// bytes.
	lock1, mod1 := fx.read(t, "pb.lock"), fx.read(t, "a/pb.yaml")
	s2 := fx.session(t, ".")
	s2.Client.Cache = s.Client.Cache
	if err := Tidy(ctx, s2); err != nil {
		t.Fatalf("second Tidy: %v", err)
	}
	if fx.read(t, "pb.lock") != lock1 || fx.read(t, "a/pb.yaml") != mod1 {
		t.Fatal("tidy is not idempotent")
	}
}

// An import no module satisfies fails tidy with the exhaustive report
// (REQ-resolve-unsatisfied-imports through REQ-dep-tidy): tidy never
// invents a dependency.
func TestTidyUnsatisfiedImportFails(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", ""),
		"a/x.proto": "syntax = \"proto3\";\nimport \"nowhere.proto\";\n",
	})
	s := fx.session(t, ".")
	err := Tidy(ctx, s)
	var ue *importcheck.UnsatisfiedError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want UnsatisfiedError", err)
	}
	if got := fx.read(t, "a/pb.yaml"); got != ws("example.com/a", "") {
		t.Fatalf("failed tidy rewrote the module file: %q", got)
	}
}

// A workspace-local import with no declared version fails tidy: the
// published declaration must stand alone, and tidy cannot invent it.
func TestTidyLocalImportNeedsDeclaredVersion(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":       "use:\n  - a\n  - lib\n",
		"a/pb.yaml":     ws("example.com/a", ""),
		"a/x.proto":     "syntax = \"proto3\";\nimport \"lib.proto\";\n",
		"lib/pb.yaml":   ws("example.com/lib", ""),
		"lib/lib.proto": "syntax = \"proto3\";\n",
	})
	s := fx.session(t, ".")
	if err := Tidy(ctx, s); err == nil ||
		!strings.Contains(err.Error(), "declares no version") {
		t.Fatalf("err = %v, want the missing-declared-version failure", err)
	}
}

// Update moves named requirements to the highest discovered release,
// rewrites the declaring module files canonically, and pins the new
// versions; the failure arms fail closed (REQ-dep-update).
func TestUpdateVerb(t *testing.T) {
	files := map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
	}
	serve := func(fx *depFixture) {
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		fx.serve(t, "example.com/m1", "v1.2.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		fx.Endpoints[fetchtest.ProxyHost+"/example.com/m1/@v/list"] = []byte("v1.0.0\nv1.2.0\n")
	}

	t.Run("named update moves to the highest release", func(t *testing.T) {
		fx := newDep(t, files)
		serve(fx)
		s := fx.session(t, ".")
		var out bytes.Buffer
		if err := Update(ctx, s, &out, nil, "example.com/m1"); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got := fx.read(t, "a/pb.yaml"); got != ws("example.com/a", "  example.com/m1: v1.2.0\n") {
			t.Fatalf("a/pb.yaml = %q", got)
		}
		if !strings.Contains(fx.read(t, "pb.lock"), "v1.2.0") {
			t.Fatal("updated version not pinned")
		}
	})

	t.Run("named update of a declared plugin re-resolves it", func(t *testing.T) {
		withPlugin := map[string]string{}
		for k, v := range files {
			withPlugin[k] = v
		}
		withPlugin["pb.gen.yaml"] = "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n  - local: tools/local-gen\n    out: gen\n"
		fx := newDep(t, withPlugin)
		serve(fx)
		s := fx.session(t, ".")
		moved := "sha256:" + strings.Repeat("22", 32)
		up := &stubUpdater{lock: s.Lock, after: lockfile.PluginPin{Ref: "ghcr.io/o/p:v1", Scheme: lockfile.SchemeOCI, Digest: moved}}
		var out bytes.Buffer
		if err := Update(ctx, s, &out, up, "ghcr.io/o/p:v1", "example.com/m1"); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if up.got != "ghcr.io/o/p:v1" {
			t.Fatalf("the updater saw %q", up.got)
		}
		if !strings.Contains(out.String(), "plugin ghcr.io/o/p:v1: ") || !strings.Contains(out.String(), "provenance none -> image-signature https://ci.example/wf by https://issuer.example") || !strings.Contains(out.String(), "example.com/m1 v1.0.0 -> v1.2.0") {
			t.Fatalf("out = %q", out.String())
		}
		// A plugin named alone: the pin it rewrote is saved.
		s = fx.session(t, ".")
		up = &stubUpdater{lock: s.Lock, after: lockfile.PluginPin{Ref: "ghcr.io/o/p:v1", Scheme: lockfile.SchemeOCI, Digest: "sha256:" + strings.Repeat("33", 32)}}
		if err := Update(ctx, s, &bytes.Buffer{}, up, "ghcr.io/o/p:v1"); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if !strings.Contains(fx.read(t, "pb.lock"), strings.Repeat("33", 32)) {
			t.Fatal("a plugin-only update left the lockfile unsaved")
		}
		// Every argument is placed before any is moved: a name that
		// is nothing fails the run whole, the plugin untouched.
		up.got = ""
		if err := Update(ctx, s, &bytes.Buffer{}, up, "ghcr.io/o/p:v1", "example.com/none"); err == nil || !strings.Contains(err.Error(), "no workspace module requires") || up.got != "" {
			t.Fatalf("a bad module beside a plugin: %v, updater saw %q", err, up.got)
		}
		// A plugin moved is durable before the module arm runs: a
		// module whose listing fails afterwards fails the run, the
		// plugin pin saved and reported.
		fx.Endpoints[fetchtest.ProxyHost+"/example.com/m1/@v/list"] = []byte("")
		s = fx.session(t, ".")
		up = &stubUpdater{lock: s.Lock, after: lockfile.PluginPin{Ref: "ghcr.io/o/p:v1", Scheme: lockfile.SchemeOCI, Digest: "sha256:" + strings.Repeat("44", 32)}}
		out.Reset()
		if err := Update(ctx, s, &out, up, "ghcr.io/o/p:v1", "example.com/m1"); err == nil || !strings.Contains(err.Error(), "no discoverable release") {
			t.Fatalf("a module failing after the plugin: %v", err)
		}
		if !strings.Contains(fx.read(t, "pb.lock"), strings.Repeat("44", 32)) || !strings.Contains(out.String(), "plugin ghcr.io/o/p:v1: ") {
			t.Fatalf("the moved plugin was not durable before the module arm: %q", out.String())
		}
		fx.Endpoints[fetchtest.ProxyHost+"/example.com/m1/@v/list"] = []byte("v1.0.0\nv1.2.0\n")
		// A plugin named with no updater wired fails; a name that is
		// not a declared oci plugin — undeclared, or a local entry —
		// is a module; the same name twice is one update.
		if err := Update(ctx, s, &bytes.Buffer{}, nil, "ghcr.io/o/p:v1"); err == nil || !strings.Contains(err.Error(), "no plugin updater") {
			t.Fatalf("no updater: %v", err)
		}
		for _, name := range []string{"ghcr.io/o/other:v1", "tools/local-gen"} {
			if err := Update(ctx, s, &bytes.Buffer{}, up, name); err == nil || !strings.Contains(err.Error(), "no workspace module requires") {
				t.Fatalf("%s: %v", name, err)
			}
		}
		up.calls = 0
		if err := Update(ctx, s, &bytes.Buffer{}, up, "ghcr.io/o/p:v1", "ghcr.io/o/p:v1"); err != nil || up.calls != 1 {
			t.Fatalf("a repeated name: %d updates, %v", up.calls, err)
		}
		// A trust policy forbidding the oci scheme refuses the update
		// as generation would, before the updater runs.
		s.Client.Policy.Execution.Schemes = []string{"local"}
		up.got = ""
		if err := Update(ctx, s, &bytes.Buffer{}, up, "ghcr.io/o/p:v1"); err == nil || !strings.Contains(err.Error(), "does not permit oci-scheme") || up.got != "" {
			t.Fatalf("under a policy forbidding oci: %v, updater saw %q", err, up.got)
		}
		// Without arguments plugins are left as pinned, and the
		// configuration is not even read.
		s = fx.session(t, ".")
		up = &stubUpdater{lock: s.Lock}
		if err := Update(ctx, s, &bytes.Buffer{}, up); err != nil || up.got != "" {
			t.Fatalf("an unnamed update touched a plugin: %q %v", up.got, err)
		}
		fx.write(t, "pb.gen.yaml", "plugins:\n  - ref: [not, a, string]\n")
		s = fx.session(t, ".")
		if err := Update(ctx, s, &bytes.Buffer{}, up); err != nil {
			t.Fatalf("an unnamed update read the configuration: %v", err)
		}
	})

	t.Run("named update of an unrequired module fails", func(t *testing.T) {
		fx := newDep(t, files)
		serve(fx)
		s := fx.session(t, ".")
		if err := Update(ctx, s, &bytes.Buffer{}, nil, "example.com/none"); err == nil ||
			!strings.Contains(err.Error(), "no workspace module requires") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("named update with no discoverable release fails", func(t *testing.T) {
		fx := newDep(t, files)
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		fx.Endpoints[fetchtest.ProxyHost+"/example.com/m1/@v/list"] = []byte("")
		s := fx.session(t, ".")
		if err := Update(ctx, s, &bytes.Buffer{}, nil, "example.com/m1"); err == nil ||
			!strings.Contains(err.Error(), "no discoverable release") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("named update against a regressed origin fails", func(t *testing.T) {
		fx := newDep(t, files)
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		fx.Endpoints[fetchtest.ProxyHost+"/example.com/m1/@v/list"] = []byte("v0.9.0\n")
		s := fx.session(t, ".")
		if err := Update(ctx, s, &bytes.Buffer{}, nil, "example.com/m1"); err == nil ||
			!strings.Contains(err.Error(), "origin regressed") {
			t.Fatalf("err = %v", err)
		}
		if got := fx.read(t, "a/pb.yaml"); !strings.Contains(got, "v1.0.0") {
			t.Fatalf("regressed update rewrote the declaration: %q", got)
		}
	})

	t.Run("unnamed sweep updates only higher releases and skips the rest", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n  example.com/m3: v1.0.0\n"),
		})
		serve(fx)
		fx.serve(t, "example.com/m3", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m3", "")})
		fx.Endpoints[fetchtest.ProxyHost+"/example.com/m3/@v/list"] = []byte("")
		s := fx.session(t, ".")
		if err := Update(ctx, s, &bytes.Buffer{}, nil); err != nil {
			t.Fatalf("Update: %v", err)
		}
		got := fx.read(t, "a/pb.yaml")
		if !strings.Contains(got, "example.com/m1: v1.2.0") || !strings.Contains(got, "example.com/m3: v1.0.0") {
			t.Fatalf("a/pb.yaml = %q", got)
		}
	})
}

// Verify recomputes cached artifacts against pins: clean caches
// verify, tampered entries are reported exhaustively and fail, absent
// entries are outside its scope (REQ-dep-verify).
func TestVerifyVerb(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
	})
	fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
	fx.Endpoint("example.com/m1", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	s := fx.session(t, ".")
	if err := Download(ctx, s, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Verify(ctx, s, &out); err != nil {
		t.Fatalf("Verify over a clean cache: %v", err)
	}
	if !strings.Contains(out.String(), "verified 1 cached module(s)") {
		t.Fatalf("output = %q", out.String())
	}

	// A tampered cache entry is a reported mismatch and a failure.
	wrong, _ := fetchtest.ModuleZip(t, map[string]string{"pb.yaml": ws("example.com/m1", "  example.com/x: v1.0.0\n")})
	if err := s.Client.Cache.Put("example.com/m1", mustVer(t, "v1.0.0"), fetch.KindZip, wrong); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err := Verify(ctx, s, &out)
	if err == nil || !strings.Contains(err.Error(), "disagree with their pins") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "example.com/m1@v1.0.0: digest") {
		t.Fatalf("report = %q", out.String())
	}

	// An absent cache entry is outside verify's scope.
	s2 := fx.session(t, ".")
	out.Reset()
	if err := Verify(ctx, s2, &out); err != nil {
		t.Fatalf("Verify with empty cache: %v", err)
	}
	if !strings.Contains(out.String(), "verified 0 cached module(s)") {
		t.Fatalf("output = %q", out.String())
	}
}

// Load enforces membership and reads the root's trust policy; SaveLock
// writes only on change.
func TestSessionLoad(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":       "use:\n  - a\n",
		"a/pb.yaml":     ws("example.com/a", ""),
		"b/pb.yaml":     ws("example.com/b", ""),
		"pb.trust.yaml": "default: require-provenance\n",
	})
	if _, err := Load(Config{WS: fx.ws, Dir: "b", Client: fx.client("proxy")}); err == nil {
		t.Fatal("membership violation tolerated")
	}
	s := fx.session(t, "a")
	if s.Client.Policy == nil {
		t.Fatal("trust policy not loaded from the root")
	}
	if err := s.SaveLock(); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.ws.Stat("pb.lock"); err == nil {
		t.Fatal("SaveLock wrote an unchanged empty lockfile")
	}
}

func mustVer(t *testing.T, s string) version.Version {
	t.Helper()
	v, err := version.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A reused client never carries the previous root's trust policy into
// the next session: Load resets before the conditional read.
func TestLoadResetsPolicyOnReuse(t *testing.T) {
	fx := newDep(t, map[string]string{
		"strict/pb.yaml":       ws("example.com/strict", ""),
		"strict/pb.trust.yaml": "default: require-provenance\n",
		"open/pb.yaml":         ws("example.com/open", ""),
	})
	client := fx.client("proxy")
	s1, err := Load(Config{WS: fx.ws, Dir: "strict", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if s1.Client.Policy == nil {
		t.Fatal("strict root's policy not loaded")
	}
	s2, err := Load(Config{WS: fx.ws, Dir: "open", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if s2.Client.Policy == nil || !reflect.DeepEqual(*s2.Client.Policy, trust.Policy{}) {
		t.Fatalf("the previous root's trust policy leaked into a policy-less root: %+v", s2.Client.Policy)
	}
}

// Session and verb error arms: corrupt root files, storage faults, and
// mid-verb failures each surface as the operation's error.
func TestSessionAndVerbFaults(t *testing.T) {
	base := map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
	}

	t.Run("corrupt lockfile fails Load", func(t *testing.T) {
		files := map[string]string{"pb.lock": "not: [a lockfile"}
		for k, v := range base {
			files[k] = v
		}
		fx := newDep(t, files)
		if _, err := Load(Config{WS: fx.ws, Dir: ".", Client: fx.client("proxy")}); err == nil ||
			!strings.Contains(err.Error(), "pb.lock") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("corrupt trust policy fails Load", func(t *testing.T) {
		files := map[string]string{"pb.trust.yaml": "default: [nonsense"}
		for k, v := range base {
			files[k] = v
		}
		fx := newDep(t, files)
		if _, err := Load(Config{WS: fx.ws, Dir: ".", Client: fx.client("proxy")}); err == nil ||
			!strings.Contains(err.Error(), "pb.trust.yaml") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("lockfile read fault fails Load", func(t *testing.T) {
		fx := newDep(t, base)
		efs := &fetchtest.ErrFS{Filesystem: fx.ws, FailOpenSuffix: "pb.lock", PutFailAfter: -1}
		if _, err := Load(Config{WS: efs, Dir: ".", Client: fx.client("proxy")}); !errors.Is(err, fetchtest.ErrInjected) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("trust read fault fails Load", func(t *testing.T) {
		fx := newDep(t, base)
		efs := &fetchtest.ErrFS{Filesystem: fx.ws, FailOpenSuffix: "pb.trust.yaml", PutFailAfter: -1}
		if _, err := Load(Config{WS: efs, Dir: ".", Client: fx.client("proxy")}); !errors.Is(err, fetchtest.ErrInjected) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("lockfile write fault fails the verb", func(t *testing.T) {
		fx := newDep(t, base)
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		fx.Endpoint("example.com/m1", "v1.0.0", "info", `{"version":"v1.0.0"}`)
		efs := &fetchtest.ErrFS{Filesystem: fx.ws, FailRenameSfx: "pb.lock", PutFailAfter: -1}
		s, err := Load(Config{WS: efs, Dir: ".", Client: fx.client("proxy")})
		if err != nil {
			t.Fatal(err)
		}
		if err := Download(ctx, s, &bytes.Buffer{}); !errors.Is(err, fetchtest.ErrInjected) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("init stat fault surfaces", func(t *testing.T) {
		fx := newDep(t, nil)
		efs := &fetchtest.ErrFS{Filesystem: fx.ws, PutFailAfter: -1, FailTempFile: true}
		if err := Init(efs, "m", "example.com/m"); !errors.Is(err, fetchtest.ErrInjected) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a failing graph node fails download graph and why", func(t *testing.T) {
		fx := newDep(t, base) // m1 never served
		s := fx.session(t, ".")
		if err := Download(ctx, s, &bytes.Buffer{}); err == nil {
			t.Fatal("Download resolved an unservable module")
		}
		if err := Graph(ctx, s, &bytes.Buffer{}); err == nil {
			t.Fatal("Graph resolved an unservable module")
		}
		if err := Why(ctx, s, &bytes.Buffer{}, "example.com/m1"); err == nil {
			t.Fatal("Why resolved an unservable module")
		}
	})

	t.Run("mid-list download failure surfaces", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n  example.com/m2: v1.0.0\n"),
		})
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		fx.serve(t, "example.com/m2", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m2", "")})
		fx.Endpoint("example.com/m1", "v1.0.0", "info", `{"version":"v1.0.0"}`)
		// m2 has no info endpoint: its Download arm fails after m1's
		// succeeded.
		s := fx.session(t, ".")
		if err := Download(ctx, s, &bytes.Buffer{}); err == nil {
			t.Fatal("a failing artifact set did not fail download")
		}
	})
}

// SaveLock's change discipline: an emptied pin store rewrites an
// existing lockfile, an unchanged store writes nothing even on a
// write-refusing tree, and the loaded-bytes memo keeps later saves
// quiet.
func TestSaveLockChangeDiscipline(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", ""),
		"pb.lock":   "version: 1\nmodules:\n  - path: example.com/stale\n    version: v1.0.0\n    digest: pb1:" + strings.Repeat("0", 64) + "\n    provenance: none\n",
	})

	t.Run("emptied pins rewrite the existing lockfile", func(t *testing.T) {
		s := fx.session(t, ".")
		if err := Tidy(ctx, s); err != nil {
			t.Fatalf("Tidy: %v", err)
		}
		after := fx.read(t, "pb.lock")
		if strings.Contains(after, "example.com/stale") {
			t.Fatalf("stale pin survived: %q", after)
		}
	})

	t.Run("unchanged pins write nothing even on a write-refusing tree", func(t *testing.T) {
		fx2 := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", ""),
		})
		efs := &fetchtest.ErrFS{Filesystem: fx2.ws, FailRename: true, FailTempFile: true, PutFailAfter: -1}
		s, err := Load(Config{WS: efs, Dir: ".", Client: fx2.client("proxy")})
		if err != nil {
			t.Fatal(err)
		}
		if err := Graph(ctx, s, &bytes.Buffer{}); err != nil {
			t.Fatalf("Graph on an empty workspace with a frozen tree: %v", err)
		}
		// A change followed by a save, then a second save with no
		// change: the second must not touch the tree either.
		fx3 := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		})
		fx3.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		s3 := fx3.session(t, ".")
		if err := Graph(ctx, s3, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		s3.WS = &fetchtest.ErrFS{Filesystem: fx3.ws, FailRename: true, FailTempFile: true, PutFailAfter: -1}
		if err := s3.SaveLock(); err != nil {
			t.Fatalf("no-change save touched the tree: %v", err)
		}
	})
}

// Why's multi-target output is exact: header lines, blank separator,
// chains, and not-needed answers in argument order.
func TestWhyExactOutput(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
	})
	fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
	s := fx.session(t, ".")
	var out bytes.Buffer
	if err := Why(ctx, s, &out, "example.com/m1", "example.com/absent"); err != nil {
		t.Fatal(err)
	}
	want := "# example.com/m1\nexample.com/a\nexample.com/m1@v1.0.0\n\n# example.com/absent\n(module example.com/absent is not needed)\n"
	if out.String() != want {
		t.Fatalf("why = %q, want %q", out.String(), want)
	}
}

// Accepted major crossings are warnings on the verb output, naming the
// module, selected version, and the lowest crossed requirement
// (REQ-resolve-major-crossing's warning half).
func TestDownloadWarnsAcceptedCrossing(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/x: v2.0.0\n  example.com/lo: v1.0.0\n"),
	})
	fx.serve(t, "example.com/x", "v2.0.0", map[string]string{"pb.yaml": ws("example.com/x", "")})
	fx.serve(t, "example.com/lo", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/lo", "  example.com/x: v1.0.0\n"),
	})
	fx.serve(t, "example.com/x", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/x", "")})
	fx.Endpoint("example.com/x", "v2.0.0", "info", `{"version":"v2.0.0"}`)
	fx.Endpoint("example.com/lo", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	fx.Endpoint("example.com/x", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	s := fx.session(t, ".")
	var out bytes.Buffer
	if err := Download(ctx, s, &out); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !strings.Contains(out.String(), "# warning: selecting example.com/x v2.0.0 crosses major above v1.0.0 required by example.com/lo@v1.0.0") {
		t.Fatalf("output = %q", out.String())
	}
}

// Tidy's remaining arms: parse failures on both sides of the import
// view, a failing archive read, per-module attribution, well-known
// precedence over shipped copies, and self-import skipping.
func TestTidyArms(t *testing.T) {
	t.Run("unresolvable graph fails tidy", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/gone: v1.0.0\n"),
		})
		s := fx.session(t, ".")
		if err := Tidy(ctx, s); err == nil {
			t.Fatal("tidy resolved an unservable module")
		}
	})

	t.Run("unparsable workspace proto fails tidy", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", ""),
			"a/x.proto": "this is not protobuf {{{",
		})
		s := fx.session(t, ".")
		if err := Tidy(ctx, s); err == nil || !strings.Contains(err.Error(), "x.proto") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("unparsable external proto fails tidy", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		})
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{
			"pb.yaml":   ws("example.com/m1", ""),
			"bad.proto": "not protobuf }}}",
		})
		s := fx.session(t, ".")
		if err := Tidy(ctx, s); err == nil || !strings.Contains(err.Error(), "bad.proto") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("failing archive read fails tidy", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		})
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		s := fx.session(t, ".")
		s.Client.Cache = &fetch.Cache{FS: &fetchtest.ErrFS{Filesystem: memfs.New(), FailOpenSuffix: ".zip", PutFailAfter: -1}}
		if err := Tidy(ctx, s); !errors.Is(err, fetchtest.ErrInjected) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("attribution is per module", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n  - b\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n  example.com/m2: v1.0.0\n"),
			"b/pb.yaml": ws("example.com/b", "  example.com/m1: v1.0.0\n  example.com/m2: v1.0.0\n"),
			"a/x.proto": "syntax = \"proto3\";\nimport \"m1.proto\";\n",
			"b/y.proto": "syntax = \"proto3\";\nimport \"m2.proto\";\n",
		})
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", ""), "m1.proto": "syntax = \"proto3\";\n"})
		fx.serve(t, "example.com/m2", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m2", ""), "m2.proto": "syntax = \"proto3\";\n"})
		s := fx.session(t, ".")
		if err := Tidy(ctx, s); err != nil {
			t.Fatal(err)
		}
		if got := fx.read(t, "a/pb.yaml"); got != ws("example.com/a", "  example.com/m1: v1.0.0\n") {
			t.Fatalf("a/pb.yaml = %q", got)
		}
		if got := fx.read(t, "b/pb.yaml"); got != ws("example.com/b", "  example.com/m2: v1.0.0\n") {
			t.Fatalf("b/pb.yaml = %q", got)
		}
	})

	t.Run("well-known imports never become dependencies", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/shipper: v1.0.0\n"),
			"a/x.proto": "syntax = \"proto3\";\nimport \"google/protobuf/timestamp.proto\";\nimport \"s.proto\";\n",
		})
		// shipper carries a copy of a well-known file AND the real dep.
		fx.serve(t, "example.com/shipper", "v1.0.0", map[string]string{
			"pb.yaml":                         ws("example.com/shipper", ""),
			"s.proto":                         "syntax = \"proto3\";\n",
			"google/protobuf/timestamp.proto": "syntax = \"proto3\";\n",
		})
		s := fx.session(t, ".")
		if err := Tidy(ctx, s); err != nil {
			t.Fatal(err)
		}
		if got := fx.read(t, "a/pb.yaml"); got != ws("example.com/a", "  example.com/shipper: v1.0.0\n") {
			t.Fatalf("a/pb.yaml = %q (well-known import must not add or drop deps)", got)
		}
	})

	t.Run("self-imports add no dependency", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", ""),
			"a/x.proto": "syntax = \"proto3\";\nimport \"y.proto\";\n",
			"a/y.proto": "syntax = \"proto3\";\n",
		})
		s := fx.session(t, ".")
		if err := Tidy(ctx, s); err != nil {
			t.Fatal(err)
		}
		if got := fx.read(t, "a/pb.yaml"); got != ws("example.com/a", "") {
			t.Fatalf("a/pb.yaml = %q (self-import must add nothing)", got)
		}
	})

	t.Run("ordered imports survive within one file", func(t *testing.T) {
		// One file whose import list mixes a local and an external in
		// declaration order: dropping anything after the first import
		// loses the external dependency.
		fx := newDep(t, map[string]string{
			"pb.work":       "use:\n  - a\n  - lib\n",
			"a/pb.yaml":     ws("example.com/a", "  example.com/lib: v0.1.0\n  example.com/m1: v1.0.0\n"),
			"a/x.proto":     "syntax = \"proto3\";\nimport \"lib.proto\";\nimport \"m1.proto\";\n",
			"lib/pb.yaml":   ws("example.com/lib", ""),
			"lib/lib.proto": "syntax = \"proto3\";\n",
		})
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{
			"pb.yaml":  ws("example.com/m1", ""),
			"m1.proto": "syntax = \"proto3\";\n",
		})
		s := fx.session(t, ".")
		if err := Tidy(ctx, s); err != nil {
			t.Fatal(err)
		}
		want := ws("example.com/a", "  example.com/lib: v0.1.0\n  example.com/m1: v1.0.0\n")
		if got := fx.read(t, "a/pb.yaml"); got != want {
			t.Fatalf("a/pb.yaml = %q, want %q", got, want)
		}
	})

	t.Run("external archive file order cannot drop protos", func(t *testing.T) {
		// pb.yaml sorts between a.proto and z.proto: any early loop exit
		// on the non-proto member loses z.proto's providership.
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - w\n",
			"w/pb.yaml": ws("example.com/w", "  example.com/m1: v1.0.0\n"),
			"w/x.proto": "syntax = \"proto3\";\nimport \"z.proto\";\n",
		})
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{
			"a.proto": "syntax = \"proto3\";\n",
			"pb.yaml": ws("example.com/m1", ""),
			"z.proto": "syntax = \"proto3\";\n",
		})
		s := fx.session(t, ".")
		if err := Tidy(ctx, s); err != nil {
			t.Fatal(err)
		}
		if got := fx.read(t, "w/pb.yaml"); got != ws("example.com/w", "  example.com/m1: v1.0.0\n") {
			t.Fatalf("w/pb.yaml = %q", got)
		}
	})
}

// Update and Verify's remaining arms.
func TestUpdateAndVerifyArms(t *testing.T) {
	t.Run("sweep never consults local declarations", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":     "use:\n  - a\n  - lib\n",
			"a/pb.yaml":   ws("example.com/a", "  example.com/lib: v0.1.0\n"),
			"lib/pb.yaml": ws("example.com/lib", ""),
		})
		// No listing exists for the local path; consulting it would fail.
		s := fx.session(t, ".")
		if err := Update(ctx, s, &bytes.Buffer{}, nil); err != nil {
			t.Fatalf("sweep over a local-only declaration: %v", err)
		}
	})

	t.Run("listing failure fails a named update", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		})
		// No list endpoint and nothing at the origin: Versions errors.
		s := fx.session(t, ".")
		if err := Update(ctx, s, &bytes.Buffer{}, nil, "example.com/m1"); err == nil {
			t.Fatal("a failing listing did not fail update")
		}
	})

	t.Run("sweep survives an early no-release target", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/aaa: v1.0.0\n  example.com/m1: v1.0.0\n"),
		})
		// aaa sorts first and has no releases; m1 must still update.
		fx.Endpoints[fetchtest.ProxyHost+"/example.com/aaa/@v/list"] = []byte("")
		fx.Endpoints[fetchtest.ProxyHost+"/example.com/m1/@v/list"] = []byte("v1.0.0\nv1.2.0\n")
		fx.serve(t, "example.com/aaa", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/aaa", "")})
		fx.serve(t, "example.com/m1", "v1.2.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		s := fx.session(t, ".")
		if err := Update(ctx, s, &bytes.Buffer{}, nil); err != nil {
			t.Fatal(err)
		}
		got := fx.read(t, "a/pb.yaml")
		if !strings.Contains(got, "example.com/m1: v1.2.0") || !strings.Contains(got, "example.com/aaa: v1.0.0") {
			t.Fatalf("a/pb.yaml = %q", got)
		}
	})

	t.Run("sweep survives an early regressed target", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/aaa: v1.0.0\n  example.com/m1: v1.0.0\n"),
		})
		fx.Endpoints[fetchtest.ProxyHost+"/example.com/aaa/@v/list"] = []byte("v0.9.0\n")
		fx.Endpoints[fetchtest.ProxyHost+"/example.com/m1/@v/list"] = []byte("v1.2.0\n")
		fx.serve(t, "example.com/aaa", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/aaa", "")})
		fx.serve(t, "example.com/m1", "v1.2.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		s := fx.session(t, ".")
		if err := Update(ctx, s, &bytes.Buffer{}, nil); err != nil {
			t.Fatal(err)
		}
		got := fx.read(t, "a/pb.yaml")
		if !strings.Contains(got, "example.com/aaa: v1.0.0") || !strings.Contains(got, "example.com/m1: v1.2.0") {
			t.Fatalf("a/pb.yaml = %q", got)
		}
	})

	t.Run("equal highest release is a no-op, named and swept", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		})
		fx.Endpoints[fetchtest.ProxyHost+"/example.com/m1/@v/list"] = []byte("v1.0.0\n")
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		s := fx.session(t, ".")
		var out bytes.Buffer
		if err := Update(ctx, s, &out, nil, "example.com/m1"); err != nil {
			t.Fatalf("equal-version named update: %v", err)
		}
		if out.Len() != 0 {
			t.Fatalf("no-op update produced output: %q", out.String())
		}
	})

	t.Run("no-op sweep touches no module file", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		})
		fx.Endpoints[fetchtest.ProxyHost+"/example.com/m1/@v/list"] = []byte("v1.0.0\n")
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		efs := &fetchtest.ErrFS{Filesystem: fx.ws, FailRenameSfx: "pb.yaml", PutFailAfter: -1}
		s, err := Load(Config{WS: efs, Dir: ".", Client: fx.client("proxy")})
		if err != nil {
			t.Fatal(err)
		}
		if err := Update(ctx, s, &bytes.Buffer{}, nil); err != nil {
			t.Fatalf("no-op sweep wrote a module file: %v", err)
		}
	})

	t.Run("module-file write failure fails update", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		})
		fx.Endpoints[fetchtest.ProxyHost+"/example.com/m1/@v/list"] = []byte("v1.2.0\n")
		fx.serve(t, "example.com/m1", "v1.2.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		efs := &fetchtest.ErrFS{Filesystem: fx.ws, FailRenameSfx: "pb.yaml", PutFailAfter: -1}
		s, err := Load(Config{WS: efs, Dir: ".", Client: fx.client("proxy")})
		if err != nil {
			t.Fatal(err)
		}
		if err := Update(ctx, s, &bytes.Buffer{}, nil, "example.com/m1"); !errors.Is(err, fetchtest.ErrInjected) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("post-update resolution failure surfaces", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		})
		// The listing advertises v2.0.0 but no artifacts exist for it.
		fx.Endpoints[fetchtest.ProxyHost+"/example.com/m1/@v/list"] = []byte("v2.0.0\n")
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		s := fx.session(t, ".")
		if err := Update(ctx, s, &bytes.Buffer{}, nil, "example.com/m1"); err == nil {
			t.Fatal("an unresolvable updated version did not fail")
		}
	})

	t.Run("verify reports every mismatch in sorted order", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n  example.com/m2: v1.0.0\n"),
		})
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		fx.serve(t, "example.com/m2", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m2", "")})
		fx.Endpoint("example.com/m1", "v1.0.0", "info", `{"version":"v1.0.0"}`)
		fx.Endpoint("example.com/m2", "v1.0.0", "info", `{"version":"v1.0.0"}`)
		s := fx.session(t, ".")
		if err := Download(ctx, s, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		wrong, _ := fetchtest.ModuleZip(t, map[string]string{"pb.yaml": ws("example.com/x", "")})
		for _, m := range []string{"example.com/m2", "example.com/m1"} {
			if err := s.Client.Cache.Put(m, mustVer(t, "v1.0.0"), fetch.KindZip, wrong); err != nil {
				t.Fatal(err)
			}
		}
		var out bytes.Buffer
		if err := Verify(ctx, s, &out); err == nil {
			t.Fatal("tampered caches verified")
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "example.com/m1@") || !strings.HasPrefix(lines[1], "example.com/m2@") {
			t.Fatalf("report = %q (want both mismatches, sorted)", out.String())
		}
	})

	t.Run("verify fails closed on an unparsable pinned version", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", ""),
		})
		s := fx.session(t, ".")
		s.Lock.Modules = append(s.Lock.Modules, lockfile.ModulePin{Path: "example.com/x", Version: "garbage"})
		if err := Verify(ctx, s, &bytes.Buffer{}); err == nil {
			t.Fatal("an unparsable pinned version was tolerated")
		}
	})

	t.Run("verify surfaces cache read faults", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		})
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		fx.Endpoint("example.com/m1", "v1.0.0", "info", `{"version":"v1.0.0"}`)
		s := fx.session(t, ".")
		if err := Download(ctx, s, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		s.Client.Cache = &fetch.Cache{FS: &fetchtest.ErrFS{Filesystem: memfs.New(), FailOpen: true, PutFailAfter: -1}}
		if err := Verify(ctx, s, &bytes.Buffer{}); !errors.Is(err, fetchtest.ErrInjected) {
			t.Fatalf("err = %v", err)
		}
	})
}

// The last verb arms: Why's ordering and save, Init's stat fault, and
// the crossing warning attributing the root.
func TestVerbEdgeArms(t *testing.T) {
	t.Run("why continues past a not-needed target", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		})
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		s := fx.session(t, ".")
		var out bytes.Buffer
		if err := Why(ctx, s, &out, "example.com/absent", "example.com/m1"); err != nil {
			t.Fatal(err)
		}
		want := "# example.com/absent\n(module example.com/absent is not needed)\n\n# example.com/m1\nexample.com/a\nexample.com/m1@v1.0.0\n"
		if out.String() != want {
			t.Fatalf("why = %q, want %q", out.String(), want)
		}
	})

	t.Run("why surfaces a failing lockfile save", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		})
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		efs := &fetchtest.ErrFS{Filesystem: fx.ws, FailRenameSfx: "pb.lock", PutFailAfter: -1}
		s, err := Load(Config{WS: efs, Dir: ".", Client: fx.client("proxy")})
		if err != nil {
			t.Fatal(err)
		}
		if err := Why(ctx, s, &bytes.Buffer{}, "example.com/m1"); !errors.Is(err, fetchtest.ErrInjected) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("init stat fault surfaces distinctly", func(t *testing.T) {
		fx := newDep(t, nil)
		efs := &fetchtest.ErrFS{Filesystem: fx.ws, FailStatSuffix: "pb.yaml", PutFailAfter: -1}
		if err := Init(efs, "m", "example.com/m"); !errors.Is(err, fetchtest.ErrInjected) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a crossing whose lowest requirement is the root's names the root", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":   "use:\n  - a\n  - b\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/x: v2.0.0\n"),
			"b/pb.yaml": ws("example.com/b", "  example.com/x: v1.0.0\n"),
		})
		fx.serve(t, "example.com/x", "v2.0.0", map[string]string{"pb.yaml": ws("example.com/x", "")})
		fx.serve(t, "example.com/x", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/x", "")})
		fx.Endpoint("example.com/x", "v2.0.0", "info", `{"version":"v2.0.0"}`)
		fx.Endpoint("example.com/x", "v1.0.0", "info", `{"version":"v1.0.0"}`)
		s := fx.session(t, ".")
		var out bytes.Buffer
		if err := Download(ctx, s, &out); err != nil {
			t.Fatalf("Download: %v", err)
		}
		if !strings.Contains(out.String(), "required by the root") {
			t.Fatalf("output = %q, want the root attribution", out.String())
		}
	})
}

// stubUpdater records the reference an update named, rewrites the
// session's lock as the acquirer would, and answers with a fixed pin.
type stubUpdater struct {
	got   string
	calls int
	lock  *lockfile.File
	after lockfile.PluginPin
}

func (u *stubUpdater) UpdatePlugin(_ context.Context, ref string) (lockfile.PluginPin, lockfile.PluginPin, error) {
	u.got = ref
	u.calls++
	before := lockfile.PluginPin{Ref: ref, Scheme: lockfile.SchemeOCI, Digest: "sha256:" + strings.Repeat("11", 32)}
	after := u.after
	after.Provenance = lockfile.Provenance{Type: lockfile.ProvenanceImageSignature, SAN: "https://ci.example/wf", Issuer: "https://issuer.example"}
	if _, ok := u.lock.Plugin(ref, lockfile.SchemeOCI); !ok {
		if err := u.lock.AddPlugin(before); err != nil {
			return lockfile.PluginPin{}, lockfile.PluginPin{}, err
		}
	}
	if err := u.lock.UpdatePlugin(after); err != nil {
		return lockfile.PluginPin{}, lockfile.PluginPin{}, err
	}
	return before, after, nil
}

// A file whose imports cannot be read fails tidy naming it by a path
// the user can find — a workspace file by its place in the tree, an
// external's by module, version and file — before any satisfaction
// judgement.
func TestTidyNamesAMalformedFile(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", ""),
		"a/x.proto": "syntax = \"proto3\";\nimport \"unterminated\n",
	})
	s := fx.session(t, ".")
	err := Tidy(ctx, s)
	if err == nil || !strings.HasPrefix(err.Error(), "a/x.proto: ") {
		t.Fatalf("a malformed workspace file: %v", err)
	}

	fx = newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		"a/x.proto": "syntax = \"proto3\";\nimport \"m1.proto\";\n",
	})
	fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{
		"pb.yaml":  ws("example.com/m1", ""),
		"m1.proto": "syntax = \"proto3\";\nimport \"unterminated\n",
	})
	err = Tidy(ctx, fx.session(t, "."))
	if err == nil || !strings.HasPrefix(err.Error(), "example.com/m1@v1.0.0: m1.proto: ") {
		t.Fatalf("a malformed external file: %v", err)
	}
}

// Tidy keeps a declared ruleset the lint file names though no import
// uses it — an external at the selected version, a workspace module
// at its declared version; a workspace-module ruleset no module
// declares needs no declaration; an external one that no workspace
// module declares fails naming it, the module file untouched
// (REQ-dep-tidy-rulesets).
func TestTidyKeepsRulesets(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":       "use:\n  - a\n  - b\n  - lib\n",
		"pb.lint.yaml":  "rulesets:\n  - example.com/rules\n  - example.com/lib\n",
		"a/pb.yaml":     ws("example.com/a", "  example.com/rules: v1.0.0\n  example.com/unused: v1.0.0\n  example.com/lib: v0.1.0\n"),
		"b/pb.yaml":     ws("example.com/b", "  example.com/rules: v1.1.0\n"),
		"b/y.proto":     "syntax = \"proto3\";\n",
		"a/x.proto":     "syntax = \"proto3\";\n",
		"lib/pb.yaml":   ws("example.com/lib", ""),
		"lib/lib.proto": "syntax = \"proto3\";\n",
	})
	for _, v := range []string{"v1.0.0", "v1.1.0"} {
		fx.serve(t, "example.com/rules", v, map[string]string{
			"pb.yaml":           ws("example.com/rules", ""),
			"naming.rules.yaml": "celEnv: 1\nrules: []\n",
		})
	}
	fx.serve(t, "example.com/unused", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/unused", ""),
	})
	s := fx.session(t, ".")
	if err := Tidy(ctx, s); err != nil {
		t.Fatalf("Tidy: %v", err)
	}
	// The external ruleset moves to the selected version, b's v1.1.0;
	// the workspace-module ruleset keeps its declared version.
	if got, want := fx.read(t, "a/pb.yaml"), "module: example.com/a\ndeps:\n  example.com/lib: v0.1.0\n  example.com/rules: v1.1.0\n"; got != want {
		t.Fatalf("tidied a/pb.yaml = %q, want %q", got, want)
	}
	if got := fx.read(t, "b/pb.yaml"); got != ws("example.com/b", "  example.com/rules: v1.1.0\n") {
		t.Fatalf("b changed: %q", got)
	}
	// Undeclared: refused, nothing rewritten.
	fx = newDep(t, map[string]string{
		"pb.work":      "use:\n  - a\n",
		"pb.lint.yaml": "rulesets:\n  - example.com/rules\n",
		"a/pb.yaml":    ws("example.com/a", "  example.com/unused: v1.0.0\n"),
		"a/x.proto":    "syntax = \"proto3\";\n",
	})
	fx.serve(t, "example.com/unused", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/unused", "")})
	s = fx.session(t, ".")
	err := Tidy(ctx, s)
	if err == nil || !strings.Contains(err.Error(), "ruleset example.com/rules is no workspace module and no workspace module declares it") {
		t.Fatalf("undeclared ruleset: %v", err)
	}
	if got := fx.read(t, "a/pb.yaml"); got != ws("example.com/a", "  example.com/unused: v1.0.0\n") {
		t.Fatalf("failed tidy rewrote the module file: %q", got)
	}
	// A malformed lint file is named.
	fx = newDep(t, map[string]string{
		"pb.work":      "use:\n  - a\n",
		"pb.lint.yaml": "rulesets: 1\n",
		"a/pb.yaml":    ws("example.com/a", ""),
		"a/x.proto":    "syntax = \"proto3\";\n",
	})
	s = fx.session(t, ".")
	if err := Tidy(ctx, s); err == nil || !errors.Is(err, lintfile.ErrInvalid) || !strings.Contains(err.Error(), "pb.lint.yaml") {
		t.Fatalf("malformed lint file: %v", err)
	}
}

// The check fixture: two workspace modules, a workspace ruleset with
// lint and breaking rules, an external ruleset, a lint file selecting
// and ignoring; b imports a.
func newCheck(t *testing.T, lint string) *depFixture {
	t.Helper()
	fx := newDep(t, map[string]string{
		"pb.work":      "use:\n  - a\n  - b\n  - house\n",
		"pb.lint.yaml": lint,
		"a/pb.yaml":    ws("example.com/a", "  example.com/std: v1.0.0\n"),
		"a/a.proto": "syntax = \"proto3\";\npackage a;\n\nmessage Thing {\n" +
			"  string BadName = 1;\n" +
			"  string Other = 2; // pb:ignore FIELD_NAMES kept for now\n" +
			"  string ok = 3;\n" +
			"}\n",
		"a/vendor/v.proto": "syntax = \"proto3\";\npackage a.vendor;\nmessage V {\n  string Vendored = 1;\n}\n",
		"b/pb.yaml":        ws("example.com/b", "  example.com/a: v0.0.1\n"),
		"b/b.proto":        "syntax = \"proto3\";\npackage b;\nimport \"a.proto\";\nmessage Use {\n  a.Thing thing = 1;\n  string Loud = 2;\n}\n",
		"house/pb.yaml":    ws("example.com/house", ""),
		"house/house.rules.yaml": "celEnv: 1\nrules:\n" +
			"  - id: FIELD_NAMES\n    kind: lint\n    target: field\n    severity: error\n    tags: [naming]\n    cel: case(field.name, 'snake') == field.name\n    message: field names are snake_case\n" +
			"  - id: MESSAGE_COUNT\n    kind: lint\n    target: set\n    severity: warning\n    cel: messages(files).size() < 3\n    message: too many messages\n" +
			"  - id: FIELD_GONE\n    kind: breaking\n    target: field\n    severity: error\n    cel: new != null\n    message: field removed\n" +
			"  - id: FIELD_TYPE\n    kind: breaking\n    target: field\n    severity: warning\n    cel: old == null || new == null || old.type == new.type\n    message: type changed\n",
	})
	fx.serve(t, "example.com/std", "v1.0.0", map[string]string{
		"pb.yaml":        ws("example.com/std", ""),
		"std.rules.yaml": "celEnv: 1\nrules:\n  - id: PACKAGE_DEFINED\n    kind: lint\n    target: file\n    severity: error\n    cel: file.package != ''\n    message: files declare a package\n",
	})
	return fx
}

// pb lint evaluates the enabled lint rules over every checked module,
// prints the findings sorted with the ignored ones dropped, and fails
// on an error finding; a comment suppresses; zero lint rules enabled
// is said on standard error and passes (REQ-check-lint-verb,
// REQ-check-findings-output, REQ-check-exit-status).
func TestLint(t *testing.T) {
	fx := newCheck(t, "rulesets:\n  - example.com/house\n  - example.com/std\nignore:\n  - paths: [\"vendor/**\"]\n")
	s := fx.session(t, ".")
	var out, diag strings.Builder
	err := Lint(ctx, s, &out, &diag)
	if !errors.Is(err, ErrFindings) {
		t.Fatalf("Lint: %v", err)
	}
	want := "a.proto:5:3: error example.com/house:FIELD_NAMES: field names are snake_case\nb.proto:6:3: error example.com/house:FIELD_NAMES: field names are snake_case\nwarning example.com/house:MESSAGE_COUNT: too many messages\n"
	if out.String() != want || diag.String() != "" {
		t.Fatalf("out = %q diag = %q", out.String(), diag.String())
	}
	// The external ruleset's first use is pinned and saved.
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "example.com/std") {
		t.Fatalf("the ruleset was not pinned: %q", lock)
	}
	// Warnings alone pass; a severity override turns the error down.
	fx = newCheck(t, "rulesets:\n  - example.com/house\nenable: [naming]\nseverity:\n  FIELD_NAMES: warning\nignore:\n  - paths: [\"vendor/**\"]\n")
	out.Reset()
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err != nil || !strings.Contains(out.String(), "warning example.com/house:FIELD_NAMES") || strings.Contains(out.String(), "MESSAGE_COUNT") {
		t.Fatalf("warnings: %v %q", err, out.String())
	}
	// Zero rules enabled.
	fx = newCheck(t, "")
	out.Reset()
	diag.Reset()
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err != nil || out.String() != "" || diag.String() != "pb lint: zero lint rules enabled\n" {
		t.Fatalf("zero rules: %v %q %q", err, out.String(), diag.String())
	}
	// A build list that pins one dependency and fails on another keeps
	// the pin: the record survives the failure (REQ-lock-first-use).
	fx = newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/std: v1.0.0\n  example.com/zzz: v1.0.0\n"),
		"a/a.proto": "syntax = \"proto3\";\npackage a;\n",
	})
	fx.serve(t, "example.com/std", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/std", "")})
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err == nil || !strings.Contains(err.Error(), "example.com/zzz") {
		t.Fatalf("unserved dependency: %v", err)
	}
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "example.com/std") {
		t.Fatalf("the served dependency's pin was lost with the failure: %q", lock)
	}
	// A ruleset the lint file names that no module declares fails.
	fx = newCheck(t, "rulesets:\n  - example.com/nowhere\n")
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err == nil || !strings.Contains(err.Error(), "ruleset example.com/nowhere") {
		t.Fatalf("undeclared ruleset: %v", err)
	}
}

// pb breaking materializes each module's base in the lint file's
// form, pairs it with the checked schema, evaluates the breaking
// rules, and reports every module's findings as one stream, base
// findings marked; a version base is pinned; no base configured fails
// naming the file; zero breaking rules is said on standard error
// (REQ-check-breaking-verb, REQ-break-base).
func TestBreaking(t *testing.T) {
	fx := newCheck(t, "rulesets:\n  - example.com/house\nbreaking:\n  base:\n    version: v0.9.0\n")
	// The bases: a had a field since removed and a field whose type
	// changed; b had a field since removed.
	fx.serve(t, "example.com/a", "v0.9.0", map[string]string{
		"pb.yaml": ws("example.com/a", ""),
		"a.proto": "syntax = \"proto3\";\npackage a;\n\nmessage Thing {\n  string BadName = 1;\n  string Other = 2;\n  int32 ok = 3;\n  string gone = 4;\n}\n",
	})
	fx.serve(t, "example.com/b", "v0.9.0", map[string]string{
		"pb.yaml": ws("example.com/b", "  example.com/a: v0.0.1\n"),
		"b.proto": "syntax = \"proto3\";\npackage b;\nimport \"a.proto\";\nmessage Use {\n  a.Thing thing = 1;\n  string Loud = 2;\n  string dropped = 3;\n}\n",
	})
	// house holds no protobuf files: nothing to pair, no base needed,
	// none served.
	s := fx.session(t, ".")
	var out, diag strings.Builder
	err := Breaking(ctx, s, BreakingDeps{}, &out, &diag)
	if !errors.Is(err, ErrFindings) {
		t.Fatalf("Breaking: %v", err)
	}
	want := "a.proto:7:3: warning example.com/house:FIELD_TYPE: type changed\na.proto:8:3: error example.com/house:FIELD_GONE: field removed [base]\nb.proto:7:3: error example.com/house:FIELD_GONE: field removed [base]\n"
	if out.String() != want || diag.String() != "" {
		t.Fatalf("out = %q diag = %q", out.String(), diag.String())
	}
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "example.com/a") || !strings.Contains(lock, "v0.9.0") {
		t.Fatalf("the version base was not pinned: %q", lock)
	}
	// No base configured.
	fx = newCheck(t, "rulesets:\n  - example.com/house\n")
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); err == nil || !strings.Contains(err.Error(), "names no breaking base") {
		t.Fatalf("no base: %v", err)
	}
	// Zero breaking rules.
	fx = newCheck(t, "rulesets:\n  - example.com/std\nbreaking:\n  base:\n    pinned: true\n")
	out.Reset()
	diag.Reset()
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); err != nil || diag.String() != "pb breaking: zero breaking rules enabled\n" {
		t.Fatalf("zero rules: %v %q", err, diag.String())
	}
	// A base the origin does not serve fails naming the module and the
	// form; a base pinned before the failure stays pinned.
	fx = newCheck(t, "rulesets:\n  - example.com/house\nbreaking:\n  base:\n    version: v0.8.0\n")
	fx.serve(t, "example.com/a", "v0.8.0", map[string]string{"pb.yaml": ws("example.com/a", ""), "a.proto": "syntax = \"proto3\";\npackage a;\nmessage Thing {}\n"})
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); err == nil || !strings.Contains(err.Error(), "breaking: example.com/b: breaking base version v0.8.0") {
		t.Fatalf("unserved base: %v", err)
	}
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "v0.8.0") {
		t.Fatalf("a's base pin lost on b's failure: %q", lock)
	}
	// The base compiles with the build's other modules resolving its
	// imports and none other a target: a's base lacking a message b
	// uses now is a's base still; an ignore drops a base finding.
	fx = newCheck(t, "rulesets:\n  - example.com/house\nbreaking:\n  base:\n    version: v0.7.0\nignore:\n  - paths: [\"b.proto\"]\n    rules: [FIELD_GONE]\n")
	fx.serve(t, "example.com/a", "v0.7.0", map[string]string{"pb.yaml": ws("example.com/a", ""), "a.proto": "syntax = \"proto3\";\npackage a;\nmessage Former {\n  string gone = 1;\n}\n"})
	fx.serve(t, "example.com/b", "v0.7.0", map[string]string{"pb.yaml": ws("example.com/b", "  example.com/a: v0.0.1\n"), "b.proto": "syntax = \"proto3\";\npackage b;\nmessage Use {\n  string Loud = 2;\n  string dropped = 3;\n}\n"})
	out.Reset()
	err = Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag)
	if !errors.Is(err, ErrFindings) || out.String() != "a.proto:4:3: error example.com/house:FIELD_GONE: field removed [base]\n" {
		t.Fatalf("base lacking what b uses: %v %q", err, out.String())
	}
	// A file a gained after its base, imported by b now: a's base run
	// checks a's imports alone, b's being no requirer of it.
	fx = newCheck(t, "rulesets:\n  - example.com/house\nbreaking:\n  base:\n    version: v0.7.0\n")
	fx.serve(t, "example.com/a", "v0.7.0", map[string]string{"pb.yaml": ws("example.com/a", ""), "a.proto": "syntax = \"proto3\";\npackage a;\nmessage Thing {\n  string BadName = 1;\n}\n"})
	fx.serve(t, "example.com/b", "v0.7.0", map[string]string{"pb.yaml": ws("example.com/b", "  example.com/a: v0.0.1\n"), "b.proto": "syntax = \"proto3\";\npackage b;\nimport \"a.proto\";\nmessage Use {\n  a.Thing thing = 1;\n}\n"})
	fx.write(t, "b/b.proto", "syntax = \"proto3\";\npackage b;\nimport \"a.proto\";\nimport \"vendor/v.proto\";\nmessage Use {\n  a.Thing thing = 1;\n  a.vendor.V v = 2;\n}\n")
	out.Reset()
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); err != nil || out.String() != "" {
		t.Fatalf("a's base with b importing a's newer file: %v %q", err, out.String())
	}
	// A reference base with no repository source wired fails naming
	// it.
	fx = newCheck(t, "rulesets:\n  - example.com/house\nbreaking:\n  base:\n    ref: HEAD\n")
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); err == nil || !strings.Contains(err.Error(), "no repository source is wired") {
		t.Fatalf("no repo source: %v", err)
	}
}

// refusingFS refuses every write: what a resolution root on read-only
// storage looks like to SaveLock.
type refusingFS struct{ billy.Filesystem }

func (refusingFS) Create(string) (billy.File, error) { return nil, errors.New("read-only") }
func (refusingFS) OpenFile(string, int, os.FileMode) (billy.File, error) {
	return nil, errors.New("read-only")
}
func (refusingFS) TempFile(string, string) (billy.File, error) { return nil, errors.New("read-only") }
func (refusingFS) Rename(string, string) error                 { return errors.New("read-only") }

// Pins are saved after a step whatever its outcome, and a save that
// fails never hides the step's own cause (REQ-lock-first-use).
func TestSavePins(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", ""),
	})
	s := fx.session(t, ".")
	if err := savePins(s, nil); err != nil {
		t.Fatalf("nothing to save: %v", err)
	}
	if err := savePins(s, errors.New("step failed")); err == nil || err.Error() != "step failed" {
		t.Fatalf("the step's error alone: %v", err)
	}
	s.Lock.Modules = append(s.Lock.Modules, lockfile.ModulePin{Path: "example.com/x", Version: "v1.0.0", Digest: "pb1:" + strings.Repeat("ab", 32)})
	s.WS = refusingFS{s.WS}
	err := savePins(s, nil)
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("the save's error alone: %v", err)
	}
	err = savePins(s, errors.New("step failed"))
	if err == nil || !strings.HasPrefix(err.Error(), "step failed (and the lockfile could not be saved: ") {
		t.Fatalf("both: %v", err)
	}
}
