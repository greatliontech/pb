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
	if err := os.WriteFile(filepath.Join(bin, "protoc-gen-x"), []byte("#!/bin/sh\necho x\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return root, &Acquirer{Root: root, Lock: &lockfile.File{}, Policy: &trust.Policy{}, platform: plugin.Platform{OS: "linux", Arch: "amd64"}}
}

// A bare name resolves on PATH exactly as written; a path resolves
// relative to the root, absolute allowed; a failure names the
// search and nothing else is tried (REQ-plugin-local-resolution).
func TestResolve(t *testing.T) {
	root, a := fixture(t)
	if p, err := a.Resolve("protoc-gen-x"); err != nil || filepath.Base(p) != "protoc-gen-x" {
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
		{"gen", `"gen" not found on PATH (looked up exactly as written)`},
		{"x", `"x" not found on PATH`},
		{"tools/bin/missing", "resolved relative to " + root},
		{"tools/notes.txt", "not executable"},
		{"tools/bin", "not a regular file"},
	}
	for _, c := range cases {
		if _, err := a.Resolve(c.value); err == nil || !strings.Contains(err.Error(), c.text) {
			t.Errorf("Resolve(%q) = %v, want %q", c.value, err, c.text)
		}
	}
}

// First use pins the content hash under the host platform; a later
// use with the same bytes at another location is a non-event; a
// differing hash fails naming both; a second platform adds its key;
// a policy disabling local pinning records nothing
// (REQ-plugin-local-pin).
func TestAcquirePins(t *testing.T) {
	root, a := fixture(t)
	got, err := a.Acquire(ctx, "tools/bin/gen")
	if err != nil {
		t.Fatal(err)
	}
	pin, ok := a.Lock.Plugin("tools/bin/gen", lockfile.SchemeLocal)
	if !ok || len(pin.Binary) != 1 || !strings.HasPrefix(pin.Binary["linux/amd64"], "sha256:") || got.Process.Argv[0] != filepath.Join(root, "tools", "bin", "gen") {
		t.Fatalf("first use: %+v %+v", pin, got)
	}
	hash := pin.Binary["linux/amd64"]
	// The same bytes elsewhere: a non-event.
	if err := os.Rename(filepath.Join(root, "tools", "bin", "gen"), filepath.Join(root, "gen")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "gen"), filepath.Join(root, "tools", "bin", "gen")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Acquire(ctx, "tools/bin/gen"); err != nil {
		t.Fatalf("moved binary, same bytes: %v", err)
	}
	// Other bytes: a mismatch naming both hashes.
	if err := os.WriteFile(filepath.Join(root, "gen"), []byte("#!/bin/sh\necho other\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = a.Acquire(ctx, "tools/bin/gen")
	if !errors.Is(err, lockfile.ErrPinMismatch) || !strings.Contains(err.Error(), hash) || !strings.Contains(err.Error(), "resolved sha256:") {
		t.Fatalf("changed binary: %v", err)
	}
	// Another platform's first use adds its key.
	other := &Acquirer{Root: root, Lock: a.Lock, Policy: a.Policy, platform: plugin.Platform{OS: "darwin", Arch: "arm64"}}
	if _, err := other.Acquire(ctx, "tools/bin/gen"); err != nil {
		t.Fatal(err)
	}
	pin, _ = a.Lock.Plugin("tools/bin/gen", lockfile.SchemeLocal)
	if len(pin.Binary) != 2 || pin.Binary["linux/amd64"] != hash {
		t.Fatalf("second platform: %+v", pin)
	}
	// Pinning disabled: nothing recorded, nothing checked.
	off := false
	unpinned := &Acquirer{Root: root, Lock: &lockfile.File{}, Policy: &trust.Policy{Execution: trust.Execution{LocalPin: &off}}, platform: plugin.Platform{OS: "linux", Arch: "amd64"}}
	if _, err := unpinned.Acquire(ctx, "tools/bin/gen"); err != nil {
		t.Fatal(err)
	}
	if len(unpinned.Lock.Plugins) != 0 {
		t.Fatalf("a pin was recorded with pinning disabled: %+v", unpinned.Lock.Plugins)
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
	for _, value := range []string{"./tools/bin/gen", filepath.Join(root, "tools", "bin", "gen")} {
		if _, err := a.Acquire(ctx, value); err != nil {
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
	if _, err := a.Acquire(cancelled, "tools/bin/gen"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquisition: %v", err)
	}
	if len(a.Lock.Plugins) != 0 {
		t.Fatal("a cancelled acquisition recorded a pin")
	}
}
