package dep

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/osfs"
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

// exists reports whether a file lies at name in the tree.
func (fx *depFixture) exists(t *testing.T, name string) bool {
	t.Helper()
	_, err := fx.ws.Stat(name)
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, fs.ErrNotExist) {
		return false
	}
	t.Fatal(err)
	return false
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
	// The lint file imports a ruleset the build never needs, and the
	// workspace module a (fetched by nothing).
	fx := newDep(t, map[string]string{
		"pb.work":      "use:\n  - a\n",
		"pb.lint.yaml": "rulesets:\n  - path: example.com/rules\n    version: v1.0.0\n    alias: rules\n  - path: example.com/a\n    alias: a\n",
		"a/pb.yaml":    ws("example.com/a", "  example.com/m1: v1.0.0\n"),
	})
	fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m1", "  example.com/m2: v1.0.0\n"),
	})
	fx.serve(t, "example.com/m2", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m2", "")})
	// The ruleset's rule file imports a ruleset of its own, fetched
	// and pinned too, after the import that reached it.
	fx.serve(t, "example.com/rules", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/rules", ""), "r.rules.yaml": "celEnv: 1\nimports:\n  - path: example.com/fns\n    version: v0.1.0\n    alias: fns\nrules: []\n"})
	fx.serve(t, "example.com/fns", "v0.1.0", map[string]string{"pb.yaml": ws("example.com/fns", ""), "f.rules.yaml": "celEnv: 1\nfunctions:\n  - name: yes\n    returns: bool\n    cel: \"true\"\nrules: []\n"})
	fx.Endpoint("example.com/m1", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	fx.Endpoint("example.com/m2", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	fx.Endpoint("example.com/rules", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	fx.Endpoint("example.com/fns", "v0.1.0", "info", `{"version":"v0.1.0"}`)

	s := fx.session(t, ".")
	var out bytes.Buffer
	if err := Download(ctx, s, &out); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := out.String(); got != "example.com/m1@v1.0.0\nexample.com/m2@v1.0.0\nexample.com/rules@v1.0.0\nexample.com/fns@v0.1.0\n" {
		t.Fatalf("output = %q", got)
	}
	lockBytes := fx.read(t, "pb.lock")
	if !strings.Contains(lockBytes, "example.com/m1") || !strings.Contains(lockBytes, "example.com/m2") || !strings.Contains(lockBytes, "rulesets:\n  - path: example.com/fns\n    version: v0.1.0\n") || !strings.Contains(lockBytes, "  - path: example.com/rules\n    version: v1.0.0\n") || strings.Contains(lockBytes, "modules:\n  - path: example.com/rules") {
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
	// A path-replaced import's line names both, as a declaration's
	// does; the pin made before a later import fails is saved.
	fx.write(t, "pb.work", "use:\n  - a\nreplace:\n  example.com/rules: example.com/fork@v2.0.0\n")
	fx.write(t, "pb.lint.yaml", "rulesets:\n  - path: example.com/rules\n    version: v1.0.0\n    alias: rules\n")
	fx.serve(t, "example.com/fork", "v2.0.0", map[string]string{"pb.yaml": ws("example.com/fork", "")})
	fx.Endpoint("example.com/fork", "v2.0.0", "info", `{"version":"v2.0.0"}`)
	out.Reset()
	if err := Download(ctx, fx.session(t, "."), &out); err != nil || !strings.HasSuffix(out.String(), "example.com/rules@v1.0.0 => example.com/fork@v2.0.0\n") {
		t.Fatalf("a replaced import: %v %q", err, out.String())
	}
	// An import that does not resolve fails the run, the pins made on
	// the way saved.
	fx.write(t, "pb.lock", "version: 1\nmodules: []\n")
	fx.write(t, "pb.lint.yaml", "rulesets:\n  - path: example.com/rules\n    version: v1.0.0\n    alias: rules\n  - path: example.com/gone\n    version: v1.0.0\n    alias: gone\n")
	if err := Download(ctx, fx.session(t, "."), &out); err == nil || !strings.Contains(err.Error(), "ruleset example.com/gone@v1.0.0") {
		t.Fatalf("an import that does not resolve: %v", err)
	}
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "rulesets:\n  - path: example.com/fork\n    version: v2.0.0\n") {
		t.Fatalf("the replaced import's pin was not saved before the failure: %q", lock)
	}
}

// Graph prints one edge per line in the spec's format; Why prints
// shortest chains and not-needed answers, sharing one graph
// computation (REQ-dep-graph, REQ-dep-why).
func TestGraphAndWhyVerbs(t *testing.T) {
	// The lint file's imports are edges from the lint file: a fetched
	// import to its pair, a workspace module's to the bare path.
	fx := newDep(t, map[string]string{
		"pb.work":       "use:\n  - a\n  - house\n",
		"pb.lint.yaml":  "rulesets:\n  - path: example.com/rules\n    version: v1.0.0\n    alias: rules\n  - path: example.com/house\n    alias: house\n",
		"a/pb.yaml":     ws("example.com/a", "  example.com/m1: v1.0.0\n"),
		"house/pb.yaml": ws("example.com/house", ""),
	})
	fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/m1", "  example.com/m2: v1.0.0\n"),
	})
	fx.serve(t, "example.com/m2", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m2", "")})
	// The imported ruleset's rule file imports a ruleset of its own for
	// its functions: an edge from the ruleset's pair.
	fx.serve(t, "example.com/rules", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/rules", ""), "r.rules.yaml": "celEnv: 1\nimports:\n  - path: example.com/fns\n    version: v0.1.0\n    alias: fns\nrules: []\n"})
	fx.serve(t, "example.com/fns", "v0.1.0", map[string]string{"pb.yaml": ws("example.com/fns", ""), "f.rules.yaml": "celEnv: 1\nfunctions:\n  - name: yes\n    returns: bool\n    cel: \"true\"\nrules: []\n"})

	s := fx.session(t, ".")
	var out bytes.Buffer
	if err := Graph(ctx, s, &out); err != nil {
		t.Fatal(err)
	}
	want := "example.com/a example.com/m1@v1.0.0\nexample.com/m1@v1.0.0 example.com/m2@v1.0.0\nexample.com/rules@v1.0.0 example.com/fns@v0.1.0\npb.lint.yaml example.com/house\npb.lint.yaml example.com/rules@v1.0.0\n"
	if out.String() != want {
		t.Fatalf("graph = %q, want %q", out.String(), want)
	}

	out.Reset()
	if err := Why(ctx, s, &out, "example.com/m2", "example.com/absent", "example.com/rules", "example.com/house"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "example.com/a\nexample.com/m1@v1.0.0\nexample.com/m2@v1.0.0\n") {
		t.Fatalf("why output = %q", got)
	}
	if !strings.Contains(got, "(module example.com/absent is not needed)") {
		t.Fatalf("why output = %q", got)
	}
	if !strings.Contains(got, "# example.com/rules\npb.lint.yaml\nexample.com/rules@v1.0.0\n") || !strings.Contains(got, "# example.com/house\npb.lint.yaml\nexample.com/house\n") {
		t.Fatalf("why over the import edges = %q", got)
	}
	out.Reset()
	if err := Why(ctx, s, &out, "example.com/fns"); err != nil || out.String() != "# example.com/fns\npb.lint.yaml\nexample.com/rules@v1.0.0\nexample.com/fns@v0.1.0\n" {
		t.Fatalf("why over a rule file's import: %v %q", err, out.String())
	}
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "  - path: example.com/fns\n    version: v0.1.0\n") {
		t.Fatalf("a rule file's import pinned as a ruleset: %q", lock)
	}
	// Edges sort by the printed line, a path that prefixes a sibling's
	// ordered as its spelling is (REQ-dep-graph); a directory-replaced
	// import is a working-tree edge, and why prefers the shorter chain
	// to it over the pairs' (REQ-dep-why).
	fx2 := newDep(t, map[string]string{
		"pb.work":      "use:\n  - a\nreplace:\n  example.com/x: ./fork\n",
		"fork/pb.yaml": ws("example.com/x", ""),
		"pb.lint.yaml": "rulesets:\n  - path: example.com/x\n    alias: x\n",
		"a/pb.yaml":    ws("example.com/a", "  example.com/m1: v1.0.0\n  example.com/m1/v2: v2.0.0\n"),
	})
	fx2.serve(t, "example.com/m1/v2", "v2.0.0", map[string]string{"pb.yaml": ws("example.com/m1/v2", "")})
	fx2.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", "  example.com/m2: v1.0.0\n  example.com/x: v1.0.0\n")})
	fx2.serve(t, "example.com/m2", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m2", "")})
	fx2.serve(t, "example.com/x", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/x", "")})
	out.Reset()
	if err := Graph(ctx, fx2.session(t, "."), &out); err != nil || out.String() != "example.com/a example.com/m1/v2@v2.0.0\nexample.com/a example.com/m1@v1.0.0\nexample.com/m1@v1.0.0 example.com/m2@v1.0.0\nexample.com/m1@v1.0.0 example.com/x@v1.0.0\npb.lint.yaml example.com/x\n" {
		t.Fatalf("graph with a prefix sibling and a replaced import: %v %q", err, out.String())
	}
	out.Reset()
	if err := Why(ctx, fx2.session(t, "."), &out, "example.com/x"); err != nil || out.String() != "# example.com/x\npb.lint.yaml\nexample.com/x\nexample.com/x => ./fork\n" {
		t.Fatalf("why prefers the shorter import chain: %v %q", err, out.String())
	}
	// An import the check run would refuse is refused here too.
	fx.write(t, "pb.lint.yaml", "rulesets:\n  - path: example.com/house\n    version: v1.0.0\n    alias: house\n")
	if err := Graph(ctx, fx.session(t, "."), &out); err == nil || !strings.Contains(err.Error(), "ruleset example.com/house: a workspace module, read from the working tree: write no version (v1.0.0 written)") {
		t.Fatalf("a working-tree import with a version: %v", err)
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
	if err := Tidy(ctx, s, io.Discard); err != nil {
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
	if err := Tidy(ctx, s2, io.Discard); err != nil {
		t.Fatalf("second Tidy: %v", err)
	}
	if fx.read(t, "pb.lock") != lock1 || fx.read(t, "a/pb.yaml") != mod1 {
		t.Fatal("tidy is not idempotent")
	}
}

// A synthesized module declares nothing, so the workspace module
// declaring it carries what its files import (REQ-dep-tidy): a
// declared synthesized module importing another external keeps that
// external declared though no workspace file imports it, to closure
// through a synthesized module the first imports; tidy is idempotent
// over the shape; and the external the synthesized module needs must
// already be in the build list — tidy invents none.
func TestTidyCarriesSynthesizedNeeds(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/s: v1.0.0\n  example.com/t: v1.0.0\n  example.com/u: v1.0.0\n"),
		"a/x.proto": "syntax = \"proto3\";\nimport \"s.proto\";\n",
	})
	fx.serve(t, "example.com/s", "v1.0.0", map[string]string{
		"s.proto": "syntax = \"proto3\";\nimport \"t.proto\";\n",
	})
	fx.serve(t, "example.com/t", "v1.0.0", map[string]string{
		"t.proto": "syntax = \"proto3\";\nimport \"u.proto\";\n",
	})
	fx.serve(t, "example.com/u", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/u", ""),
		"u.proto": "syntax = \"proto3\";\n",
	})
	s := fx.session(t, ".")
	if err := Tidy(ctx, s, io.Discard); err != nil {
		t.Fatalf("Tidy: %v", err)
	}
	want := "module: example.com/a\ndeps:\n  example.com/s: v1.0.0\n  example.com/t: v1.0.0\n  example.com/u: v1.0.0\n"
	if got := fx.read(t, "a/pb.yaml"); got != want {
		t.Fatalf("tidied a/pb.yaml = %q, want %q", got, want)
	}
	s2 := fx.session(t, ".")
	s2.Client.Cache = s.Client.Cache
	if err := Tidy(ctx, s2, io.Discard); err != nil {
		t.Fatalf("second Tidy: %v", err)
	}
	if got := fx.read(t, "a/pb.yaml"); got != want {
		t.Fatalf("tidy is not idempotent: %q", got)
	}

	// A declaring external carries its own needs as the graph's edges:
	// its imports are not carried by the workspace module, which keeps
	// exactly what its own files and the synthesized modules it
	// declares import.
	fx4 := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/d: v1.0.0\n"),
		"a/x.proto": "syntax = \"proto3\";\nimport \"d.proto\";\n",
	})
	fx4.serve(t, "example.com/d", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/d", "  example.com/e: v1.0.0\n"),
		"d.proto": "syntax = \"proto3\";\nimport \"e.proto\";\n",
	})
	fx4.serve(t, "example.com/e", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/e", ""),
		"e.proto": "syntax = \"proto3\";\n",
	})
	if err := Tidy(ctx, fx4.session(t, "."), io.Discard); err != nil {
		t.Fatalf("Tidy over a declaring external: %v", err)
	}
	if got := fx4.read(t, "a/pb.yaml"); got != ws("example.com/a", "  example.com/d: v1.0.0\n") {
		t.Fatalf("a declaring external's needs carried: %q", got)
	}

	// A synthesized module importing a workspace module the declaring
	// module gives no version for: the error names the module that
	// must declare it and the one whose files import it.
	fx3 := newDep(t, map[string]string{
		"pb.work":       "use:\n  - a\n  - lib\n",
		"a/pb.yaml":     ws("example.com/a", "  example.com/s: v1.0.0\n"),
		"a/x.proto":     "syntax = \"proto3\";\nimport \"s.proto\";\n",
		"lib/pb.yaml":   ws("example.com/lib", ""),
		"lib/lib.proto": "syntax = \"proto3\";\n",
	})
	fx3.serve(t, "example.com/s", "v1.0.0", map[string]string{
		"s.proto": "syntax = \"proto3\";\nimport \"lib.proto\";\n",
	})
	if err := Tidy(ctx, fx3.session(t, "."), io.Discard); err == nil || !strings.Contains(err.Error(), "example.com/a needs example.com/s, whose files import workspace module example.com/lib") {
		t.Fatalf("err = %v, want the carried local import named", err)
	}

	// The synthesized module's need not declared: unsatisfied, never
	// invented.
	fx2 := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/s: v1.0.0\n"),
		"a/x.proto": "syntax = \"proto3\";\nimport \"s.proto\";\n",
	})
	fx2.serve(t, "example.com/s", "v1.0.0", map[string]string{
		"s.proto": "syntax = \"proto3\";\nimport \"t.proto\";\n",
	})
	var ue *importcheck.UnsatisfiedError
	if err := Tidy(ctx, fx2.session(t, "."), io.Discard); !errors.As(err, &ue) || len(ue.Unsatisfied) != 1 || ue.Unsatisfied[0].Module != "example.com/s" {
		t.Fatalf("err = %v, want s's import of t unsatisfied", err)
	}
}

// A declaring external whose files import beyond its declarations
// has its gap carried by the consumer: the consumer's declaration of
// the provider is kept, reported naming the external, transitively
// through a declared dependency's own gap; a fully declaring external
// is carried for nothing; the gap left undeclared stays unsatisfied,
// never invented (REQ-dep-tidy).
func TestTidyCarriesAnUnderDeclaringExternal(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/d: v1.0.0\n  example.com/e: v1.0.0\n  example.com/g: v1.0.0\n  example.com/i: v1.0.0\n"),
		"a/x.proto": "syntax = \"proto3\";\nimport \"d.proto\";\n",
	})
	// d declares f and h and imports e's file; f imports g's file and
	// declares nothing of it; h, declared by d and imported by no
	// file, imports i's file undeclared: a and only a can carry e, g
	// and i, the last reached by declaration alone.
	fx.serve(t, "example.com/d", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/d", "  example.com/f: v1.0.0\n  example.com/h: v1.0.0\n"),
		"d.proto": "syntax = \"proto3\";\nimport \"e.proto\";\nimport \"f.proto\";\n",
	})
	fx.serve(t, "example.com/h", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/h", ""), "h.proto": "syntax = \"proto3\";\nimport \"i.proto\";\n"})
	fx.serve(t, "example.com/i", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/i", ""), "i.proto": "syntax = \"proto3\";\n"})
	fx.serve(t, "example.com/e", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/e", ""), "e.proto": "syntax = \"proto3\";\n"})
	fx.serve(t, "example.com/f", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/f", ""), "f.proto": "syntax = \"proto3\";\nimport \"g.proto\";\n"})
	fx.serve(t, "example.com/g", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/g", ""), "g.proto": "syntax = \"proto3\";\n"})
	s := fx.session(t, ".")
	var out strings.Builder
	if err := Tidy(ctx, s, &out); err != nil {
		t.Fatalf("Tidy: %v", err)
	}
	want := ws("example.com/a", "  example.com/d: v1.0.0\n  example.com/e: v1.0.0\n  example.com/g: v1.0.0\n  example.com/i: v1.0.0\n")
	if got := fx.read(t, "a/pb.yaml"); got != want {
		t.Fatalf("tidied a/pb.yaml = %q, want %q", got, want)
	}
	wantOut := "example.com/a carries example.com/e for example.com/d@v1.0.0, whose files import it undeclared\nexample.com/a carries example.com/g for example.com/f@v1.0.0, whose files import it undeclared\nexample.com/a carries example.com/i for example.com/h@v1.0.0, whose files import it undeclared\n"
	if out.String() != wantOut {
		t.Fatalf("report = %q, want %q", out.String(), wantOut)
	}
	// Idempotent, the report the same.
	out.Reset()
	s2 := fx.session(t, ".")
	s2.Client.Cache = s.Client.Cache
	if err := Tidy(ctx, s2, &out); err != nil || fx.read(t, "a/pb.yaml") != want || out.String() != wantOut {
		t.Fatalf("second tidy: %v %q %q", err, fx.read(t, "a/pb.yaml"), out.String())
	}
	// Version skew: the graph carries an older d's edges, which
	// declared e; the selected d does not, so a carries e all the
	// same.
	fxv := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/d: v1.1.0\n  example.com/e: v1.0.0\n  example.com/x: v1.0.0\n"),
		"a/x.proto": "syntax = \"proto3\";\nimport \"d.proto\";\nimport \"xx.proto\";\n",
	})
	fxv.serve(t, "example.com/x", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/x", "  example.com/d: v1.0.0\n"), "xx.proto": "syntax = \"proto3\";\n"})
	fxv.serve(t, "example.com/d", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/d", "  example.com/e: v1.0.0\n"), "d.proto": "syntax = \"proto3\";\nimport \"e.proto\";\n"})
	fxv.serve(t, "example.com/d", "v1.1.0", map[string]string{"pb.yaml": ws("example.com/d", ""), "d.proto": "syntax = \"proto3\";\nimport \"e.proto\";\n"})
	fxv.serve(t, "example.com/e", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/e", ""), "e.proto": "syntax = \"proto3\";\n"})
	out.Reset()
	if err := Tidy(ctx, fxv.session(t, "."), &out); err != nil {
		t.Fatalf("Tidy under version skew: %v", err)
	}
	if got := fxv.read(t, "a/pb.yaml"); got != ws("example.com/a", "  example.com/d: v1.1.0\n  example.com/e: v1.0.0\n  example.com/x: v1.0.0\n") || out.String() != "example.com/a carries example.com/e for example.com/d@v1.1.0, whose files import it undeclared\n" {
		t.Fatalf("version skew: %q %q", got, out.String())
	}
	// Two modules needing one declaration are each named, in one
	// order, and a round that rewrites reports once.
	fxt := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/d: v1.0.0\n  example.com/e: v1.0.0\n  example.com/k: v1.0.0\n  example.com/z: v1.0.0\n"),
		"a/x.proto": "syntax = \"proto3\";\nimport \"d.proto\";\n",
		"a/y.proto": "syntax = \"proto3\";\nimport \"k.proto\";\n",
	})
	fxt.serve(t, "example.com/d", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/d", ""), "d.proto": "syntax = \"proto3\";\nimport \"e.proto\";\n"})
	fxt.serve(t, "example.com/k", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/k", ""), "k.proto": "syntax = \"proto3\";\nimport \"e.proto\";\n"})
	fxt.serve(t, "example.com/e", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/e", ""), "e.proto": "syntax = \"proto3\";\n"})
	fxt.serve(t, "example.com/z", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/z", ""), "z.proto": "syntax = \"proto3\";\n"})
	out.Reset()
	if err := Tidy(ctx, fxt.session(t, "."), &out); err != nil {
		t.Fatalf("Tidy over two needs: %v", err)
	}
	wantTwo := "example.com/a carries example.com/e for example.com/d@v1.0.0, whose files import it undeclared\nexample.com/a carries example.com/e for example.com/k@v1.0.0, whose files import it undeclared\n"
	if got := fxt.read(t, "a/pb.yaml"); got != ws("example.com/a", "  example.com/d: v1.0.0\n  example.com/e: v1.0.0\n  example.com/k: v1.0.0\n") || out.String() != wantTwo {
		t.Fatalf("two needs: %q %q", got, out.String())
	}
	// The gap not declared by the consumer either: unsatisfied.
	fx2 := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/d: v1.0.0\n"),
		"a/x.proto": "syntax = \"proto3\";\nimport \"d.proto\";\n",
	})
	fx2.serve(t, "example.com/d", "v1.0.0", map[string]string{
		"pb.yaml": ws("example.com/d", ""),
		"d.proto": "syntax = \"proto3\";\nimport \"e.proto\";\n",
	})
	var ue *importcheck.UnsatisfiedError
	if err := Tidy(ctx, fx2.session(t, "."), io.Discard); !errors.As(err, &ue) || len(ue.Unsatisfied) != 1 || ue.Unsatisfied[0].Module != "example.com/d" {
		t.Fatalf("err = %v, want d's import of e unsatisfied", err)
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
	err := Tidy(ctx, s, io.Discard)
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
	if err := Tidy(ctx, s, io.Discard); err == nil ||
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

	t.Run("imports move with the sweep and by name", func(t *testing.T) {
		withLint := map[string]string{
			"pb.work":      "use:\n  - a\n",
			"pb.lint.yaml": "rulesets:\n  - path: example.com/m1\n    version: v1.0.0\n    alias: one\n",
			"a/pb.yaml":    ws("example.com/a", ""),
		}
		fx := newDep(t, withLint)
		serve(fx)
		s := fx.session(t, ".")
		var out bytes.Buffer
		if err := Update(ctx, s, &out, nil); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got := fx.read(t, "pb.lint.yaml"); got != "rulesets:\n  - path: example.com/m1\n    version: v1.2.0\n    alias: one\n" {
			t.Fatalf("the lint file after the sweep: %q", got)
		}
		if !strings.Contains(out.String(), "pb.lint.yaml: example.com/m1 v1.0.0 -> v1.2.0\n") || !strings.Contains(fx.read(t, "pb.lock"), "rulesets:\n  - path: example.com/m1\n    version: v1.2.0\n") {
			t.Fatalf("out = %q lock = %q", out.String(), fx.read(t, "pb.lock"))
		}
		// Named: the path no module requires is the lint file's import.
		fx = newDep(t, withLint)
		serve(fx)
		if err := Update(ctx, fx.session(t, "."), &bytes.Buffer{}, nil, "example.com/m1"); err != nil || !strings.Contains(fx.read(t, "pb.lint.yaml"), "version: v1.2.0") {
			t.Fatalf("named import: %v %q", err, fx.read(t, "pb.lint.yaml"))
		}
		if err := Update(ctx, fx.session(t, "."), &bytes.Buffer{}, nil, "example.com/none"); err == nil || !strings.Contains(err.Error(), "no workspace module requires example.com/none, and the lint file imports it not") {
			t.Fatalf("a name nothing declares or imports: %v", err)
		}
		// Of two imports of one path, the higher moves and the lower is
		// left with a report — it would read the same release under a
		// second alias; naming the path fails before anything moves.
		fx = newDep(t, withLint)
		serve(fx)
		fx.serve(t, "example.com/m1", "v1.1.0", map[string]string{"pb.yaml": ws("example.com/m1", "")})
		fx.Endpoints[fetchtest.ProxyHost+"/example.com/m1/@v/list"] = []byte("v1.0.0\nv1.1.0\nv1.2.0\n")
		two := "rulesets:\n  - path: example.com/m1\n    version: v1.0.0\n    alias: one\n  - path: example.com/m1\n    version: v1.1.0\n    alias: two\n"
		fx.write(t, "pb.lint.yaml", two)
		out.Reset()
		if err := Update(ctx, fx.session(t, "."), &out, nil); err != nil || out.String() != "pb.lint.yaml: example.com/m1 v1.1.0 -> v1.2.0\npb.lint.yaml: example.com/m1 v1.0.0 left: v1.2.0 is read as two already\n" || fx.read(t, "pb.lint.yaml") != strings.Replace(two, "v1.1.0", "v1.2.0", 1) {
			t.Fatalf("two imports, one moving: %v %q %q", err, out.String(), fx.read(t, "pb.lint.yaml"))
		}
		// One already at the highest release: the other is left too.
		atHighest := strings.Replace(two, "v1.1.0", "v1.2.0", 1)
		out.Reset()
		if err := Update(ctx, fx.session(t, "."), &out, nil); err != nil || out.String() != "pb.lint.yaml: example.com/m1 v1.0.0 left: v1.2.0 is read as two already\n" || fx.read(t, "pb.lint.yaml") != atHighest {
			t.Fatalf("one import at the highest release: %v %q %q", err, out.String(), fx.read(t, "pb.lint.yaml"))
		}
		if err := Update(ctx, fx.session(t, "."), &bytes.Buffer{}, nil, "example.com/m1"); err == nil || !strings.Contains(err.Error(), "example.com/m1 is imported as two and as one; the highest discovered release v1.2.0 would be read under both") || fx.read(t, "pb.lint.yaml") != atHighest {
			t.Fatalf("named, two imports: %v %q", err, fx.read(t, "pb.lint.yaml"))
		}
		// A regressed origin holds the higher import where it is, and a
		// lower one moves: the two read different pairs.
		fx.write(t, "pb.lint.yaml", "rulesets:\n  - path: example.com/m1\n    version: v1.3.0\n    alias: one\n  - path: example.com/m1\n    version: v1.0.0\n    alias: two\n")
		out.Reset()
		if err := Update(ctx, fx.session(t, "."), &out, nil); err != nil || out.String() != "pb.lint.yaml: example.com/m1 v1.0.0 -> v1.2.0\n" || !strings.Contains(fx.read(t, "pb.lint.yaml"), "version: v1.3.0\n    alias: one\n  - path: example.com/m1\n    version: v1.2.0\n    alias: two\n") {
			t.Fatalf("a regressed origin beside a lower import: %v %q %q", err, out.String(), fx.read(t, "pb.lint.yaml"))
		}
		// A named import above the highest discovered release is an
		// origin that regressed.
		fx.write(t, "pb.lint.yaml", "rulesets:\n  - path: example.com/m1\n    version: v1.3.0\n    alias: one\n")
		if err := Update(ctx, fx.session(t, "."), &bytes.Buffer{}, nil, "example.com/m1"); err == nil || !strings.Contains(err.Error(), "the highest discovered release v1.2.0 is below the imported v1.3.0") {
			t.Fatalf("a regressed origin for an import: %v", err)
		}
		// A workspace module the lint file imports has nothing to move;
		// a directory-replaced import is left as a replaced declaration.
		fx.write(t, "pb.lint.yaml", "rulesets:\n  - path: example.com/a\n    alias: a\n")
		if err := Update(ctx, fx.session(t, "."), &bytes.Buffer{}, nil, "example.com/a"); err == nil || !strings.Contains(err.Error(), "example.com/a is a workspace module the lint file imports from the working tree: nothing to move") {
			t.Fatalf("a workspace import named: %v", err)
		}
		fx.write(t, "pb.work", "use:\n  - a\nreplace:\n  example.com/m1: ./fork\n")
		fx.write(t, "fork/pb.yaml", ws("example.com/m1", ""))
		fx.write(t, "pb.lint.yaml", "rulesets:\n  - path: example.com/m1\n    alias: one\n")
		out.Reset()
		if err := Update(ctx, fx.session(t, "."), &out, nil); err != nil || out.String() != "example.com/m1: replaced by ./fork, import left\n" {
			t.Fatalf("a directory-replaced import swept: %v %q", err, out.String())
		}
		if err := Update(ctx, fx.session(t, "."), &bytes.Buffer{}, nil, "example.com/m1"); err == nil || !strings.Contains(err.Error(), "example.com/m1 is replaced by ./fork in the workspace file") {
			t.Fatalf("a directory-replaced import named: %v", err)
		}
		fx.write(t, "pb.work", "use:\n  - a\n")
		// A replaced import is reported left, as a declaration is.
		fx = newDep(t, withLint)
		serve(fx)
		fx.write(t, "pb.work", "use:\n  - a\nreplace:\n  example.com/m1: example.com/fork@v1.0.0\n")
		fx.serve(t, "example.com/fork", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/fork", "")})
		out.Reset()
		if err := Update(ctx, fx.session(t, "."), &out, nil); err != nil || !strings.Contains(out.String(), "example.com/m1: replaced by example.com/fork@v1.0.0, import left\n") || !strings.Contains(fx.read(t, "pb.lint.yaml"), "version: v1.0.0") {
			t.Fatalf("a replaced import: %v %q", err, out.String())
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

	// The rulesets' pins are covered after the modules', their lines
	// saying so (REQ-dep-ruleset-declarations).
	fx.write(t, "pb.lint.yaml", "rulesets:\n  - path: example.com/rules\n    version: v1.0.0\n    alias: rules\n")
	fx.serve(t, "example.com/rules", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/rules", ""), "r.rules.yaml": "celEnv: 1\nrules: []\n"})
	fx.Endpoint("example.com/rules", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	s = fx.session(t, ".")
	if err := Download(ctx, s, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Verify(ctx, s, &out); err != nil || !strings.Contains(out.String(), "verified 2 cached module(s)") {
		t.Fatalf("with a ruleset pin: %v %q", err, out.String())
	}
	wrongRules, _ := fetchtest.ModuleZip(t, map[string]string{"pb.yaml": ws("example.com/rules", ""), "r.rules.yaml": "celEnv: 1\nrules: []\n# tampered\n"})
	if err := s.Client.Cache.Put("example.com/rules", mustVer(t, "v1.0.0"), fetch.KindZip, wrongRules); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Verify(ctx, s, &out); err == nil || !strings.Contains(out.String(), "example.com/rules@v1.0.0 (ruleset pin): digest") {
		t.Fatalf("a tampered ruleset archive: %v %q", err, out.String())
	}
	// The modules' pins are reported before the rulesets', whatever
	// the paths' order.
	if err := s.Client.Cache.Put("example.com/m1", mustVer(t, "v1.0.0"), fetch.KindZip, wrong); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	Verify(ctx, s, &out) //nolint:errcheck — the mismatches are the point
	if m, r := strings.Index(out.String(), "example.com/m1@v1.0.0: digest"), strings.Index(out.String(), "example.com/rules@v1.0.0 (ruleset pin)"); m < 0 || r < m {
		t.Fatalf("the rulesets' pins before the modules': %q", out.String())
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
		efs := &fetchtest.ErrFS{Filesystem: fx.ws, PutFailAfter: -1, FailCreate: true}
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
		if err := Tidy(ctx, s, io.Discard); err != nil {
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
		efs := &fetchtest.ErrFS{Filesystem: fx2.ws, FailRename: true, FailCreate: true, PutFailAfter: -1}
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
		s3.WS = &fetchtest.ErrFS{Filesystem: fx3.ws, FailRename: true, FailCreate: true, PutFailAfter: -1}
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
		if err := Tidy(ctx, s, io.Discard); err == nil {
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
		if err := Tidy(ctx, s, io.Discard); err == nil || !strings.Contains(err.Error(), "x.proto") {
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
		if err := Tidy(ctx, s, io.Discard); err == nil || !strings.Contains(err.Error(), "bad.proto") {
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
		if err := Tidy(ctx, s, io.Discard); !errors.Is(err, fetchtest.ErrInjected) {
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
		if err := Tidy(ctx, s, io.Discard); err != nil {
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
		if err := Tidy(ctx, s, io.Discard); err != nil {
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
		if err := Tidy(ctx, s, io.Discard); err != nil {
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
		if err := Tidy(ctx, s, io.Discard); err != nil {
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
		if err := Tidy(ctx, s, io.Discard); err != nil {
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
	err := Tidy(ctx, s, io.Discard)
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
	err = Tidy(ctx, fx.session(t, "."), io.Discard)
	if err == nil || !strings.HasPrefix(err.Error(), "example.com/m1@v1.0.0: m1.proto: ") {
		t.Fatalf("a malformed external file: %v", err)
	}
}

// Tidy leaves rulesets alone (REQ-dep-ruleset-declarations): the lint
// file's imports add no declaration, a declaration no import uses is
// dropped like any other, and of the rulesets' pins those no import
// names — the lint file's or a rule file's — are pruned, the rest
// kept — a ruleset is no protobuf dependency.
func TestTidyLeavesRulesetsAlone(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":       "use:\n  - a\n  - lib\n",
		"pb.lint.yaml":  "rulesets:\n  - path: example.com/rules\n    version: v0.9.0\n    alias: rules\n  - path: example.com/lib\n    alias: lib\n",
		"a/pb.yaml":     ws("example.com/a", "  example.com/unused: v1.0.0\n"),
		"a/x.proto":     "syntax = \"proto3\";\n",
		"lib/pb.yaml":   ws("example.com/lib", ""),
		"lib/lib.proto": "syntax = \"proto3\";\n",
	})
	fx.serve(t, "example.com/unused", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/unused", "")})
	fx.Endpoint("example.com/unused", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	for _, v := range []string{"v0.9.0", "v1.0.0"} {
		fx.serve(t, "example.com/rules", v, map[string]string{"pb.yaml": ws("example.com/rules", ""), "r.rules.yaml": "celEnv: 1\nimports:\n  - path: example.com/fns\n    version: v0.1.0\n    alias: fns\nrules: []\n"})
		fx.Endpoint("example.com/rules", v, "info", `{"version":"`+v+`"}`)
	}
	fx.serve(t, "example.com/fns", "v0.1.0", map[string]string{"pb.yaml": ws("example.com/fns", ""), "f.rules.yaml": "celEnv: 1\nfunctions:\n  - name: yes\n    returns: bool\n    cel: \"true\"\nrules: []\n"})
	fx.Endpoint("example.com/fns", "v0.1.0", "info", `{"version":"v0.1.0"}`)
	// v0.9.0 pinned by a download, then the import moved to v1.0.0 and
	// pinned too: the stale pin is what tidy prunes.
	if err := Download(ctx, fx.session(t, "."), io.Discard); err != nil {
		t.Fatal(err)
	}
	fx.write(t, "pb.lint.yaml", "rulesets:\n  - path: example.com/rules\n    version: v1.0.0\n    alias: rules\n  - path: example.com/lib\n    alias: lib\n")
	if err := Download(ctx, fx.session(t, "."), io.Discard); err != nil {
		t.Fatal(err)
	}
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "version: v0.9.0") || !strings.Contains(lock, "version: v1.0.0") {
		t.Fatalf("both pins before tidy: %q", lock)
	}
	s := fx.session(t, ".")
	if err := Tidy(ctx, s, io.Discard); err != nil {
		t.Fatalf("Tidy: %v", err)
	}
	if got := fx.read(t, "a/pb.yaml"); got != ws("example.com/a", "") {
		t.Fatalf("tidied a/pb.yaml = %q: the ruleset import is no declaration to keep", got)
	}
	// The imported pair's pin stays, the rule file's import's with it,
	// the pair no import names goes, and no ruleset pin moves to the
	// modules.
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "  - path: example.com/rules\n    version: v1.0.0\n") || !strings.Contains(lock, "rulesets:\n  - path: example.com/fns\n    version: v0.1.0\n") || strings.Contains(lock, "v0.9.0") || strings.Contains(lock, "modules:\n  - path: example.com/rules") {
		t.Fatalf("the ruleset pins after tidy: %q", lock)
	}
	// Tidy reads the lint file for the pins its imports name; one it
	// cannot read is named.
	fx = newDep(t, map[string]string{
		"pb.work":      "use:\n  - a\n",
		"pb.lint.yaml": "rulesets: 1\n",
		"a/pb.yaml":    ws("example.com/a", ""),
		"a/x.proto":    "syntax = \"proto3\";\n",
	})
	if err := Tidy(ctx, fx.session(t, "."), io.Discard); err == nil || !errors.Is(err, lintfile.ErrInvalid) || !strings.Contains(err.Error(), "pb.lint.yaml") {
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
		// A workspace copy of a well-known path: no file of the build,
		// so neither checked nor paired (REQ-gen-compile).
		"a/google/protobuf/empty.proto": "syntax = \"proto3\";\npackage google.protobuf;\nmessage Empty {\n  string NotChecked = 1;\n}\n",
		"b/pb.yaml":                     ws("example.com/b", "  example.com/a: v0.0.1\n"),
		"b/b.proto":                     "syntax = \"proto3\";\npackage b;\nimport \"a.proto\";\nmessage Use {\n  a.Thing thing = 1;\n  string Loud = 2;\n}\n",
		"house/pb.yaml":                 ws("example.com/house", ""),
		"house/house.rules.yaml": "celEnv: 1\nrules:\n" +
			"  - id: FIELD_NAMES\n    kind: lint\n    target: field\n    severity: error\n    tags: [naming]\n    cel: case(field.name, 'snake') == field.name\n    message: field names are snake_case\n" +
			"  - id: MESSAGE_COUNT\n    kind: lint\n    target: set\n    severity: warning\n    cel: messages(files).size() < 3\n    message: too many messages\n" +
			"  - id: PACKAGE_SIZE\n    kind: lint\n    target: package\n    severity: warning\n    cel: messages(files).size() < 3\n    message: too many messages in a package\n" +
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
	fx := newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\n  - path: example.com/std\n    version: v1.0.0\n    alias: std\nignore:\n  - paths: [\"vendor/**\"]\n")
	s := fx.session(t, ".")
	var out, diag strings.Builder
	err := Lint(ctx, s, &out, &diag)
	if !errors.Is(err, ErrFindings) {
		t.Fatalf("Lint: %v", err)
	}
	want := "a.proto:5:3: error house:FIELD_NAMES: field names are snake_case\nb.proto:6:3: error house:FIELD_NAMES: field names are snake_case\nwarning house:MESSAGE_COUNT: too many messages\n"
	if out.String() != want || diag.String() != "" {
		t.Fatalf("out = %q diag = %q", out.String(), diag.String())
	}
	// The external ruleset's first use is pinned as a ruleset and
	// saved (REQ-lock-ruleset-entry) — beside its module pin, a
	// declaring dependency of a.
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "rulesets:\n  - path: example.com/std\n    version: v1.0.0\n") || !strings.Contains(lock, "modules:\n  - path: example.com/std\n") {
		t.Fatalf("the ruleset was not pinned as one: %q", lock)
	}
	// Warnings alone pass; a severity override turns the error down.
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nenable: [naming]\nseverity:\n  FIELD_NAMES: warning\nignore:\n  - paths: [\"vendor/**\"]\n")
	out.Reset()
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err != nil || !strings.Contains(out.String(), "warning house:FIELD_NAMES") || strings.Contains(out.String(), "MESSAGE_COUNT") {
		t.Fatalf("warnings: %v %q", err, out.String())
	}
	// A module's own selection governs its files alone: a's entry
	// enables nothing, b stays under the root, and the root's set
	// rule counts b's messages alone.
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nmodules:\n  a:\n    enable: []\n")
	out.Reset()
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err == nil || !errors.Is(err, ErrFindings) || out.String() != "b.proto:6:3: error house:FIELD_NAMES: field names are snake_case\n" {
		t.Fatalf("per module: %v %q", err, out.String())
	}
	// An ignore naming a kind excludes that kind's findings alone: the
	// breaking ignore over a.proto leaves its lint finding standing.
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nenable: [naming]\nignore:\n  - paths: [a.proto, \"vendor/**\"]\n    kind: breaking\n  - paths: [\"vendor/**\"]\n")
	out.Reset()
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err == nil || !errors.Is(err, ErrFindings) || out.String() != "a.proto:5:3: error house:FIELD_NAMES: field names are snake_case\nb.proto:6:3: error house:FIELD_NAMES: field names are snake_case\n" {
		t.Fatalf("an ignore of the other kind: %v %q", err, out.String())
	}
	// A module's own ignore excludes its files' findings alone: a's
	// entry ignores a.proto, and b's finding under the root stands.
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nmodules:\n  a:\n    ignore:\n      - paths: [a.proto, \"vendor/**\"]\n")
	out.Reset()
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err == nil || !errors.Is(err, ErrFindings) || out.String() != "b.proto:6:3: error house:FIELD_NAMES: field names are snake_case\n" {
		t.Fatalf("a module's own ignore: %v %q", err, out.String())
	}
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nmodules:\n  nowhere:\n    enable: []\n")
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err == nil || !strings.Contains(err.Error(), `modules names "nowhere", which is no workspace module`) {
		t.Fatalf("an unknown module: %v", err)
	}
	// Every module with an entry leaves the root governing nothing; a
	// set rule enabled for a module sees its files alone and its
	// finding is located at the module's directory.
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nmodules:\n  a:\n    enable: [MESSAGE_COUNT]\n  b:\n    enable: [MESSAGE_COUNT]\n  house:\n    enable: []\n")
	out.Reset()
	diag.Reset()
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err != nil || out.String() != "" || diag.String() != "" {
		t.Fatalf("every module its own, under the count: %v %q %q", err, out.String(), diag.String())
	}
	// Every module's entry enabling nothing leaves zero rules enabled
	// under every selection governing a file, whatever the root would
	// enable for a module it does not govern.
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nmodules:\n  a:\n    enable: []\n  b:\n    enable: []\n  house:\n    enable: []\n")
	out.Reset()
	diag.Reset()
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err != nil || out.String() != "" || diag.String() != "pb lint: zero lint rules enabled\n" {
		t.Fatalf("zero under every selection: %v %q %q", err, out.String(), diag.String())
	}
	// A package finding under a module's own selection is located
	// there too, in place of the package's first file.
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nmodules:\n  a:\n    enable: [MESSAGE_COUNT, PACKAGE_SIZE]\n")
	fx.write(t, "a/more.proto", "syntax = \"proto3\";\npackage a;\nmessage M2 {}\nmessage M3 {}\n")
	out.Reset()
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); !errors.Is(err, ErrFindings) || out.String() != "a: warning house:MESSAGE_COUNT: too many messages\na: warning house:PACKAGE_SIZE: too many messages in a package\nb.proto:6:3: error house:FIELD_NAMES: field names are snake_case\n" {
		t.Fatalf("a module's set and package findings located at its directory: %v %q", err, out.String())
	}
	// Under the root, the package finding sits at the package's first
	// file, and the root's ignore over that file drops it.
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nenable: [PACKAGE_SIZE]\nignore:\n  - paths: [a.proto]\n")
	fx.write(t, "a/more.proto", "syntax = \"proto3\";\npackage a;\nmessage M2 {}\nmessage M3 {}\n")
	out.Reset()
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err != nil || out.String() != "" {
		t.Fatalf("a package finding at its first file, ignored: %v %q", err, out.String())
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
	// Imports are exact and isolated (the ruleset import term): one
	// path at two versions under two aliases reads two rulesets, each
	// rule under its import's alias, both pinned as rulesets — and an
	// import no module declares is pinned among the rulesets alone.
	fx = newCheck(t, "rulesets:\n  - path: example.com/std\n    version: v1.0.0\n    alias: one\n  - path: example.com/std\n    version: v1.1.0\n    alias: two\n")
	fx.serve(t, "example.com/std", "v1.1.0", map[string]string{
		"pb.yaml":        ws("example.com/std", ""),
		"std.rules.yaml": "celEnv: 1\nrules:\n  - id: PACKAGE_DEFINED\n    kind: lint\n    target: file\n    severity: error\n    cel: file.package != ''\n    message: files declare a package\n  - id: NEWER\n    kind: lint\n    target: file\n    severity: warning\n    cel: file.package != 'a'\n    message: the newer ruleset alone declares this\n",
	})
	out.Reset()
	diag.Reset()
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err != nil || out.String() != "a.proto:1:1: warning two:NEWER: the newer ruleset alone declares this\n" {
		t.Fatalf("two versions of one ruleset: %v %q %q", err, out.String(), diag.String())
	}
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "rulesets:\n  - path: example.com/std\n    version: v1.0.0\n") || !strings.Contains(lock, "  - path: example.com/std\n    version: v1.1.0\n") || strings.Contains(lock, "modules:\n  - path: example.com/std\n    version: v1.1.0") {
		t.Fatalf("two versions pinned as rulesets: %q", lock)
	}
	// A first ruleset pin makes the lockfile, holding rulesets alone
	// (REQ-lock-first-use); a pin made before a later import fails is
	// saved; a pair the modules pin at another digest is refused, one
	// pair naming one content (REQ-lock-ruleset-entry).
	std := "  - path: example.com/std\n    version: v1.0.0\n    alias: std\n"
	lean := func(lint, lock string) *depFixture {
		files := map[string]string{
			"pb.work":      "use:\n  - a\n",
			"pb.lint.yaml": lint,
			"a/pb.yaml":    ws("example.com/a", ""),
			"a/a.proto":    "syntax = \"proto3\";\npackage a;\n",
		}
		if lock != "" {
			files["pb.lock"] = lock
		}
		fx := newDep(t, files)
		fx.serve(t, "example.com/std", "v1.0.0", map[string]string{
			"pb.yaml":        ws("example.com/std", ""),
			"std.rules.yaml": "celEnv: 1\nrules:\n  - id: PACKAGE_DEFINED\n    kind: lint\n    target: file\n    severity: error\n    cel: file.package != ''\n    message: files declare a package\n",
		})
		return fx
	}
	fx = lean("rulesets:\n"+std, "")
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err != nil {
		t.Fatalf("lint with a ruleset alone: %v", err)
	}
	if lock := fx.read(t, "pb.lock"); !strings.HasPrefix(lock, "version: 1\nmodules:\nrulesets:\n  - path: example.com/std\n    version: v1.0.0\n") {
		t.Fatalf("the lockfile a ruleset pin makes: %q", lock)
	}
	fx = lean("rulesets:\n"+std+"  - path: example.com/nowhere\n    version: v1.0.0\n    alias: no\n", "")
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err == nil || !strings.Contains(err.Error(), "ruleset example.com/nowhere@v1.0.0") {
		t.Fatalf("a later import failing: %v", err)
	}
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "rulesets:\n  - path: example.com/std\n    version: v1.0.0\n") {
		t.Fatalf("the pin made before the failure was lost: %q", lock)
	}
	fx = lean("rulesets:\n"+std, "version: 1\nmodules:\n  - path: example.com/std\n    version: v1.0.0\n    digest: pb1:"+strings.Repeat("0", 64)+"\n    provenance: none\n")
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err == nil || !errors.Is(err, lockfile.ErrPinMismatch) || !strings.Contains(err.Error(), "example.com/std@v1.0.0 digest: pinned as a module at pb1:"+strings.Repeat("0", 64)+", fetched pb1:") {
		t.Fatalf("a pair the modules pin at another digest: %v", err)
	}
	if lock := fx.read(t, "pb.lock"); strings.Contains(lock, "rulesets:") {
		t.Fatalf("the refused pair was pinned as a ruleset: %q", lock)
	}
	// And the other way: a module's first use held to the rulesets'
	// pin of the pair.
	fx = lean("rulesets:\n"+std, "version: 1\nmodules: []\nrulesets:\n  - path: example.com/std\n    version: v1.0.0\n    digest: pb1:"+strings.Repeat("0", 64)+"\n    provenance: none\n")
	fx.write(t, "a/pb.yaml", ws("example.com/a", "  example.com/std: v1.0.0\n"))
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err == nil || !errors.Is(err, lockfile.ErrPinMismatch) || !strings.Contains(err.Error(), "example.com/std@v1.0.0 digest: pinned as a ruleset at pb1:"+strings.Repeat("0", 64)+", fetched pb1:") {
		t.Fatalf("a pair the rulesets pin at another digest: %v", err)
	}

	// Imports read through the workspace's replacements as the build
	// does: a directory replacement from the working tree, a version
	// refused; a path replacement's pair in the path's place, pinned
	// under its own path (REQ-lint-rulesets-imported).
	forked := "celEnv: 1\nrules:\n  - id: FORKED\n    kind: lint\n    target: file\n    severity: error\n    cel: \"false\"\n    message: the fork's rule\n"
	fx = newDep(t, map[string]string{
		"pb.work":              "use:\n  - a\nreplace:\n  example.com/std: ./fork\n",
		"pb.lint.yaml":         "rulesets:\n  - path: example.com/std\n    alias: std\n",
		"a/pb.yaml":            ws("example.com/a", ""),
		"a/a.proto":            "syntax = \"proto3\";\npackage a;\n",
		"fork/pb.yaml":         ws("example.com/std", ""),
		"fork/fork.rules.yaml": forked,
	})
	out.Reset()
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); !errors.Is(err, ErrFindings) || out.String() != "a.proto:1:1: error std:FORKED: the fork's rule\n" {
		t.Fatalf("a directory replacement's rules: %v %q", err, out.String())
	}
	fx.write(t, "pb.lint.yaml", "rulesets:\n  - path: example.com/std\n    version: v1.0.0\n    alias: std\n")
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err == nil || !strings.Contains(err.Error(), "ruleset example.com/std: replaced by a directory, read from the working tree: write no version (v1.0.0 written)") {
		t.Fatalf("a directory replacement with a version: %v", err)
	}
	fx = newDep(t, map[string]string{
		"pb.work":      "use:\n  - a\nreplace:\n  example.com/std: example.com/fork@v2.0.0\n",
		"pb.lint.yaml": "rulesets:\n  - path: example.com/std\n    version: v1.0.0\n    alias: std\n",
		"a/pb.yaml":    ws("example.com/a", ""),
		"a/a.proto":    "syntax = \"proto3\";\npackage a;\n",
	})
	fx.serve(t, "example.com/fork", "v2.0.0", map[string]string{"pb.yaml": ws("example.com/fork", ""), "fork.rules.yaml": forked})
	out.Reset()
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); !errors.Is(err, ErrFindings) || out.String() != "a.proto:1:1: error std:FORKED: the fork's rule\n" {
		t.Fatalf("a path replacement's rules: %v %q", err, out.String())
	}
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "rulesets:\n  - path: example.com/fork\n    version: v2.0.0\n") || strings.Contains(lock, "example.com/std") {
		t.Fatalf("a path replacement's pin: %q", lock)
	}

	// A rule file's functions and imports through the verb: the
	// workspace ruleset's file imports a fetched ruleset for a function
	// and declares its own over it; a rule calls both; the imported
	// pair is pinned as a ruleset (REQ-rules-functions,
	// REQ-rules-imports).
	fx = newDep(t, map[string]string{
		"pb.work":       "use:\n  - a\n  - house\n",
		"pb.lint.yaml":  "rulesets:\n  - path: example.com/house\n    alias: house\n",
		"a/pb.yaml":     ws("example.com/a", ""),
		"a/a.proto":     "syntax = \"proto3\";\npackage a;\nmessage Thing {\n  string BadName = 1;\n  string ok = 2;\n}\n",
		"house/pb.yaml": ws("example.com/house", ""),
		"house/house.rules.yaml": "celEnv: 1\nimports:\n  - path: example.com/fns\n    version: v0.1.0\n    alias: fns\nfunctions:\n  - name: named\n    params:\n      - name: f\n        type: google.protobuf.FieldDescriptorProto\n    returns: bool\n    cel: fns.isSnake(f.name)\nrules:\n" +
			"  - id: FIELD_NAMES\n    kind: lint\n    target: field\n    severity: error\n    cel: named(field)\n    message: field names are snake_case\n",
	})
	fx.serve(t, "example.com/fns", "v0.1.0", map[string]string{
		"pb.yaml":      ws("example.com/fns", ""),
		"f.rules.yaml": "celEnv: 1\nfunctions:\n  - name: isSnake\n    params:\n      - name: s\n        type: string\n    returns: bool\n    cel: case(s, 'snake') == s\nrules: []\n",
	})
	out.Reset()
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); !errors.Is(err, ErrFindings) || out.String() != "a.proto:4:3: error house:FIELD_NAMES: field names are snake_case\n" {
		t.Fatalf("functions through the verb: %v %q", err, out.String())
	}
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "rulesets:\n  - path: example.com/fns\n    version: v0.1.0\n") {
		t.Fatalf("the rule file's import pinned as a ruleset: %q", lock)
	}
	// A rule calling a function its file never sees fails the run
	// naming the rule and the cause.
	fx.write(t, "house/house.rules.yaml", "celEnv: 1\nrules:\n  - id: FIELD_NAMES\n    kind: lint\n    target: field\n    severity: error\n    cel: fns.isSnake(field.name)\n    message: m\n")
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err == nil || !strings.Contains(err.Error(), "house:FIELD_NAMES") || !strings.Contains(err.Error(), "undeclared reference to 'fns'") {
		t.Fatalf("a function the file never imported: %v", err)
	}

	// An import whose version does not resolve fails naming it; a
	// workspace module imported with a version, and an external one
	// imported without, are refused as written (REQ-lint-rulesets-
	// imported).
	fx = newCheck(t, "rulesets:\n  - path: example.com/nowhere\n    version: v1.0.0\n    alias: nowhere\n")
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err == nil || !strings.Contains(err.Error(), "ruleset example.com/nowhere@v1.0.0") {
		t.Fatalf("unresolved import: %v", err)
	}
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    version: v1.0.0\n    alias: house\n")
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err == nil || !strings.Contains(err.Error(), "ruleset example.com/house: a workspace module, read from the working tree: write no version") {
		t.Fatalf("workspace module with a version: %v", err)
	}
	fx = newCheck(t, "rulesets:\n  - path: example.com/std\n    alias: std\n")
	if err := Lint(ctx, fx.session(t, "."), &out, &diag); err == nil || !strings.Contains(err.Error(), "ruleset example.com/std: no workspace module: write the version to read") {
		t.Fatalf("external without a version: %v", err)
	}
}

// pb breaking materializes each module's base in the lint file's
// form, pairs it with the checked schema, evaluates the breaking
// rules, and reports every module's findings as one stream, base
// findings marked; a version base is pinned; no base configured fails
// naming the file; zero breaking rules is said on standard error
// (REQ-check-breaking-verb, REQ-break-base).
func TestBreaking(t *testing.T) {
	fx := newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nbreaking:\n  base:\n    version: v0.9.0\n")
	// The bases: a had a field since removed and a field whose type
	// changed; b had a field since removed.
	fx.serve(t, "example.com/a", "v0.9.0", map[string]string{
		"pb.yaml": ws("example.com/a", ""),
		"a.proto": "syntax = \"proto3\";\npackage a;\n\nmessage Thing {\n  string BadName = 1;\n  string Other = 2;\n  int32 ok = 3;\n  string gone = 4;\n}\n",
		// The base's copy of a well-known path pairs with nothing.
		"google/protobuf/empty.proto": "syntax = \"proto3\";\npackage google.protobuf;\nmessage Empty {\n  string NotChecked = 1;\n  string gone_too = 2;\n}\n",
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
	want := "a.proto:7:3: warning house:FIELD_TYPE: type changed\na.proto:8:3: error house:FIELD_GONE: field removed [base]\nb.proto:7:3: error house:FIELD_GONE: field removed [base]\n"
	if out.String() != want || diag.String() != "" {
		t.Fatalf("out = %q diag = %q", out.String(), diag.String())
	}
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "example.com/a") || !strings.Contains(lock, "v0.9.0") {
		t.Fatalf("the version base was not pinned: %q", lock)
	}
	// No base configured.
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\n")
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); err == nil || !strings.Contains(err.Error(), "names no breaking base") {
		t.Fatalf("no base: %v", err)
	}
	// Zero breaking rules.
	fx = newCheck(t, "rulesets:\n  - path: example.com/std\n    version: v1.0.0\n    alias: std\nbreaking:\n  base:\n    pinned: true\n")
	out.Reset()
	diag.Reset()
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); err != nil || diag.String() != "pb breaking: zero breaking rules enabled\n" {
		t.Fatalf("zero rules: %v %q", err, diag.String())
	}
	// A base the origin does not serve fails naming the module and the
	// form; a base pinned before the failure stays pinned.
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nbreaking:\n  base:\n    version: v0.8.0\n")
	fx.serve(t, "example.com/a", "v0.8.0", map[string]string{"pb.yaml": ws("example.com/a", ""), "a.proto": "syntax = \"proto3\";\npackage a;\nmessage Thing {}\n"})
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); err == nil || !strings.Contains(err.Error(), "breaking: example.com/b: breaking base version v0.8.0") {
		t.Fatalf("unserved base: %v", err)
	}
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "v0.8.0") {
		t.Fatalf("a's base pin lost on b's failure: %q", lock)
	}
	// A module whose own selection enables no breaking rule needs no
	// base: b's unserved base is never asked for, nothing of b is
	// pinned, and a's findings under the root's rules stand.
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nmodules:\n  b:\n    enable: []\nbreaking:\n  base:\n    version: v0.8.0\n")
	fx.serve(t, "example.com/a", "v0.8.0", map[string]string{"pb.yaml": ws("example.com/a", ""), "a.proto": "syntax = \"proto3\";\npackage a;\nmessage Thing {\n  string gone = 9;\n}\n"})
	out.Reset()
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); !errors.Is(err, ErrFindings) || out.String() != "a.proto:4:3: error house:FIELD_GONE: field removed [base]\n" {
		t.Fatalf("a module needing no base: %v %q", err, out.String())
	}
	if lock := fx.read(t, "pb.lock"); strings.Contains(lock, "example.com/b") || !strings.Contains(lock, "v0.8.0") {
		t.Fatalf("b's base pinned, or a's not: %q", lock)
	}
	// The base compiles with the build's other modules resolving its
	// imports and none other a target: a's base lacking a message b
	// uses now is a's base still; an ignore drops a base finding.
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nbreaking:\n  base:\n    version: v0.7.0\nignore:\n  - paths: [\"b.proto\"]\n    rules: [FIELD_GONE]\n")
	fx.serve(t, "example.com/a", "v0.7.0", map[string]string{"pb.yaml": ws("example.com/a", ""), "a.proto": "syntax = \"proto3\";\npackage a;\nmessage Former {\n  string gone = 1;\n}\n"})
	fx.serve(t, "example.com/b", "v0.7.0", map[string]string{"pb.yaml": ws("example.com/b", "  example.com/a: v0.0.1\n"), "b.proto": "syntax = \"proto3\";\npackage b;\nmessage Use {\n  string Loud = 2;\n  string dropped = 3;\n}\n"})
	out.Reset()
	err = Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag)
	if !errors.Is(err, ErrFindings) || out.String() != "a.proto:4:3: error house:FIELD_GONE: field removed [base]\n" {
		t.Fatalf("base lacking what b uses: %v %q", err, out.String())
	}
	// A module's own ignore reaches its base's files: a's base held a
	// file since removed, whose finding a's entry drops by path, while
	// an entry naming another rule leaves it standing.
	for _, c := range []struct{ rules, want string }{
		{"", ""},
		{"\n        rules: [FIELD_TYPE]", "former.proto:4:3: error house:FIELD_GONE: field removed [base]\n"},
	} {
		fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nmodules:\n  a:\n    ignore:\n      - paths: [former.proto]"+c.rules+"\nbreaking:\n  base:\n    version: v0.6.0\n")
		fx.serve(t, "example.com/a", "v0.6.0", map[string]string{"pb.yaml": ws("example.com/a", ""), "a.proto": fx.read(t, "a/a.proto"), "former.proto": "syntax = \"proto3\";\npackage a;\nmessage Former {\n  string gone = 1;\n}\n"})
		fx.serve(t, "example.com/b", "v0.6.0", map[string]string{"pb.yaml": ws("example.com/b", "  example.com/a: v0.0.1\n"), "b.proto": fx.read(t, "b/b.proto")})
		out.Reset()
		err = Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag)
		if (c.want == "") != (err == nil) || out.String() != c.want {
			t.Fatalf("a module's ignore over its base's file (rules %q): %v %q", c.rules, err, out.String())
		}
	}
	// A file a gained after its base, imported by b now: a's base run
	// checks a's imports alone, b's being no requirer of it.
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nbreaking:\n  base:\n    version: v0.7.0\n")
	fx.serve(t, "example.com/a", "v0.7.0", map[string]string{"pb.yaml": ws("example.com/a", ""), "a.proto": "syntax = \"proto3\";\npackage a;\nmessage Thing {\n  string BadName = 1;\n}\n"})
	fx.serve(t, "example.com/b", "v0.7.0", map[string]string{"pb.yaml": ws("example.com/b", "  example.com/a: v0.0.1\n"), "b.proto": "syntax = \"proto3\";\npackage b;\nimport \"a.proto\";\nmessage Use {\n  a.Thing thing = 1;\n}\n"})
	fx.write(t, "b/b.proto", "syntax = \"proto3\";\npackage b;\nimport \"a.proto\";\nimport \"vendor/v.proto\";\nmessage Use {\n  a.Thing thing = 1;\n  a.vendor.V v = 2;\n}\n")
	out.Reset()
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); err != nil || out.String() != "" {
		t.Fatalf("a's base with b importing a's newer file: %v %q", err, out.String())
	}
	// A reference base with no repository source wired fails naming
	// it.
	fx = newCheck(t, "rulesets:\n  - path: example.com/house\n    alias: house\nbreaking:\n  base:\n    ref: HEAD\n")
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); err == nil || !strings.Contains(err.Error(), "no repository source is wired") {
		t.Fatalf("no repo source: %v", err)
	}
}

// A module whose only protobuf file is a copy of a well-known path
// holds no file of the build: nothing to pair, no base materialized,
// nothing pinned — while a sibling with files is paired as ever
// (REQ-break-base-materialized, REQ-gen-compile).
func TestBreakingSkipsModuleWithoutFilesOfTheBuild(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":                       "use:\n  - a\n  - b\n  - house\n",
		"pb.lint.yaml":                  "rulesets:\n  - path: example.com/house\n    alias: house\nbreaking:\n  base:\n    version: v0.9.0\n",
		"a/pb.yaml":                     ws("example.com/a", ""),
		"a/google/protobuf/empty.proto": "syntax = \"proto3\";\npackage google.protobuf;\nmessage Empty {}\n",
		"b/pb.yaml":                     ws("example.com/b", ""),
		"b/b.proto":                     "syntax = \"proto3\";\npackage b;\nmessage B {\n  string kept = 1;\n}\n",
		"house/pb.yaml":                 ws("example.com/house", ""),
		"house/house.rules.yaml":        "celEnv: 1\nrules:\n  - id: FIELD_GONE\n    kind: breaking\n    target: field\n    severity: error\n    cel: new != null\n    message: field removed\n",
	})
	// b's base is served and equal; none is served for a, so
	// materializing one for a would fail.
	fx.serve(t, "example.com/b", "v0.9.0", map[string]string{
		"pb.yaml": ws("example.com/b", ""),
		"b.proto": "syntax = \"proto3\";\npackage b;\nmessage B {\n  string kept = 1;\n}\n",
	})
	var out, diag strings.Builder
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); err != nil || out.String() != "" || diag.String() != "" {
		t.Fatalf("Breaking: %v %q %q", err, out.String(), diag.String())
	}
	lock := fx.read(t, "pb.lock")
	if !strings.Contains(lock, "example.com/b") || strings.Contains(lock, "example.com/a") {
		t.Fatalf("pins = %q: b's base pinned, none for a", lock)
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

// pb format lists, diffs or rewrites the workspace's own files whose
// bytes are not their canonical form, in path order, fails under
// --exit-code where any was not, and refuses a file that does not
// parse before writing anything (format.md REQ-format-verb).
func TestFormat(t *testing.T) {
	canonical := "syntax = \"proto3\";\n\npackage b;\n\nmessage Tidy {\n  string name = 1;\n}\n"
	messy := "syntax = \"proto3\";\npackage a;\nmessage   Loose {\n      string name=1;  }\n"
	// Neither the workspace's order (z, b, a) nor the module paths'
	// (example.com/aa at z, example.com/zz at b) is the directories'
	// order; the files are listed by their paths from the root, a
	// workspace copy of a well-known file among them.
	files := func() map[string]string {
		return map[string]string{
			"pb.work":                       "use:\n  - z\n  - b\n  - a\n",
			"a/pb.yaml":                     ws("example.com/a", ""),
			"a/loose.proto":                 messy,
			"a/sub/also.proto":              "syntax = \"proto3\";\npackage a.sub;\n\n\n\nmessage Also {}\n",
			"a/google/protobuf/empty.proto": "syntax = \"proto3\";\npackage google.protobuf;\nmessage Empty {\n}\n",
			"b/pb.yaml":                     ws("example.com/zz", ""),
			"b/tidy.proto":                  canonical,
			"b/messy.proto":                 "syntax  =  \"proto3\";\n",
			"z/pb.yaml":                     ws("example.com/aa", ""),
			"z/last.proto":                  "syntax  =  \"proto3\";\n",
		}
	}
	// A real filesystem: the mode a rewrite keeps is one the in-memory
	// filesystem does not record.
	dir := t.TempDir()
	fx := newDepOn(t, osfs.New(dir), files())
	if err := os.Chmod(filepath.Join(dir, "a", "loose.proto"), 0o640); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := Format(ctx, fx.session(t, "."), FormatOptions{}, &out); err != nil || out.String() != "a/google/protobuf/empty.proto\na/loose.proto\na/sub/also.proto\nb/messy.proto\nz/last.proto\n" {
		t.Fatalf("list: %v %q", err, out.String())
	}
	if fx.read(t, "a/loose.proto") != messy {
		t.Fatal("listing wrote")
	}
	out.Reset()
	err := Format(ctx, fx.session(t, "."), FormatOptions{Diff: true, ExitCode: true}, &out)
	wantDiff := "--- a/google/protobuf/empty.proto\n+++ a/google/protobuf/empty.proto\n@@ -1,4 +1,4 @@\n syntax = \"proto3\";\n package google.protobuf;\n-message Empty {\n-}\n+\n+message Empty {}\n--- a/loose.proto\n+++ a/loose.proto\n@@ -1,4 +1,6 @@\n syntax = \"proto3\";\n package a;\n-message   Loose {\n-      string name=1;  }\n+\n+message Loose {\n+  string name = 1;\n+}\n--- a/sub/also.proto\n+++ a/sub/also.proto\n@@ -1,6 +1,4 @@\n syntax = \"proto3\";\n package a.sub;\n \n-\n-\n message Also {}\n--- b/messy.proto\n+++ b/messy.proto\n@@ -1 +1 @@\n-syntax  =  \"proto3\";\n+syntax = \"proto3\";\n--- z/last.proto\n+++ z/last.proto\n@@ -1 +1 @@\n-syntax  =  \"proto3\";\n+syntax = \"proto3\";\n"
	if !errors.Is(err, ErrUnformatted) || out.String() != wantDiff {
		t.Fatalf("diff: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := Format(ctx, fx.session(t, "."), FormatOptions{Write: true, ExitCode: true}, &out); !errors.Is(err, ErrUnformatted) || out.String() != "a/google/protobuf/empty.proto\na/loose.proto\na/sub/also.proto\nb/messy.proto\nz/last.proto\n" {
		t.Fatalf("write: %v %q", err, out.String())
	}
	if got := fx.read(t, "a/loose.proto"); got != "syntax = \"proto3\";\npackage a;\n\nmessage Loose {\n  string name = 1;\n}\n" {
		t.Fatalf("written: %q", got)
	}
	// windows has no mode to keep.
	if info, err := fx.ws.Stat("a/loose.proto"); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o640) {
		t.Fatalf("the mode kept: %v %v", info.Mode(), err)
	}
	if fx.read(t, "b/tidy.proto") != canonical {
		t.Fatal("a formatted file rewritten")
	}
	out.Reset()
	if err := Format(ctx, fx.session(t, "."), FormatOptions{ExitCode: true}, &out); err != nil || out.String() != "" {
		t.Fatalf("after the write: %v %q", err, out.String())
	}
	// A file that does not parse fails the run naming it, nothing
	// written.
	fx = newDep(t, files())
	fx.write(t, "b/broken.proto", "syntax = \"proto3\";\nmessage {\n")
	out.Reset()
	if err := Format(ctx, fx.session(t, "."), FormatOptions{Write: true}, &out); err == nil || !strings.Contains(err.Error(), "b/broken.proto") || !strings.Contains(err.Error(), "does not parse") || out.String() != "" {
		t.Fatalf("a file that does not parse: %v %q", err, out.String())
	}
	if fx.read(t, "a/loose.proto") != messy {
		t.Fatal("written despite the failure")
	}
	// A symbolic link among the files to rewrite fails the run before
	// anything is written.
	fx = newDep(t, files())
	if err := fx.ws.Symlink("loose.proto", "a/link.proto"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Format(ctx, fx.session(t, "."), FormatOptions{Write: true}, &out); err == nil || !strings.Contains(err.Error(), "a/link.proto is a symbolic link") || out.String() != "" {
		t.Fatalf("a symbolic link: %v %q", err, out.String())
	}
	if fx.read(t, "a/loose.proto") != messy {
		t.Fatal("written despite the link")
	}
	out.Reset()
	if err := Format(ctx, fx.session(t, "."), FormatOptions{}, &out); err != nil || !strings.Contains(out.String(), "a/link.proto\n") {
		t.Fatalf("a link listed: %v %q", err, out.String())
	}
}

// A descriptor set file as the base: the set pb build wrote at the
// old state stands as compiled, each module under check's base the
// set's files it provides now and those of its own package it no
// longer provides, the findings those a version base gives; the
// set's files of a package no module declares now — a package
// deleted whole, which a dropped dependency would look like — judged
// once under the root's selection, whatever the modules' own entries
// say; a base-located finding at the recorded column, a tab
// advancing it to eight; the well-known file the set carries no file
// of any base; nothing is fetched or pinned; a file that is no
// descriptor set fails naming the form (REQ-break-base,
// REQ-break-base-materialized).
func TestBreakingFromFile(t *testing.T) {
	old := map[string]string{
		"pb.work":      "use:\n  - a\n  - b\n  - house\n",
		"pb.lint.yaml": "rulesets:\n  - path: example.com/house\n    alias: house\nbreaking:\n  base:\n    file: build/base.binpb\nmodules:\n  a:\n    ignore:\n      - paths: [old.proto, shared_old.proto, nopkg.proto]\n",
		"a/pb.yaml":    ws("example.com/a", ""),
		// a imports a well-known file, which the set carries and which
		// is no file of a's base; a's own copy of it is no file of the
		// build.
		"a/a.proto": "syntax = \"proto3\";\npackage a;\n\nimport \"google/protobuf/empty.proto\";\n\nmessage Thing {\n  string BadName = 1;\n  string Other = 2;\n  int32 ok = 3;\n  string gone = 4;\n  google.protobuf.Empty e = 5;\n}\n",
		// old.proto is deleted in the new state; its field is indented
		// with a tab.
		"a/old.proto": "syntax = \"proto3\";\npackage a;\nmessage Old {\n\tstring y = 1;\n}\nmessage Moved {\n  string z = 1;\n}\n",
		// legacy.proto, the whole of package legacy, is deleted in the
		// new state.
		"a/legacy.proto": "syntax = \"proto3\";\npackage legacy;\nmessage Legacy {\n  string q = 1;\n}\n",
		// Package shared spans a and b; shared_old.proto is deleted in the
		// new state, its module unknowable.
		"a/shared_a.proto":   "syntax = \"proto3\";\npackage shared;\nmessage SharedA {\n  string s = 1;\n}\n",
		"a/shared_old.proto": "syntax = \"proto3\";\npackage shared;\nmessage SharedOld {\n  string t = 1;\n}\n",
		"b/shared_b.proto":   "syntax = \"proto3\";\npackage shared;\nmessage SharedB {\n  string u = 1;\n}\n",
		// Files declaring no package: a keeps one, b's is deleted in the
		// new state and is of no module.
		"a/nopkg_a.proto":        "syntax = \"proto3\";\nmessage NA {\n  string w = 1;\n}\n",
		"b/nopkg.proto":          "syntax = \"proto3\";\nmessage N {\n  string v = 1;\n}\n",
		"b/pb.yaml":              ws("example.com/b", "  example.com/a: v0.0.1\n"),
		"b/b.proto":              "syntax = \"proto3\";\npackage b;\nimport \"a.proto\";\nimport \"google/protobuf/timestamp.proto\";\nmessage Use {\n  a.Thing thing = 1;\n  string Loud = 2;\n  string dropped = 3;\n  google.protobuf.Timestamp t = 4;\n}\n",
		"house/pb.yaml":          ws("example.com/house", ""),
		"house/house.rules.yaml": "celEnv: 1\nrules:\n  - id: FIELD_GONE\n    kind: breaking\n    target: field\n    severity: error\n    cel: new != null\n    message: field removed\n  - id: FIELD_TYPE\n    kind: breaking\n    target: field\n    severity: warning\n    cel: old == null || new == null || old.type == new.type\n    message: type changed\n  - id: FILE_GONE\n    kind: breaking\n    target: file\n    severity: error\n    cel: new != null\n    message: file removed\n  - id: MSG_GONE\n    kind: breaking\n    target: message\n    severity: error\n    cel: new != null\n    message: message removed\n  - id: PKG_GONE\n    kind: breaking\n    target: package\n    severity: error\n    cel: newPackage != null\n    message: package removed\n  - id: SET_PKGS\n    kind: breaking\n    target: set\n    severity: error\n    cel: oldFiles.all(f, newFiles.exists(g, g.package == f.package))\n    message: a package vanished\n  - id: NEW_MSG\n    kind: breaking\n    target: message\n    severity: warning\n    cel: old != null\n    message: message added\n  - id: SET_NOADD\n    kind: breaking\n    target: set\n    severity: error\n    cel: newFiles.all(g, oldFiles.exists(f, f.package == g.package))\n    message: a package was added\n  - id: PKG_NOADD\n    kind: breaking\n    target: package\n    severity: error\n    cel: newFiles == null || oldFiles == null || newFiles.all(g, g.message_type.all(n, oldFiles.exists(f, f.message_type.exists(m, m.name == n.name))))\n    message: a message was added to the package\n",
	}
	fx := newDep(t, old)
	if err := fx.ws.MkdirAll("build", 0o755); err != nil {
		t.Fatal(err)
	}
	var out, diag strings.Builder
	if err := Build(ctx, fx.session(t, "."), "build/base.binpb", "build/base.binpb", &out); err != nil {
		t.Fatal(err)
	}
	set := fx.read(t, "build/base.binpb")
	// The new state: a's gone field removed and ok's type changed, b's
	// dropped field removed, a's old.proto gone; Moved is an addition
	// to a's own base and a pair in the root's.
	for _, p := range []string{"a/old.proto", "a/legacy.proto", "a/shared_old.proto", "b/nopkg.proto"} {
		if err := fx.ws.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	// Moved lands in a.proto, its field's type changed on the way:
	// old.proto, of a's own package, is a's base file, so the move
	// pairs within a and no set or package rule sees an addition;
	// a's own ignore of old.proto reaches its deletion findings.
	fx.write(t, "a/a.proto", "syntax = \"proto3\";\npackage a;\n\nimport \"google/protobuf/empty.proto\";\n\nmessage Thing {\n  string BadName = 1;\n  string Other = 2;\n  string ok = 3;\n  google.protobuf.Empty e = 5;\n}\n\nmessage Moved {\n  int32 z = 1;\n}\n")
	fx.write(t, "b/b.proto", "syntax = \"proto3\";\npackage b;\nimport \"a.proto\";\nimport \"google/protobuf/timestamp.proto\";\nmessage Use {\n  a.Thing thing = 1;\n  string Loud = 2;\n  google.protobuf.Timestamp t = 4;\n}\n")
	out.Reset()
	err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag)
	if !errors.Is(err, ErrFindings) {
		t.Fatalf("Breaking: %v", err)
	}
	want := "a.proto:9:3: warning house:FIELD_TYPE: type changed\na.proto:10:3: error house:FIELD_GONE: field removed [base]\na.proto:14:3: warning house:FIELD_TYPE: type changed\nb.proto:8:3: error house:FIELD_GONE: field removed [base]\nlegacy.proto: error house:PKG_GONE: package removed [base]\nlegacy.proto:1:1: error house:FILE_GONE: file removed [base]\nlegacy.proto:3:1: error house:MSG_GONE: message removed [base]\nlegacy.proto:4:3: error house:FIELD_GONE: field removed [base]\nnopkg.proto:1:1: error house:FILE_GONE: file removed [base]\nnopkg.proto:2:1: error house:MSG_GONE: message removed [base]\nnopkg.proto:3:3: error house:FIELD_GONE: field removed [base]\nshared_old.proto:1:1: error house:FILE_GONE: file removed [base]\nshared_old.proto:3:1: error house:MSG_GONE: message removed [base]\nshared_old.proto:4:3: error house:FIELD_GONE: field removed [base]\n"
	if out.String() != want || diag.String() != "" {
		t.Fatalf("out = %q diag = %q", out.String(), diag.String())
	}
	if fx.exists(t, "pb.lock") {
		t.Fatal("a file base pinned something")
	}
	if fx.read(t, "build/base.binpb") != set {
		t.Fatal("the set was rewritten")
	}
	// Every module's own entry enabling nothing, the root's rules
	// still judge the set's files of no module.
	fx.write(t, "pb.lint.yaml", "rulesets:\n  - path: example.com/house\n    alias: house\nbreaking:\n  base:\n    file: build/base.binpb\nmodules:\n  a:\n    enable: []\n  b:\n    enable: []\n")
	out.Reset()
	diag.Reset()
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); !errors.Is(err, ErrFindings) || out.String() != "legacy.proto: error house:PKG_GONE: package removed [base]\nlegacy.proto:1:1: error house:FILE_GONE: file removed [base]\nlegacy.proto:3:1: error house:MSG_GONE: message removed [base]\nlegacy.proto:4:3: error house:FIELD_GONE: field removed [base]\nnopkg.proto:1:1: error house:FILE_GONE: file removed [base]\nnopkg.proto:2:1: error house:MSG_GONE: message removed [base]\nnopkg.proto:3:3: error house:FIELD_GONE: field removed [base]\nshared_old.proto:1:1: error house:FILE_GONE: file removed [base]\nshared_old.proto:3:1: error house:MSG_GONE: message removed [base]\nshared_old.proto:4:3: error house:FIELD_GONE: field removed [base]\n" || diag.String() != "" {
		t.Fatalf("modules opted out: %v %q %q", err, out.String(), diag.String())
	}
	// An empty file is the empty set: every file new, every message
	// an addition, every package added to each module's set, and
	// nothing a deletion.
	fx.write(t, "pb.lint.yaml", "rulesets:\n  - path: example.com/house\n    alias: house\nbreaking:\n  base:\n    file: build/base.binpb\n")
	fx.write(t, "build/base.binpb", "")
	out.Reset()
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); !errors.Is(err, ErrFindings) || out.String() != "a.proto:6:1: warning house:NEW_MSG: message added\na.proto:13:1: warning house:NEW_MSG: message added\nb.proto:5:1: warning house:NEW_MSG: message added\nnopkg_a.proto:2:1: warning house:NEW_MSG: message added\nshared_a.proto:3:1: warning house:NEW_MSG: message added\nshared_b.proto:3:1: warning house:NEW_MSG: message added\nerror house:SET_NOADD: a package was added\nerror house:SET_NOADD: a package was added\n" {
		t.Fatalf("the empty set: %v %q", err, out.String())
	}
	// A file that is no descriptor set, and one that is absent, fail
	// naming the form.
	fx.write(t, "build/base.binpb", "not a set")
	out.Reset()
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); err == nil || !strings.Contains(err.Error(), "file build/base.binpb: not a descriptor set") {
		t.Fatalf("not a set: %v", err)
	}
	fx.write(t, "pb.lint.yaml", "rulesets:\n  - path: example.com/house\n    alias: house\nbreaking:\n  base:\n    file: build/none.binpb\n")
	if err := Breaking(ctx, fx.session(t, "."), BreakingDeps{}, &out, &diag); err == nil || !strings.Contains(err.Error(), "file build/none.binpb") {
		t.Fatalf("absent: %v", err)
	}
}
