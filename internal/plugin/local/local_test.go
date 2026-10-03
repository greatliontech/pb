package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/flock"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/pb/internal/testing/flocktest"
)

var ctx = context.Background()

func fixture(t *testing.T) (root string, a *Acquirer) {
	t.Helper()
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tools", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tools", "bin", "gen"), []byte("#!/bin/sh\necho gen\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tools", "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	// The PATH binary bears the host's executable suffix, so the
	// host's own lookup finds it: the fixture's acquirer resolves for
	// the host.
	name := "protoc-gen-x"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho x\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	a = &Acquirer{Root: root, Runs: t.TempDir(), Lock: &lockfile.File{}, Policy: &trust.Policy{}, platform: plugin.HostPlatform()}
	t.Cleanup(func() { _ = a.Release() })
	return root, a
}

// isCopy fails unless argv names a copy under the acquirer's run of
// the binary at src: the same name, the same bytes, elsewhere.
func isCopy(t *testing.T, a *Acquirer, argv0, src string) {
	t.Helper()
	rel, err := filepath.Rel(a.Runs, argv0)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.Base(argv0) != filepath.Base(src) || argv0 == src {
		t.Fatalf("argv[0] = %s, want a copy of %s under %s", argv0, src, a.Runs)
	}
	want, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(argv0)
	if err != nil || string(got) != string(want) {
		t.Fatalf("the copy's bytes: %q, %v; want %q", got, err, want)
	}
}

// A bare name resolves on PATH exactly as written; a path resolves
// relative to the root, absolute allowed; a failure names the
// search and nothing else is tried (REQ-plugin-local-resolution).
func TestResolve(t *testing.T) {
	root, a := fixture(t)
	if p, err := a.Resolve("protoc-gen-x"); err != nil || strings.TrimSuffix(filepath.Base(p), ".exe") != "protoc-gen-x" {
		t.Fatalf("bare name: %q %v", p, err)
	}
	if p, err := a.Resolve("tools/bin/gen"); err != nil || p != filepath.Join(root, "tools", "bin", "gen") {
		t.Fatalf("root-relative: %q %v", p, err)
	}
	if p, err := a.Resolve("./tools/bin/gen"); err != nil || p != filepath.Join(root, "tools", "bin", "gen") {
		t.Fatalf("dot-relative: %q %v", p, err)
	}
	// An absolute value is spelled with slashes on every platform; the
	// resolved path is the host's.
	abs := filepath.Join(root, "tools", "bin", "gen")
	if p, err := a.Resolve(filepath.ToSlash(abs)); err != nil || p != abs {
		t.Fatalf("absolute: %q %v", p, err)
	}
	cases := []struct{ value, text string }{
		{"gen", `"gen" not found on PATH (looked up exactly as "gen`},
		{"x", `"x" not found on PATH`},
		{"tools/bin/missing", "resolved relative to " + root},
		{"tools/bin", "not a regular file"},
		{`tools\bin\gen`, "holds a backslash"},
		{`C:\tools\gen.exe`, "holds a backslash"},
	}
	if runtime.GOOS != "windows" {
		cases = append(cases, struct{ value, text string }{"tools/notes.txt", "not executable"})
	}
	for _, c := range cases {
		if _, err := a.Resolve(c.value); err == nil || !strings.Contains(err.Error(), c.text) {
			t.Errorf("Resolve(%q) = %v, want %q", c.value, err, c.text)
		}
	}
	// On windows a bare name resolves to the name with .exe appended,
	// unless it already ends in .exe in any case, found as a regular
	// file of exactly that spelling in a PATH directory: a decoy the
	// platform would accept (gen.exe.cmd, a trailing dot dropped, the
	// working directory) never resolves; a path value is not held to
	// an executable bit, which the platform lacks (platforms.md
	// REQ-plat-local-runner). The walk is the acquirer's own, so it
	// runs on every host over a PATH of this test's making.
	first, second := t.TempDir(), t.TempDir()
	for _, f := range []string{filepath.Join(first, "gen.exe.cmd"), filepath.Join(first, "gen.exe.bat"), filepath.Join(second, "gen.exe"), filepath.Join(second, "Tool.EXE"), filepath.Join(first, "dot.exe")} {
		if err := os.WriteFile(f, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(second, "dir.exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	// An empty PATH element, which the platform reads as the working
	// directory, is passed over: a decoy there never resolves.
	decoy := t.TempDir()
	if err := os.WriteFile(filepath.Join(decoy, "gen.exe"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(decoy)
	// A relative PATH entry (".", "bin") would name a file by the
	// working directory, which the pinned hash and the run would read
	// differently: passed over as the empty one is.
	pathList := strings.Join([]string{first, "", ".", second}, string(os.PathListSeparator))
	w := &Acquirer{Root: root, Lock: a.Lock, Policy: a.Policy, platform: plugin.Platform{OS: "windows", Arch: "amd64"}, env: func(k string) string {
		if k == "PATH" {
			return pathList
		}
		return ""
	}}
	// Case is the filesystem's: a case-insensitive one answers for a
	// case variant with its one file, a case-sensitive one with none.
	folds := false
	if _, err := os.Stat(filepath.Join(second, "GEN.EXE")); err == nil {
		folds = true
	}
	winCases := map[string]string{"gen": filepath.Join(second, "gen.exe"), "gen.exe": filepath.Join(second, "gen.exe"), "GEN.EXE": "", "Tool.EXE": filepath.Join(second, "Tool.EXE"), "tool": "", "dir": "", "gen.cmd": "", "protoc-gen-go.v2": ""}
	if folds {
		winCases["GEN.EXE"], winCases["tool"] = filepath.Join(second, "GEN.EXE"), filepath.Join(second, "tool.exe")
	}
	for value, want := range winCases {
		p, err := w.Resolve(value)
		if want == "" {
			if err == nil || !strings.Contains(err.Error(), "not found on PATH (looked up exactly as") {
				t.Errorf("windows %q: %q %v, want a refusal", value, p, err)
			}
			continue
		}
		if err != nil || p != want {
			t.Errorf("windows %q: %q %v, want %q", value, p, err, want)
		}
	}
	if _, err := w.Resolve("dot."); err == nil || !strings.Contains(err.Error(), "ends in a dot") {
		t.Errorf("windows trailing dot: %v", err)
	}
	if p, err := w.Resolve("tools/notes.txt"); err != nil || p != filepath.Join(root, "tools", "notes.txt") {
		t.Errorf("windows path value without an executable bit: %q %v", p, err)
	}
}

// First use pins the content hash under the host platform; a later
// use with the same bytes at another location is a non-event; a
// differing hash fails naming both; a second platform adds its key;
// a policy disabling local pinning records nothing
// (REQ-plugin-local-pin).
func TestAcquirePins(t *testing.T) {
	root, a := fixture(t)
	got, err := a.Acquire(ctx, "tools/bin/gen", nil)
	if err != nil {
		t.Fatal(err)
	}
	pin, ok := a.Lock.Plugin("tools/bin/gen", lockfile.SchemeLocal)
	if got.Image != nil {
		t.Fatalf("a host binary carries image facts: %+v", got.Image)
	}
	host := plugin.HostPlatform().String()
	if !ok || len(pin.Binary) != 1 || !strings.HasPrefix(pin.Binary[host], "sha256:") || len(got.Process.Argv) != 1 || got.Process.WorkDir != root {
		t.Fatalf("first use: %+v %+v", pin, got)
	}
	isCopy(t, a, got.Process.Argv[0], filepath.Join(root, "tools", "bin", "gen"))
	hash := pin.Binary[host]
	// The same bytes elsewhere: a non-event.
	if err := os.Rename(filepath.Join(root, "tools", "bin", "gen"), filepath.Join(root, "gen")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "gen"), filepath.Join(root, "tools", "bin", "gen")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Acquire(ctx, "tools/bin/gen", nil); err != nil {
		t.Fatalf("moved binary, same bytes: %v", err)
	}
	// Other bytes: a mismatch naming both hashes.
	if err := os.WriteFile(filepath.Join(root, "gen"), []byte("#!/bin/sh\necho other\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = a.Acquire(ctx, "tools/bin/gen", nil)
	if !errors.Is(err, lockfile.ErrPinMismatch) || !strings.Contains(err.Error(), hash) || !strings.Contains(err.Error(), "resolved sha256:") {
		t.Fatalf("changed binary: %v", err)
	}
	// Another platform's first use adds its key: a platform other than
	// the host's, of the host's operating system so the path value
	// resolves the same way.
	otherPlatform := plugin.Platform{OS: runtime.GOOS, Arch: "arm64"}
	if plugin.HostPlatform().Arch == "arm64" {
		otherPlatform.Arch = "amd64"
	}
	other := &Acquirer{Root: root, Runs: a.Runs, Lock: a.Lock, Policy: a.Policy, platform: otherPlatform}
	defer other.Release()
	if _, err := other.Acquire(ctx, "tools/bin/gen", nil); err != nil {
		t.Fatal(err)
	}
	pin, _ = a.Lock.Plugin("tools/bin/gen", lockfile.SchemeLocal)
	if len(pin.Binary) != 2 || pin.Binary[host] != hash {
		t.Fatalf("second platform: %+v", pin)
	}
	// Pinning disabled: nothing recorded, nothing checked.
	off := false
	unpinned := &Acquirer{Root: root, Runs: a.Runs, Lock: &lockfile.File{}, Policy: &trust.Policy{Execution: trust.Execution{LocalPin: &off}}, platform: plugin.HostPlatform()}
	defer unpinned.Release()
	if _, err := unpinned.Acquire(ctx, "tools/bin/gen", nil); err != nil {
		t.Fatal(err)
	}
	if len(unpinned.Lock.Plugins) != 0 {
		t.Fatalf("a pin was recorded with pinning disabled: %+v", unpinned.Lock.Plugins)
	}
}

// A command with arguments runs as the resolved command followed by
// the arguments verbatim, from the resolution root; the pin is the
// command's alone, shared by every entry naming it whatever the
// arguments, and an argument naming a path is neither resolved nor
// checked (REQ-plugin-local-resolution, REQ-plugin-local-pin).
func TestAcquireWithArguments(t *testing.T) {
	root, a := fixture(t)
	args := []string{"tool", "web/no/such/script.js", "--flag with space", ""}
	got, err := a.Acquire(ctx, "protoc-gen-x", args)
	if err != nil {
		t.Fatal(err)
	}
	exe := "protoc-gen-x"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	isCopy(t, a, got.Process.Argv[0], filepath.Join(os.Getenv("PATH"), exe))
	if strings.Join(got.Process.Argv[1:], "\x00") != strings.Join(args, "\x00") || got.Process.WorkDir != root {
		t.Fatalf("process = %+v, want the arguments %q after the copy, from %s", got.Process, args, root)
	}
	pin, ok := a.Lock.Plugin("protoc-gen-x", lockfile.SchemeLocal)
	if !ok || len(a.Lock.Plugins) != 1 {
		t.Fatalf("pin = %+v %v; plugins %+v", pin, ok, a.Lock.Plugins)
	}
	// The same command with other arguments, and with none: one pin.
	if _, err := a.Acquire(ctx, "protoc-gen-x", []string{"other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Acquire(ctx, "protoc-gen-x", nil); err != nil {
		t.Fatal(err)
	}
	if len(a.Lock.Plugins) != 1 {
		t.Fatalf("arguments changed the pin: %+v", a.Lock.Plugins)
	}
	// The arguments are the caller's: the acquisition never aliases
	// them.
	args[0] = "changed"
	if got.Process.Argv[1] != "tool" {
		t.Fatal("the process shares the caller's argument slice")
	}
}

// The pin key is the host platform spelled <os>/<arch>, and the
// production acquirer keys by it; a root that is not an absolute host
// path is refused before any lookup; the legal path spellings pin.
func TestPlatformAndRoot(t *testing.T) {
	if plugin.HostPlatform().String() != runtime.GOOS+"/"+runtime.GOARCH {
		t.Fatalf("host platform = %q", plugin.HostPlatform())
	}
	root, _ := fixture(t)
	a := &Acquirer{Root: root, Runs: t.TempDir(), Lock: &lockfile.File{}, Policy: &trust.Policy{}}
	defer a.Release()
	for _, value := range []string{"./tools/bin/gen", filepath.ToSlash(filepath.Join(root, "tools", "bin", "gen"))} {
		if _, err := a.Acquire(ctx, value, nil); err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		pin, ok := a.Lock.Plugin(value, lockfile.SchemeLocal)
		if !ok || pin.Binary[plugin.HostPlatform().String()] == "" {
			t.Fatalf("%q pinned as %+v under %q", value, pin, plugin.HostPlatform().String())
		}
	}
	relative := &Acquirer{Root: "tmp/ws", Runs: t.TempDir(), Lock: &lockfile.File{}, Policy: &trust.Policy{}}
	if _, err := relative.Resolve("tools/bin/gen"); err == nil || !strings.Contains(err.Error(), "not an absolute host path") {
		t.Fatalf("relative root: %v", err)
	}
	if _, err := relative.Resolve("protoc-gen-x"); err == nil || !strings.Contains(err.Error(), "not an absolute host path") {
		t.Fatalf("relative root, bare name: %v", err)
	}
}

// The copy runs under the caller's context: a cancelled acquisition
// ends without a pin, and its run's directory goes with the release.
func TestAcquireHonoursContext(t *testing.T) {
	_, a := fixture(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Acquire(cancelled, "tools/bin/gen", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquisition: %v", err)
	}
	if len(a.Lock.Plugins) != 0 {
		t.Fatal("a cancelled acquisition recorded a pin")
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(a.Runs); err != nil || len(entries) != 0 {
		t.Fatalf("the runs directory after the release: %v, %v", entries, err)
	}
}

// The acquisition's copy is the run's own: the run's directory under
// Runs holds it, held by its lock while the run lives, removed with
// every copy by the release; the pin names the copy's bytes, so a
// binary rebuilt at its place after the acquisition is what the
// next acquisition refuses, not what this run executes
// (REQ-plugin-local-pin).
func TestRunOwnsTheCopy(t *testing.T) {
	root, a := fixture(t)
	src := filepath.Join(root, "tools", "bin", "gen")
	got, err := a.Acquire(ctx, "tools/bin/gen", nil)
	if err != nil {
		t.Fatal(err)
	}
	isCopy(t, a, got.Process.Argv[0], src)
	runs, err := os.ReadDir(a.Runs)
	if err != nil || len(runs) != 1 {
		t.Fatalf("the runs directory: %v, %v; want the run's own", runs, err)
	}
	run := filepath.Join(a.Runs, runs[0].Name())
	if !strings.HasPrefix(got.Process.Argv[0], run+string(filepath.Separator)) {
		t.Fatalf("the copy %s is not under the run's directory %s", got.Process.Argv[0], run)
	}
	if !flocktest.Held(t, filepath.Join(run, lockName)) {
		t.Fatal("the live run's directory is not held by its lock")
	}
	// A second entry naming another binary: a copy of its own, under
	// its own name, beside the first.
	other, err := a.Acquire(ctx, "protoc-gen-x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(filepath.Dir(other.Process.Argv[0])) != run || filepath.Dir(other.Process.Argv[0]) == filepath.Dir(got.Process.Argv[0]) {
		t.Fatalf("the second copy %s, want one of its own under %s", other.Process.Argv[0], run)
	}
	// The binary rebuilt at its place: the copy stands, its bytes the
	// pin's; the next acquisition of the place refuses the new bytes.
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho rebuilt\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(got.Process.Argv[0]); err != nil || string(b) != "#!/bin/sh\necho gen\n" {
		t.Fatalf("the copy after the rebuild: %q, %v", b, err)
	}
	if _, err := a.Acquire(ctx, "tools/bin/gen", nil); !errors.Is(err, lockfile.ErrPinMismatch) {
		t.Fatalf("the rebuilt binary acquired again: %v", err)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(run); !os.IsNotExist(err) {
		t.Fatalf("the run's directory after the release: %v", err)
	}
	if err := a.Release(); err != nil {
		t.Fatalf("a second release: %v", err)
	}
}

// Residue — a run's directory left by a run that ended without
// removing it — is removed by the next run's first acquisition and
// by Sweep, a live run's directory kept, held by its lock; a
// directory with no lock file is residue too; a runs directory that
// does not exist holds none (REQ-plugin-local-pin, dep-verbs.md
// REQ-dep-clean).
func TestResidueSweptLiveRunKept(t *testing.T) {
	root, live := fixture(t)
	if _, err := live.Acquire(ctx, "tools/bin/gen", nil); err != nil {
		t.Fatal(err)
	}
	// Residue of three shapes: a run's directory with its lock free,
	// one with no lock file, and a remover's tombstone it never
	// finished removing.
	for _, stale := range []string{"run-stale", "run-unlocked", "dead-gone"} {
		dir := filepath.Join(live.Runs, stale, "1")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "gen"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(live.Runs, "run-stale", lockName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live.Runs, "stray"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stranger's directory under Runs is nobody's to remove.
	if err := os.MkdirAll(filepath.Join(live.Runs, "stranger", "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	next := &Acquirer{Root: root, Runs: live.Runs, Lock: &lockfile.File{}, Policy: &trust.Policy{}, platform: plugin.HostPlatform()}
	if _, err := next.Acquire(ctx, "tools/bin/gen", nil); err != nil {
		t.Fatal(err)
	}
	names := func() string {
		entries, err := os.ReadDir(live.Runs)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return strings.Join(names, ",")
	}
	if n := names(); strings.Contains(n, "stale") || strings.Contains(n, "unlocked") || strings.Contains(n, "dead-") || !strings.Contains(n, "stray") || strings.Count(n, "run-") != 2 {
		t.Fatalf("the runs directory after the next run's acquisition: %s; want the two live runs and the stray file, the residue gone", n)
	}
	if err := live.Release(); err != nil {
		t.Fatal(err)
	}
	if n := names(); strings.Count(n, "run-") != 1 {
		t.Fatalf("after the first run's release: %s", n)
	}
	// A sweep spares the live run.
	if err := Sweep(live.Runs); err != nil {
		t.Fatal(err)
	}
	if n := names(); strings.Count(n, "run-") != 1 {
		t.Fatalf("a sweep removed the live run: %s", n)
	}
	if err := next.Release(); err != nil {
		t.Fatal(err)
	}
	if n := names(); n != "stranger,stray" {
		t.Fatalf("after every release: %s", n)
	}
	if err := Sweep(filepath.Join(live.Runs, "never")); err != nil {
		t.Fatalf("a sweep of no directory: %v", err)
	}
	// An acquirer with no runs directory is refused before any copy.
	none := &Acquirer{Root: root, Lock: &lockfile.File{}, Policy: &trust.Policy{}}
	if _, err := none.Acquire(ctx, "tools/bin/gen", nil); err == nil || !strings.Contains(err.Error(), "runs directory") {
		t.Fatalf("no runs directory: %v", err)
	}
}

// A claim is decided by the lock alone: a fresh directory a sweeper
// condemned before the run took its lock — or removed outright — is
// left to the sweeper and another made; a directory swept from
// under the run three times is the run's failure, naming it; a
// failure past the directory's making leaves no directory behind
// (REQ-plugin-local-pin).
func TestClaimYieldsToASweeper(t *testing.T) {
	root, a := fixture(t)
	made := 0
	a.mkdirTemp = func(dir, pattern string) (string, error) {
		made++
		d, err := os.MkdirTemp(dir, pattern)
		if err != nil {
			return "", err
		}
		switch made {
		case 1:
			// Condemned: a sweeper's lock file, its byte written.
			if err := os.WriteFile(filepath.Join(d, lockName), []byte{'x'}, 0o644); err != nil {
				return "", err
			}
		case 2:
			// Gone: a sweeper that removed it already.
			if err := os.RemoveAll(d); err != nil {
				return "", err
			}
		}
		return d, nil
	}
	got, err := a.Acquire(ctx, "tools/bin/gen", nil)
	if err != nil || made != 3 {
		t.Fatalf("acquired after %d directories: %v", made, err)
	}
	if entries, err := os.ReadDir(a.Runs); err != nil || len(entries) != 1 || !strings.HasPrefix(got.Process.Argv[0], filepath.Join(a.Runs, entries[0].Name())) {
		t.Fatalf("the runs directory after the claim: %v, %v; want the claimed run alone, the condemned one swept", entries, err)
	}
	always := &Acquirer{Root: root, Runs: t.TempDir(), Lock: &lockfile.File{}, Policy: &trust.Policy{}, platform: plugin.HostPlatform()}
	always.mkdirTemp = func(dir, pattern string) (string, error) {
		d, err := os.MkdirTemp(dir, pattern)
		if err != nil {
			return "", err
		}
		return d, os.WriteFile(filepath.Join(d, lockName), []byte{'x'}, 0o644)
	}
	if _, err := always.Acquire(ctx, "tools/bin/gen", nil); err == nil || !strings.Contains(err.Error(), "three times") {
		t.Fatalf("a directory swept from under the run every time: %v", err)
	}
	// The condemned directories are a sweeper's to remove, not the
	// refused run's: a sweep removes them, their locks free.
	if entries, err := os.ReadDir(always.Runs); err != nil || len(entries) != 3 {
		t.Fatalf("the runs directory after the failed claims: %v, %v; want the three condemned directories left to a sweep", entries, err)
	}
	if err := Sweep(always.Runs); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(always.Runs); err != nil || len(entries) != 0 {
		t.Fatalf("the runs directory after the sweep: %v, %v", entries, err)
	}
	// A failure making the lock file leaves the fresh directory
	// removed: a regular file where the lock file goes.
	broken := &Acquirer{Root: root, Runs: t.TempDir(), Lock: &lockfile.File{}, Policy: &trust.Policy{}, platform: plugin.HostPlatform()}
	broken.mkdirTemp = func(dir, pattern string) (string, error) {
		d, err := os.MkdirTemp(dir, pattern)
		if err != nil {
			return "", err
		}
		return d, os.Mkdir(filepath.Join(d, lockName), 0o755)
	}
	if _, err := broken.Acquire(ctx, "tools/bin/gen", nil); err == nil {
		t.Fatal("a lock file that cannot be made: no error")
	}
	if entries, err := os.ReadDir(broken.Runs); err != nil || len(entries) != 0 {
		t.Fatalf("the runs directory after the failed making: %v, %v; want it empty", entries, err)
	}
}

// Residue a sweep cannot remove is left to a later one by a run's
// start, and fails Sweep, the emptying's (REQ-plugin-local-pin,
// dep-verbs.md REQ-dep-clean).
func TestResidueBeyondRemovalLeftByARunFailsASweep(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a directory unremovable by its permissions needs unix and a user they bind")
	}
	_, a := fixture(t)
	stuck := filepath.Join(a.Runs, "run-stuck")
	if err := os.MkdirAll(filepath.Join(stuck, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stuck, lockName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// The directory's entries cannot be removed: no write on it.
	if err := os.Chmod(stuck, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o755); _ = os.Chmod(tombstone(stuck), 0o755) })
	// The start's sweep tries the removal once: what it cannot remove
	// is a later sweep's, not worth the run's waiting.
	removals := 0
	a.removeAll = func(path string) error {
		if path == tombstone(stuck) {
			removals++
		}
		return os.RemoveAll(path)
	}
	if _, err := a.Acquire(ctx, "tools/bin/gen", nil); err != nil {
		t.Fatalf("a run's start failed on residue it cannot remove: %v", err)
	}
	if removals != 1 {
		t.Fatalf("the start's sweep tried the removal %d times, want once", removals)
	}
	// The run's start took the directory out of a claim's reach
	// before the removal it could not finish: renamed to its
	// tombstone, its copies left there.
	dead := tombstone(stuck)
	if _, err := os.Stat(filepath.Join(dead, "1")); err != nil {
		t.Fatalf("the stuck residue: %v", err)
	}
	if err := Sweep(a.Runs); err == nil {
		t.Fatal("a sweep that left residue reported no failure")
	}
	// The lock file condemned under the lock; a claim of the run's
	// name finds nothing.
	if fi, err := os.Stat(filepath.Join(dead, lockName)); err != nil || fi.Size() == 0 {
		t.Fatalf("the stuck residue's lock file after the sweep: %v, %v; want it at the tombstone, condemned", fi, err)
	}
	if f, claimed, err := claim(stuck); err != nil || claimed {
		f.Close()
		t.Fatalf("the swept directory claimed: %v, %v", claimed, err)
	}
	if err := os.Chmod(dead, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Sweep(a.Runs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("the residue after the sweep: %v", err)
	}
}

// Residue whose lock file cannot be opened — a directory where the
// file goes — is left by a run's start and fails Sweep, on every
// host and user alike (REQ-plugin-local-pin, dep-verbs.md
// REQ-dep-clean).
func TestResidueWithoutALockFileToOpenLeftByARunFailsASweep(t *testing.T) {
	_, a := fixture(t)
	stuck := filepath.Join(a.Runs, "run-stuck")
	if err := os.MkdirAll(filepath.Join(stuck, lockName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Acquire(ctx, "tools/bin/gen", nil); err != nil {
		t.Fatalf("a run's start failed on residue it cannot open: %v", err)
	}
	if _, err := os.Stat(stuck); err != nil {
		t.Fatalf("the stuck residue: %v", err)
	}
	if err := Sweep(a.Runs); err == nil {
		t.Fatal("a sweep that left residue reported no failure")
	}
}

// A remover takes the directory out of a claim's reach before
// removing anything in it: a claim of the run's name made in the
// midst of the removal — the lock file unlinked already, where a
// fresh lock file would otherwise be made and claimed in a directory
// being removed — finds nothing, and the removal removes no claim's
// copies (REQ-plugin-local-pin).
func TestClaimDuringARemovalFindsNothing(t *testing.T) {
	_, a := fixture(t)
	var dir string
	a.mkdirTemp = func(d, pattern string) (string, error) {
		var err error
		dir, err = os.MkdirTemp(d, pattern)
		return dir, err
	}
	removals, claimedDuring := 0, false
	a.removeAll = func(path string) error {
		removals++
		if filepath.Base(path) != filepath.Base(tombstone(dir)) {
			t.Errorf("the removal of %s, want the tombstone of %s", path, dir)
		}
		// The remover's unlinking has reached the lock file; a run
		// claims the directory by its name in that instant.
		if err := os.Remove(filepath.Join(path, lockName)); err != nil {
			return err
		}
		if f, claimed, err := claim(dir); err != nil {
			return err
		} else if claimed {
			f.Close()
			claimedDuring = true
		}
		return os.RemoveAll(path)
	}
	if _, err := a.Acquire(ctx, "tools/bin/gen", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if removals != 1 || claimedDuring {
		t.Fatalf("removals %d, a claim in the midst of one %v", removals, claimedDuring)
	}
	if entries, err := os.ReadDir(a.Runs); err != nil || len(entries) != 0 {
		t.Fatalf("the runs directory after the release: %v, %v", entries, err)
	}
}

// The lock held names a file and the rename a directory: a remover
// holding the lock of a directory removed whole and made again under
// the same name by a new run — the stdlib's fresh name reused in the
// instant — finds the tombstone's lock file a claim's, empty, renames
// the directory back and removes nothing of the new run; the file's
// identity would not tell, a freed inode being reused
// (REQ-plugin-local-pin).
func TestARemoverSparesADirectoryMadeAnewUnderItsName(t *testing.T) {
	_, a := fixture(t)
	dir := filepath.Join(a.Runs, "run-reused")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The remover's own opening and lock of the directory's first life.
	stale, claimed, err := claim(dir)
	if err != nil || !claimed {
		t.Fatalf("the first claim: %v %v", claimed, err)
	}
	// The directory's first life removed whole by another remover,
	// and a new run making it anew under the same name.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1", "gen"), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	fresh, claimed, err := claim(dir)
	if err != nil || !claimed {
		t.Fatalf("the new run's claim: %v %v", claimed, err)
	}
	defer fresh.Close()
	removals := 0
	if err := (&run{dir: dir, lock: stale, rm: func(string) error { removals++; return nil }}).remove(true); err != nil {
		t.Fatal(err)
	}
	if removals != 0 {
		t.Fatalf("the stale remover removed %d directories of the new run", removals)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "1", "gen")); err != nil || string(b) != "new" {
		t.Fatalf("the new run's copy after the stale removal: %q, %v", b, err)
	}
	if _, err := os.Stat(tombstone(dir)); !os.IsNotExist(err) {
		t.Fatalf("a tombstone left for the new run's directory: %v", err)
	}
	if !flocktest.Held(t, filepath.Join(dir, lockName)) {
		t.Fatal("the new run's lock is not held")
	}
}

// Two removers at one directory — a run's release and another run's
// sweep — leave it to the one that renamed it first: the other,
// finding nothing at the name, neither removes nor renames back what
// the first is at, so the first's removal completes and nothing
// stands under the run's name (REQ-plugin-local-pin).
func TestARemoverYieldsToTheOneThatRenamedFirst(t *testing.T) {
	_, a := fixture(t)
	dir := filepath.Join(a.Runs, "run-shared")
	if err := os.MkdirAll(filepath.Join(dir, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1", "gen"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	first, claimed, err := claim(dir)
	if err != nil || !claimed {
		t.Fatalf("the first remover's claim: %v %v", claimed, err)
	}
	// The second remover's descriptor on the same lock file, taking
	// the lock once the first releases it — inside the first's
	// removal here, the first's unlinking having reached the lock
	// file.
	second, err := os.OpenFile(filepath.Join(dir, lockName), os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	seconds := 0
	rm := func(path string) error {
		if err := os.Remove(filepath.Join(path, lockName)); err != nil {
			return err
		}
		if taken, err := flock.TryLock(second); err != nil || !taken {
			t.Fatalf("the second remover's lock: %v %v", taken, err)
		}
		if err := (&run{dir: dir, lock: second, rm: func(string) error { seconds++; return nil }}).remove(true); err != nil {
			t.Fatalf("the second remover: %v", err)
		}
		return os.RemoveAll(path)
	}
	if err := (&run{dir: dir, lock: first, rm: rm}).remove(true); err != nil {
		t.Fatal(err)
	}
	if seconds != 0 {
		t.Fatalf("the second remover removed %d directories the first was at", seconds)
	}
	if entries, err := os.ReadDir(a.Runs); err != nil || len(entries) != 0 {
		t.Fatalf("the runs directory after both removers: %v, %v; want nothing under the run's name", entries, err)
	}
}

// A tombstone's lock file gone is another remover's unlinking — a
// sweep that reached the tombstone after the rename — not a new
// life's empty one: the remover carries on, and nothing stands
// under the run's name after it (REQ-plugin-local-pin).
func TestARemoverCarriesOnWhenTheTombstonesLockFileIsGone(t *testing.T) {
	_, a := fixture(t)
	dir := filepath.Join(a.Runs, "run-unlinked")
	if err := os.MkdirAll(filepath.Join(dir, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, claimed, err := claim(dir)
	if err != nil || !claimed {
		t.Fatalf("the claim: %v %v", claimed, err)
	}
	// The other remover's unlinking of the lock file, the descriptor
	// held all the same.
	if err := os.Remove(filepath.Join(dir, lockName)); err != nil {
		t.Fatal(err)
	}
	if err := (&run{dir: dir, lock: f, rm: os.RemoveAll}).remove(true); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(a.Runs); err != nil || len(entries) != 0 {
		t.Fatalf("the runs directory after the removal: %v, %v; want nothing under the run's name", entries, err)
	}
}

// A tombstone whose lock file cannot be looked at — the directory
// unreadable — is neither taken for a new life nor removed: the
// failure is reported and the directory left as it is
// (REQ-plugin-local-pin).
func TestARemoverStopsWhereTheTombstoneCannotBeRead(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("an unreadable directory needs unix and a user its permissions bind")
	}
	_, a := fixture(t)
	dir := filepath.Join(a.Runs, "run-dark")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, claimed, err := claim(dir)
	if err != nil || !claimed {
		t.Fatalf("the claim: %v %v", claimed, err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755); _ = os.Chmod(tombstone(dir), 0o755) })
	removals := 0
	err = (&run{dir: dir, lock: f, rm: func(string) error { removals++; return nil }}).remove(true)
	if err == nil || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("the removal past an unreadable tombstone: %v", err)
	}
	if removals != 0 {
		t.Fatalf("the remover removed %d directories it could not look at", removals)
	}
	if _, err := os.Stat(tombstone(dir)); err != nil {
		t.Fatalf("the tombstone after the failure: %v", err)
	}
}
