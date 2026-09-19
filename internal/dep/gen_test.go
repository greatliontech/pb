package dep

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	iofs "io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-billy/v6/util"

	"github.com/greatliontech/pb/internal/lockfile"
	"github.com/greatliontech/pb/internal/plugexec"
	"github.com/greatliontech/pb/internal/pluglocal"
	"github.com/greatliontech/pb/internal/plugoci"
	"github.com/greatliontech/pb/internal/plugrun"
	"github.com/greatliontech/pb/internal/trust"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
	"pgregory.net/rapid"
)

type stubAcquirer struct {
	overrides []string
	got       []string
	acq       *plugoci.Acquired
	err       error
	onAcquire func()
}

func (s *stubAcquirer) Acquire(_ context.Context, ref string) (*plugoci.Acquired, error) {
	s.got = append(s.got, ref)
	if s.onAcquire != nil {
		s.onAcquire()
	}
	return s.acq, s.err
}

type stubRunner struct {
	spec plugrun.Spec
	res  *plugrun.Result
	err  error
}

func (s *stubRunner) Run(_ context.Context, spec plugrun.Spec) (*plugrun.Result, error) {
	s.spec = spec
	return s.res, s.err
}

func (s *stubRunner) Platform() (string, string) { return "linux", "amd64" }

// daemonStubRunner is a stub that runs daemon-local images.
type daemonStubRunner struct{ stubRunner }

func (*daemonStubRunner) RunsDaemonImages() {}

func respBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	resp := &pluginpb.CodeGeneratorResponse{}
	for name, content := range files {
		resp.File = append(resp.File, &pluginpb.CodeGeneratorResponse_File{Name: proto.String(name), Content: proto.String(content)})
	}
	b, err := proto.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func genFixture(t *testing.T, genYaml string) (*depFixture, *Session) {
	t.Helper()
	fx := newDep(t, map[string]string{
		"pb.work":     "use:\n  - a\n",
		"a/pb.yaml":   ws("example.com/a", ""),
		"a/x.proto":   "syntax = \"proto3\";\npackage a;\nmessage X {}\n",
		"pb.gen.yaml": genYaml,
	})
	return fx, fx.session(t, ".")
}

// The verb compiles, builds the request from the entry's opt, runs the
// plugin over the acquired image, honors the reported tier, and writes
// the response under out (REQ-gen-compile through
// REQ-gen-out-containment end to end).
func TestGenHappyPath(t *testing.T) {
	fx, s := genFixture(t, "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen/go\n    opt: k=v\n")
	acq := &stubAcquirer{acq: &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/plugin"}}, Pin: lockfile.PluginPin{Ref: "ghcr.io/o/p:v1", Scheme: lockfile.SchemeOCI}}}
	run := &stubRunner{res: &plugrun.Result{Stdout: respBytes(t, map[string]string{"a/x.pb.go": "code", "doc/readme.md": "d"}), Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}}
	var out strings.Builder
	if err := Gen(ctx, s, GenDeps{Acquirer: acq, Runner: run}, &out); err != nil {
		t.Fatal(err)
	}
	if got := fx.read(t, "gen/go/a/x.pb.go"); got != "code" {
		t.Fatalf("generated file = %q", got)
	}
	if got := fx.read(t, "gen/go/doc/readme.md"); got != "d" {
		t.Fatalf("nested file = %q", got)
	}
	var req pluginpb.CodeGeneratorRequest
	if err := proto.Unmarshal(run.spec.Stdin, &req); err != nil {
		t.Fatal(err)
	}
	if req.GetParameter() != "k=v" || len(req.GetFileToGenerate()) != 1 || req.GetFileToGenerate()[0] != "x.proto" {
		t.Fatalf("request = %+v", &req)
	}
	if !strings.Contains(out.String(), "2 file(s)") || !strings.Contains(out.String(), "(tier Strong, bounds cgroups)") {
		t.Fatalf("report = %q", out.String())
	}
	if !strings.Contains(strings.Join(acq.got, ","), "ghcr.io/o/p:v1") {
		t.Fatalf("acquired = %v", acq.got)
	}
}

// A reported tier below the floor refuses, naming both tiers and the
// explicit lowering path (REQ-plugin-min-tier, REQ-plugin-reported-tier).
func TestGenTierFloor(t *testing.T) {
	_, s := genFixture(t, "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n")
	acq := &stubAcquirer{acq: &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}}}
	run := &stubRunner{res: &plugrun.Result{Stdout: respBytes(t, nil), Tier: plugexec.TierOS, Bounds: plugrun.BoundsCgroups}}
	err := Gen(ctx, s, GenDeps{Acquirer: acq, Runner: run}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "tier OS, below the required Strong") || !strings.Contains(err.Error(), "lower the floor explicitly") {
		t.Fatalf("err = %v", err)
	}
	// The floor reaches the runner, and a runner refusing below it
	// before anything runs is reported with the same lowering path.
	if run.spec.MinTier != plugexec.TierStrong {
		t.Fatalf("runner received floor %q", run.spec.MinTier)
	}
	refusing := &stubRunner{err: fmt.Errorf("%w: this host reaches the os row; strong required", plugrun.ErrTierUnreachable)}
	err = Gen(ctx, s, GenDeps{Acquirer: acq, Runner: refusing}, &strings.Builder{})
	if !errors.Is(err, plugrun.ErrTierUnreachable) || !strings.Contains(err.Error(), "reaches the os row") || !strings.Contains(err.Error(), "lower the floor explicitly") {
		t.Fatalf("err = %v", err)
	}
	// An explicitly lowered floor accepts the same run.
	s.Client.Policy = &trust.Policy{Execution: trust.Execution{MinTier: plugexec.TierOS}}
	var out strings.Builder
	if err := Gen(ctx, s, GenDeps{Acquirer: acq, Runner: run}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "ghcr.io/o/p:v1: 0 file(s) into gen (tier OS, bounds cgroups)\n" {
		t.Fatalf("report = %q", out.String())
	}
}

// Containment refuses escaping and unclean names and insertion points,
// writing nothing (REQ-gen-out-containment).
func TestGenContainment(t *testing.T) {
	for _, hostile := range []string{"/abs.go", "../up.go", "a/../../up.go", "a//b.go", "..", ".", "./x.go"} {
		fx, s := genFixture(t, "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n")
		acq := &stubAcquirer{acq: &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}}}
		run := &stubRunner{res: &plugrun.Result{Stdout: respBytes(t, map[string]string{"ok.go": "x", hostile: "y"}), Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}}
		err := Gen(ctx, s, GenDeps{Acquirer: acq, Runner: run}, &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), "is not a clean relative path inside the output directory") {
			t.Fatalf("%q: err = %v", hostile, err)
		}
		if _, err := fx.ws.Stat("gen/ok.go"); err == nil {
			t.Fatalf("%q: partial write before refusal", hostile)
		}
	}

	// A response naming the same file twice is refused whole: protoc
	// rejects duplicate output names, and last-write-wins would let one
	// entry silently shadow another.
	fxDup, sDup := genFixture(t, "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n")
	dup := &pluginpb.CodeGeneratorResponse{File: []*pluginpb.CodeGeneratorResponse_File{
		{Name: proto.String("x.go"), Content: proto.String("one")},
		{Name: proto.String("x.go"), Content: proto.String("two")},
	}}
	db, _ := proto.Marshal(dup)
	runDup := &stubRunner{res: &plugrun.Result{Stdout: db, Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}}
	err := Gen(ctx, sDup, GenDeps{Acquirer: &stubAcquirer{acq: &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}}}, Runner: runDup}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), `names file "x.go" twice`) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, statErr := fxDup.ws.Stat("gen/x.go"); statErr == nil {
		t.Fatal("duplicate-name file written")
	}

	fx, s := genFixture(t, "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n")
	resp := &pluginpb.CodeGeneratorResponse{File: []*pluginpb.CodeGeneratorResponse_File{{Name: proto.String("x.go"), InsertionPoint: proto.String("imports"), Content: proto.String("y")}}}
	b, _ := proto.Marshal(resp)
	run := &stubRunner{res: &plugrun.Result{Stdout: b, Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}}
	err = Gen(ctx, s, GenDeps{Acquirer: &stubAcquirer{acq: &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}}}, Runner: run}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "insertion point") {
		t.Fatalf("insertion point: %v", err)
	}
	if _, statErr := fx.ws.Stat("gen/x.go"); statErr == nil {
		t.Fatal("insertion-point file written")
	}
}

// Scheme gating: a local entry is refused while the policy permits
// only oci; permitting local reaches the local arm, which needs the
// native runner.
func TestGenSchemeGate(t *testing.T) {
	_, s := genFixture(t, "plugins:\n  - local: protoc-gen-x\n    out: gen\n")
	err := Gen(ctx, s, GenDeps{}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "does not permit local-scheme") {
		t.Fatalf("err = %v", err)
	}
	s.Client.Policy = &trust.Policy{Execution: trust.Execution{Schemes: []string{"oci", "local"}}}
	err = Gen(ctx, s, GenDeps{}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "local plugins run on the native runner, and none is wired here") {
		t.Fatalf("err = %v", err)
	}
}

// The policy's limits reach the runner; a plugin's declared error and
// a missing gen file surface cleanly.
func TestGenPlumbing(t *testing.T) {
	_, s := genFixture(t, "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n")
	s.Client.Policy = &trust.Policy{Execution: trust.Execution{Limits: trust.Limits{Pids: 7}}}
	acq := &stubAcquirer{acq: &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}}}
	run := &stubRunner{res: &plugrun.Result{Stdout: respBytes(t, nil), Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}}
	if err := Gen(ctx, s, GenDeps{Acquirer: acq, Runner: run}, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if run.spec.Limits.Pids != 7 {
		t.Fatalf("limits did not reach the runner: %+v", run.spec.Limits)
	}

	declared := &pluginpb.CodeGeneratorResponse{Error: proto.String("cannot handle editions")}
	b, _ := proto.Marshal(declared)
	run2 := &stubRunner{res: &plugrun.Result{Stdout: b, Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}}
	err := Gen(ctx, s, GenDeps{Acquirer: acq, Runner: run2}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "cannot handle editions") {
		t.Fatalf("declared error: %v", err)
	}

	sNo := s
	_ = sNo
	fx2 := newDep(t, map[string]string{"pb.yaml": ws("example.com/a", "")})
	s2 := fx2.session(t, ".")
	err = Gen(ctx, s2, GenDeps{}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "reading pb.gen.yaml") {
		t.Fatalf("missing gen file: %v", err)
	}
}

// Property: a response file name is accepted exactly when it is a
// clean relative forward-slash path that stays inside the output
// directory (REQ-gen-out-containment's for-all form).
func TestGenContainmentProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		seg := rapid.SampledFrom([]string{"a", "b", "..", ".", "", "c.go", "..d"})
		n := rapid.IntRange(1, 4).Draw(rt, "n")
		parts := make([]string, n)
		for i := range parts {
			parts[i] = seg.Draw(rt, "seg")
		}
		name := strings.Join(parts, "/")
		if rapid.Bool().Draw(rt, "abs") {
			name = "/" + name
		}
		_, s := genFixture(t, "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n")
		run := &stubRunner{res: &plugrun.Result{Stdout: respBytes(t, map[string]string{name: "y"}), Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}}
		err := Gen(ctx, s, GenDeps{Acquirer: &stubAcquirer{acq: &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}}}, Runner: run}, &strings.Builder{})
		clean := name != "" && name != "." && name != ".." && !strings.HasPrefix(name, "/") && !strings.HasPrefix(name, "../") && pathpkg.Clean(name) == name
		if clean && err != nil {
			rt.Fatalf("clean %q refused: %v", name, err)
		}
		if !clean && (err == nil || !strings.Contains(err.Error(), "is not a clean relative path inside the output directory")) {
			rt.Fatalf("hostile %q accepted: %v", name, err)
		}
	})
}

// Every failure between parsing and writing propagates out of Gen:
// each row breaks one stage and the run reports it.
func TestGenFailurePaths(t *testing.T) {
	okAcq := func() *stubAcquirer {
		return &stubAcquirer{acq: &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}}}
	}
	okRun := func() *stubRunner {
		return &stubRunner{res: &plugrun.Result{Stdout: respBytes(t, nil), Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}}
	}
	genEntry := "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n"

	t.Run("gen file malformed", func(t *testing.T) {
		_, s := genFixture(t, "plugins: {\n")
		if err := Gen(ctx, s, GenDeps{}, &strings.Builder{}); err == nil {
			t.Fatal("malformed gen file accepted")
		}
	})
	t.Run("build list unresolvable", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":     "use:\n  - a\n",
			"a/pb.yaml":   ws("example.com/a", "  example.com/missing: v1.0.0\n"),
			"a/x.proto":   "syntax = \"proto3\";\npackage a;\nmessage X {}\n",
			"pb.gen.yaml": genEntry,
		})
		s := fx.session(t, ".")
		err := Gen(ctx, s, GenDeps{Acquirer: okAcq(), Runner: okRun()}, &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), "example.com/missing") {
			t.Fatalf("unresolvable build list: %v", err)
		}
	})
	t.Run("archive unavailable", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":     "use:\n  - a\n",
			"a/pb.yaml":   ws("example.com/a", "  example.com/m1: v1.0.0\n"),
			"a/x.proto":   "syntax = \"proto3\";\npackage a;\nmessage X {}\n",
			"pb.gen.yaml": genEntry,
		})
		fx.Endpoint("example.com/m1", "v1.0.0", "info", `{"version":"v1.0.0"}`)
		fx.Endpoint("example.com/m1", "v1.0.0", "mod", ws("example.com/m1", ""))
		s := fx.session(t, ".")
		err := Gen(ctx, s, GenDeps{Acquirer: okAcq(), Runner: okRun()}, &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), "example.com/m1") {
			t.Fatalf("missing archive: %v", err)
		}
	})
	t.Run("compile failure", func(t *testing.T) {
		fx := newDep(t, map[string]string{
			"pb.work":     "use:\n  - a\n",
			"a/pb.yaml":   ws("example.com/a", ""),
			"a/x.proto":   "this is not protobuf\n",
			"pb.gen.yaml": genEntry,
		})
		s := fx.session(t, ".")
		if err := Gen(ctx, s, GenDeps{Acquirer: okAcq(), Runner: okRun()}, &strings.Builder{}); err == nil {
			t.Fatal("uncompilable source accepted")
		}
	})
	t.Run("request build failure", func(t *testing.T) {
		// Grammar errors die at parse time; the build arm fires on
		// semantic failures against the compiled set.
		_, s := genFixture(t, genEntry+"overrides:\n  - files: x.proto\n    option: (m1.nosuch)\n    value: x\n")
		err := Gen(ctx, s, GenDeps{Acquirer: okAcq(), Runner: okRun()}, &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), "no extension") {
			t.Fatalf("bad override: %v", err)
		}
	})
	t.Run("file-set walk failure", func(t *testing.T) {
		// The one modfiles failure the resolver cannot pre-empt (it
		// fetches every archive while loading requirements) is the
		// workspace's own walk.
		if os.Geteuid() == 0 {
			t.Skip("root reads through permission bits")
		}
		dir := t.TempDir()
		fx := newDepOn(t, osfs.New(dir), map[string]string{
			"pb.work":           "use:\n  - a\n",
			"a/pb.yaml":         ws("example.com/a", ""),
			"a/x.proto":         "syntax = \"proto3\";\npackage a;\nmessage X {}\n",
			"a/blocked/y.proto": "syntax = \"proto3\";\npackage a;\n",
			"pb.gen.yaml":       genEntry,
		})
		s := fx.session(t, ".")
		if err := os.Chmod(filepath.Join(dir, "a/blocked"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(filepath.Join(dir, "a/blocked"), 0o755) })
		if err := Gen(ctx, s, GenDeps{Acquirer: okAcq(), Runner: okRun()}, &strings.Builder{}); err == nil {
			t.Fatal("unreadable workspace directory accepted")
		}
	})
	t.Run("acquire failure", func(t *testing.T) {
		_, s := genFixture(t, genEntry)
		err := Gen(ctx, s, GenDeps{Acquirer: &stubAcquirer{err: context.DeadlineExceeded}, Runner: okRun()}, &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
			t.Fatalf("acquire failure: %v", err)
		}
	})
	t.Run("runner failure", func(t *testing.T) {
		_, s := genFixture(t, genEntry)
		err := Gen(ctx, s, GenDeps{Acquirer: okAcq(), Runner: &stubRunner{err: context.DeadlineExceeded}}, &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), "generate: plugin ghcr.io/o/p:v1") {
			t.Fatalf("runner failure: %v", err)
		}
	})
}

// A pin recorded during acquisition persists with the run
// (REQ-lock-first-use): the saved lockfile carries it.
func TestGenPersistsPins(t *testing.T) {
	fx, s := genFixture(t, "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n")
	acq := &stubAcquirer{
		acq: &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}},
		onAcquire: func() {
			if err := s.Lock.AddPlugin(lockfile.PluginPin{Ref: "ghcr.io/o/p:v1", Scheme: lockfile.SchemeOCI, Digest: "sha256:" + strings.Repeat("ab", 32)}); err != nil {
				t.Fatal(err)
			}
		},
	}
	run := &stubRunner{res: &plugrun.Result{Stdout: respBytes(t, nil), Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}}
	if err := Gen(ctx, s, GenDeps{Acquirer: acq, Runner: run}, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if got := fx.read(t, "pb.lock"); !strings.Contains(got, "ghcr.io/o/p:v1") {
		t.Fatalf("pin not persisted: %q", got)
	}
}

// The write path over a real filesystem: nested directories are
// created, and a filesystem refusal — a file standing where a
// directory is needed, a directory standing where the file goes —
// surfaces as that plugin's error (REQ-gen-out-containment's write
// half; in-memory filesystems create directories implicitly and
// would hide all three behaviors).
func TestGenWriteRealFS(t *testing.T) {
	fixture := func(t *testing.T) (*depFixture, *Session) {
		t.Helper()
		fx := newDepOn(t, osfs.New(t.TempDir()), map[string]string{
			"pb.work":     "use:\n  - a\n",
			"a/pb.yaml":   ws("example.com/a", ""),
			"a/x.proto":   "syntax = \"proto3\";\npackage a;\nmessage X {}\n",
			"pb.gen.yaml": "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n",
		})
		return fx, fx.session(t, ".")
	}
	deps := func(files map[string]string) GenDeps {
		return GenDeps{
			Acquirer: &stubAcquirer{acq: &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}}},
			Runner:   &stubRunner{res: &plugrun.Result{Stdout: respBytes(t, files), Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}},
		}
	}

	t.Run("nested directories created", func(t *testing.T) {
		fx, s := fixture(t)
		if err := Gen(ctx, s, deps(map[string]string{"sub/dir/x.go": "y"}), &strings.Builder{}); err != nil {
			t.Fatal(err)
		}
		if got := fx.read(t, "gen/sub/dir/x.go"); got != "y" {
			t.Fatalf("nested file = %q", got)
		}
	})
	t.Run("file where a directory is needed", func(t *testing.T) {
		fx, s := fixture(t)
		if err := util.WriteFile(fx.ws, "gen/sub", []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := Gen(ctx, s, deps(map[string]string{"sub/x.go": "y"}), &strings.Builder{}); err == nil {
			t.Fatal("write through a file succeeded")
		}
	})
	t.Run("directory where the file goes", func(t *testing.T) {
		fx, s := fixture(t)
		if err := fx.ws.MkdirAll("gen/x.go", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := Gen(ctx, s, deps(map[string]string{"x.go": "y"}), &strings.Builder{}); err == nil {
			t.Fatal("write over a directory succeeded")
		}
	})
}

// A response name whose directory components pass through a symlink
// below the output directory is refused before anything is written
// (REQ-gen-out-containment): the written name is contained, the
// resolved path would not be. In-memory and real filesystems both,
// and both symlink forms: the real-filesystem half is rooted at "/"
// with the workspace under a temp dir, exactly as the CLI roots its
// working tree, where billy's own bound still refuses an absolute
// symlink target but follows a relative one — the form repositories
// commit — so the relative row is the one only pb's guard can refuse.
func TestGenSymlinkEscapeRefused(t *testing.T) {
	type half struct {
		fs       func(t *testing.T) (billy.Filesystem, string)
		relative bool
	}
	for name, h := range map[string]half{
		"memfs": {func(t *testing.T) (billy.Filesystem, string) { return memfs.New(), "" }, false},
		"osfs absolute": {func(t *testing.T) (billy.Filesystem, string) {
			return osfs.New("/"), strings.TrimPrefix(t.TempDir(), "/")
		}, false},
		"osfs relative": {func(t *testing.T) (billy.Filesystem, string) {
			return osfs.New("/"), strings.TrimPrefix(t.TempDir(), "/")
		}, true},
	} {
		t.Run(name, func(t *testing.T) {
			outside := t.TempDir()
			fs, root := h.fs(t)
			at := func(p string) string { return pathpkg.Join(root, p) }
			fx := newDepOn(t, fs, map[string]string{
				at("pb.work"):     "use:\n  - a\n",
				at("a/pb.yaml"):   ws("example.com/a", ""),
				at("a/x.proto"):   "syntax = \"proto3\";\npackage a;\nmessage X {}\n",
				at("pb.gen.yaml"): "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n",
			})
			if err := fx.ws.MkdirAll(at("gen"), 0o755); err != nil {
				t.Fatal(err)
			}
			target := outside
			if h.relative {
				rel, err := filepath.Rel("/"+at("gen"), outside)
				if err != nil {
					t.Fatal(err)
				}
				target = rel
			}
			if err := fx.ws.Symlink(target, at("gen/sub")); err != nil {
				t.Fatal(err)
			}
			s := fx.session(t, pathpkg.Join(root, "."))
			deps := GenDeps{
				Acquirer: &stubAcquirer{acq: &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}}},
				Runner:   &stubRunner{res: &plugrun.Result{Stdout: respBytes(t, map[string]string{"ok.go": "x", "sub/deep/x.go": "y"}), Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}},
			}
			err := Gen(ctx, s, deps, &strings.Builder{})
			if err == nil || !strings.Contains(err.Error(), `passes through "sub", a symlink`) {
				t.Fatalf("symlinked directory: %v", err)
			}
			if _, statErr := fx.ws.Stat(at("gen/ok.go")); statErr == nil {
				t.Fatal("partial write before the symlink refusal")
			}
			if entries, _ := os.ReadDir(outside); len(entries) != 0 {
				t.Fatalf("wrote outside the output directory: %v", entries)
			}
			// A symlink at the leaf is replaced by the atomic rename,
			// never followed: the write stays inside.
			if err := fx.ws.Symlink(filepath.Join(outside, "leaf"), at("gen/leaf.go")); err != nil {
				t.Fatal(err)
			}
			deps.Runner = &stubRunner{res: &plugrun.Result{Stdout: respBytes(t, map[string]string{"leaf.go": "z"}), Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}}
			if err := Gen(ctx, s, deps, &strings.Builder{}); err != nil {
				t.Fatal(err)
			}
			if fi, err := fx.ws.Lstat(at("gen/leaf.go")); err != nil || fi.Mode()&iofs.ModeSymlink != 0 {
				t.Fatalf("leaf symlink not replaced: %v %v", fi, err)
			}
			if _, err := os.Stat(filepath.Join(outside, "leaf")); err == nil {
				t.Fatal("write followed the leaf symlink outside")
			}
		})
	}
}

// The request handed to a plugin is byte-identical across runs
// (REQ-gen-request-determinism).
func TestGenRequestDeterministic(t *testing.T) {
	gen := "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    opt: k=v\noverrides:\n  - files: x.proto\n    option: go_package\n    value: example.com/x\n"
	var runs [][]byte
	for i := 0; i < 2; i++ {
		_, s := genFixture(t, gen)
		run := &stubRunner{res: &plugrun.Result{Stdout: respBytes(t, nil), Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}}
		if err := Gen(ctx, s, GenDeps{Acquirer: &stubAcquirer{acq: &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}}}, Runner: run}, &strings.Builder{}); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, run.spec.Stdin)
	}
	if !bytes.Equal(runs[0], runs[1]) || len(runs[0]) == 0 {
		t.Fatalf("requests differ across runs (%d vs %d bytes)", len(runs[0]), len(runs[1]))
	}
}

// Every arm of the verb names the failing entry (REQ-gen-verb), and
// the runner's report must carry a tier and a bounds mechanism.
func TestGenNamesEntry(t *testing.T) {
	okAcq := &stubAcquirer{acq: &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}}}
	_, s := genFixture(t, "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\noverrides:\n  - files: x.proto\n    option: (m1.nosuch)\n    value: x\n")
	err := Gen(ctx, s, GenDeps{Acquirer: okAcq, Runner: &stubRunner{}}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "generate: plugin ghcr.io/o/p:v1: ") || !strings.Contains(err.Error(), "no extension") {
		t.Fatalf("request build arm: %v", err)
	}
	_, s = genFixture(t, "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n")
	err = Gen(ctx, s, GenDeps{Acquirer: &stubAcquirer{err: errors.New("registry down")}, Runner: &stubRunner{}}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "generate: plugin ghcr.io/o/p:v1: registry down") {
		t.Fatalf("acquire arm: %v", err)
	}
	for _, res := range []*plugrun.Result{
		{Stdout: respBytes(t, nil), Tier: plugexec.TierStrong},
		{Stdout: respBytes(t, nil), Bounds: plugrun.BoundsCgroups},
		{Stdout: respBytes(t, nil), Tier: "Absolute", Bounds: plugrun.BoundsCgroups},
	} {
		_, s = genFixture(t, "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n")
		err = Gen(ctx, s, GenDeps{Acquirer: okAcq, Runner: &stubRunner{res: res}}, &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), "generate: plugin ghcr.io/o/p:v1: the runner reported no sandbox tier or bounds mechanism") {
			t.Fatalf("runner report %+v: %v", res, err)
		}
	}
}

// Acquisition precedes every execution, and a first-use pin persists
// even when a later entry's acquisition fails (REQ-gen-verb,
// REQ-plugin-digest-pin): the next run must not re-resolve the tag.
func TestGenPinsPersistAcrossLaterFailure(t *testing.T) {
	fx, s := genFixture(t, "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n  - ref: ghcr.io/o/q:v1\n    out: gen\n")
	var acquired []string
	acq := &stubAcquirer{}
	acq.onAcquire = func() {
		ref := acq.got[len(acq.got)-1]
		acquired = append(acquired, ref)
		if ref == "ghcr.io/o/q:v1" {
			acq.err = errors.New("registry down")
			return
		}
		acq.acq = &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}}
		if err := s.Lock.AddPlugin(lockfile.PluginPin{Ref: ref, Scheme: lockfile.SchemeOCI, Digest: "sha256:" + strings.Repeat("ab", 32)}); err != nil {
			t.Fatal(err)
		}
	}
	run := &stubRunner{res: &plugrun.Result{Stdout: respBytes(t, nil), Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}}
	err := Gen(ctx, s, GenDeps{Acquirer: acq, Runner: run}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "generate: plugin ghcr.io/o/q:v1: registry down") {
		t.Fatalf("err = %v", err)
	}
	if run.spec.Stdin != nil {
		t.Fatal("a plugin ran before every acquisition completed")
	}
	if got := fx.read(t, "pb.lock"); !strings.Contains(got, "ghcr.io/o/p:v1") {
		t.Fatalf("first entry's pin lost when the second failed: %q", got)
	}
}

func (s *stubAcquirer) AcquireOverride(_ context.Context, ref, source string) (*plugoci.Acquired, error) {
	s.overrides = append(s.overrides, ref+"="+source)
	return s.acq, s.err
}

type stubLocal struct {
	value string
	acq   *pluglocal.Acquired
	err   error
}

func (s *stubLocal) Acquire(_ context.Context, value string) (*pluglocal.Acquired, error) {
	s.value = value
	return s.acq, s.err
}

// A local entry resolves through the local acquirer, runs on the
// native runner with no rootfs and no floor, and is reported at the
// tier that runner reports; without a native runner it is refused
// before anything runs; the oci entries still run on the selected
// runner.
func TestGenLocalEntry(t *testing.T) {
	_, s := genFixture(t, "plugins:\n  - local: tools/gen\n    out: gen\n  - ref: ghcr.io/o/p:v1\n    out: gen2\n")
	s.Client.Policy = &trust.Policy{Execution: trust.Execution{Schemes: []string{plugexec.SchemeOCI, plugexec.SchemeLocal}}}
	local := &stubLocal{acq: &pluglocal.Acquired{Process: plugexec.Process{Argv: []string{"/abs/tools/gen"}}}}
	localRun := &stubRunner{res: &plugrun.Result{Stdout: respBytes(t, map[string]string{"a.go": "x"}), Tier: plugexec.TierMinimal, Bounds: plugrun.BoundsRlimits}}
	ociRun := &stubRunner{res: &plugrun.Result{Stdout: respBytes(t, nil), Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}}
	acq := &stubAcquirer{acq: &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}}}
	var out strings.Builder
	if err := Gen(ctx, s, GenDeps{Acquirer: acq, Runner: ociRun, Local: &LocalDeps{Acquirer: local, Runner: localRun}}, &out); err != nil {
		t.Fatal(err)
	}
	if local.value != "tools/gen" {
		t.Fatalf("local acquirer got %q", local.value)
	}
	if localRun.spec.Scheme != plugexec.SchemeLocal || localRun.spec.Rootfs != "" || localRun.spec.MinTier != plugexec.TierNone || localRun.spec.Process.Argv[0] != "/abs/tools/gen" {
		t.Fatalf("local run spec = %+v", localRun.spec)
	}
	if ociRun.spec.Scheme != plugexec.SchemeOCI || ociRun.spec.Rootfs != "/r" || ociRun.spec.MinTier != plugexec.TierStrong {
		t.Fatalf("oci run spec = %+v", ociRun.spec)
	}
	if !strings.Contains(out.String(), "tools/gen: 1 file(s) into gen (tier Minimal, bounds rlimits)") {
		t.Fatalf("report = %q", out.String())
	}
	err := Gen(ctx, s, GenDeps{Acquirer: acq, Runner: ociRun}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "local plugins run on the native runner, and none is wired here") {
		t.Fatalf("no native runner: %v", err)
	}
}

// An override substitutes only the content that executes: a layout or
// archive goes through the override acquisition, a daemon-local image
// rides the seam to the runner as it is; each is reported on standard
// error; the policy can forbid them; a key naming no oci entry is an
// error; the lockfile is untouched in both directions.
func TestGenOverrides(t *testing.T) {
	_, s := genFixture(t, "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n  - ref: ghcr.io/o/q:v1\n    out: gen2\n")
	acq := &stubAcquirer{acq: &plugoci.Acquired{Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}}}
	run := &stubRunner{res: &plugrun.Result{Stdout: respBytes(t, nil), Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}}
	var diag strings.Builder
	deps := GenDeps{Acquirer: acq, Runner: run, Overrides: map[string]string{"ghcr.io/o/p:v1": "/tmp/layout"}, Diagnostics: &diag}
	if err := Gen(ctx, s, deps, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if len(acq.overrides) != 1 || acq.overrides[0] != "ghcr.io/o/p:v1=/tmp/layout" {
		t.Fatalf("override acquisitions = %v", acq.overrides)
	}
	if diag.String() != "overriding ghcr.io/o/p:v1 with /tmp/layout\n" {
		t.Fatalf("diagnostics = %q", diag.String())
	}
	// A daemon-local image reaches the runner as the world — a runner
	// that runs daemon images; any other refuses it before anything
	// runs, naming the way to select the docker runner.
	diag.Reset()
	runs := &daemonStubRunner{stubRunner{res: run.res}}
	deps = GenDeps{Acquirer: acq, Runner: runs, Overrides: map[string]string{"ghcr.io/o/q:v1": "docker://plugins/q:dev"}, Diagnostics: &diag}
	if err := Gen(ctx, s, deps, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	plain := &stubRunner{res: run.res}
	err := Gen(ctx, s, GenDeps{Acquirer: acq, Runner: plain, Overrides: map[string]string{"ghcr.io/o/q:v1": "docker://plugins/q:dev"}}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "only the docker runner runs (select it with --runner docker)") || plain.spec.Scheme != "" {
		t.Fatalf("daemon image under another runner: %v (ran: %+v)", err, plain.spec)
	}
	if runs.spec.Image != "plugins/q:dev" || runs.spec.Rootfs != "" || len(runs.spec.Process.Argv) != 0 {
		t.Fatalf("daemon-local spec = %+v", runs.spec)
	}
	if !strings.Contains(diag.String(), "overriding ghcr.io/o/q:v1 with the daemon-local image plugins/q:dev") {
		t.Fatalf("diagnostics = %q", diag.String())
	}
	// A key naming no oci entry — a local entry included — and a
	// policy forbidding overrides.
	deps = GenDeps{Acquirer: acq, Runner: run, Overrides: map[string]string{"ghcr.io/o/none:v1": "/tmp/x"}}
	if err := Gen(ctx, s, deps, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "names no oci plugin entry") {
		t.Fatalf("unknown key: %v", err)
	}
	_, sl := genFixture(t, "plugins:\n  - local: tools/gen\n    out: gen\n")
	sl.Client.Policy = &trust.Policy{Execution: trust.Execution{Schemes: []string{plugexec.SchemeOCI, plugexec.SchemeLocal}}}
	local := &stubLocal{acq: &pluglocal.Acquired{Process: plugexec.Process{Argv: []string{"/abs/tools/gen"}}}}
	deps = GenDeps{Acquirer: acq, Runner: run, Local: &LocalDeps{Acquirer: local, Runner: run}, Overrides: map[string]string{"tools/gen": "/tmp/x"}}
	if err := Gen(ctx, sl, deps, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "names no oci plugin entry") {
		t.Fatalf("local key: %v", err)
	}
	off := false
	s.Client.Policy = &trust.Policy{Execution: trust.Execution{PluginOverrides: &off}}
	deps = GenDeps{Acquirer: acq, Runner: run, Overrides: map[string]string{"ghcr.io/o/p:v1": "/tmp/layout"}}
	if err := Gen(ctx, s, deps, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "forbids plugin overrides") {
		t.Fatalf("forbidden: %v", err)
	}
}

// An acquisition that yields a digest for the daemon to pull reaches
// the runner as a pulled image with no rootfs and no process of pb's
// — the daemon applies the image's own — and only a runner that runs
// daemon images may receive it (REQ-plugin-core-verifies).
func TestGenPulledImage(t *testing.T) {
	_, s := genFixture(t, "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n")
	image := "ghcr.io/o/p@sha256:" + strings.Repeat("ab", 32)
	acq := &stubAcquirer{acq: &plugoci.Acquired{Image: image, Pin: lockfile.PluginPin{Ref: "ghcr.io/o/p:v1", Scheme: lockfile.SchemeOCI}}}
	run := &daemonStubRunner{stubRunner{res: &plugrun.Result{Stdout: respBytes(t, nil), Tier: plugexec.TierStrong, Bounds: plugrun.BoundsCgroups}}}
	if err := Gen(ctx, s, GenDeps{Acquirer: acq, Runner: run}, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if run.spec.Image != image || !run.spec.Pull || run.spec.Rootfs != "" || len(run.spec.Process.Argv) != 0 {
		t.Fatalf("runner received %+v", run.spec)
	}
	plain := &stubRunner{res: run.res}
	err := Gen(ctx, s, GenDeps{Acquirer: acq, Runner: plain}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "runs no daemon images") || plain.spec.Image != "" {
		t.Fatalf("a runner without a daemon ran a pulled image: %v %+v", err, plain.spec)
	}
}
