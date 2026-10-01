package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/provenance/trust"
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
	return root, &Acquirer{Root: root, Lock: &lockfile.File{}, Policy: &trust.Policy{}, platform: plugin.HostPlatform()}
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
	abs := filepath.Join(root, "tools", "bin", "gen")
	if p, err := a.Resolve(abs); err != nil || p != abs {
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
	if !ok || len(pin.Binary) != 1 || !strings.HasPrefix(pin.Binary[host], "sha256:") || len(got.Process.Argv) != 1 || got.Process.Argv[0] != filepath.Join(root, "tools", "bin", "gen") || got.Process.WorkDir != root {
		t.Fatalf("first use: %+v %+v", pin, got)
	}
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
	other := &Acquirer{Root: root, Lock: a.Lock, Policy: a.Policy, platform: otherPlatform}
	if _, err := other.Acquire(ctx, "tools/bin/gen", nil); err != nil {
		t.Fatal(err)
	}
	pin, _ = a.Lock.Plugin("tools/bin/gen", lockfile.SchemeLocal)
	if len(pin.Binary) != 2 || pin.Binary[host] != hash {
		t.Fatalf("second platform: %+v", pin)
	}
	// Pinning disabled: nothing recorded, nothing checked.
	off := false
	unpinned := &Acquirer{Root: root, Lock: &lockfile.File{}, Policy: &trust.Policy{Execution: trust.Execution{LocalPin: &off}}, platform: plugin.HostPlatform()}
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
	want := append([]string{filepath.Join(os.Getenv("PATH"), exe)}, args...)
	if strings.Join(got.Process.Argv, "\x00") != strings.Join(want, "\x00") || got.Process.WorkDir != root {
		t.Fatalf("process = %+v, want argv %q from %s", got.Process, want, root)
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
	a := &Acquirer{Root: root, Lock: &lockfile.File{}, Policy: &trust.Policy{}}
	for _, value := range []string{"./tools/bin/gen", filepath.ToSlash(filepath.Join(root, "tools", "bin", "gen"))} {
		if _, err := a.Acquire(ctx, value, nil); err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		pin, ok := a.Lock.Plugin(value, lockfile.SchemeLocal)
		if !ok || pin.Binary[plugin.HostPlatform().String()] == "" {
			t.Fatalf("%q pinned as %+v under %q", value, pin, plugin.HostPlatform().String())
		}
	}
	relative := &Acquirer{Root: "tmp/ws", Lock: &lockfile.File{}, Policy: &trust.Policy{}}
	if _, err := relative.Resolve("tools/bin/gen"); err == nil || !strings.Contains(err.Error(), "not an absolute host path") {
		t.Fatalf("relative root: %v", err)
	}
	if _, err := relative.Resolve("protoc-gen-x"); err == nil || !strings.Contains(err.Error(), "not an absolute host path") {
		t.Fatalf("relative root, bare name: %v", err)
	}
}

// Hashing runs under the caller's context: a cancelled acquisition
// ends without a pin.
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
}
