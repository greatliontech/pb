package local

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/plugin"
)

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
	return root, &Acquirer{Root: root, platform: plugin.HostPlatform()}
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
	// working directory, pb's wherever it was invoked: passed over as
	// the empty one is.
	pathList := strings.Join([]string{first, "", ".", second}, string(os.PathListSeparator))
	w := &Acquirer{Root: root, platform: plugin.Platform{OS: "windows", Arch: "amd64"}, env: func(k string) string {
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

// The process is the resolved command with the arguments after it,
// verbatim, from the resolution root, and nothing is recorded of it;
// an argument naming a path is neither resolved nor checked; the
// arguments are the caller's, never aliased
// (REQ-plugin-local-resolution, plugin-execution.md "Local binaries").
func TestAcquireIsTheResolvedCommand(t *testing.T) {
	root, a := fixture(t)
	args := []string{"tool", "web/no/such/script.js", "--flag with space", ""}
	got, err := a.Acquire("protoc-gen-x", args)
	if err != nil {
		t.Fatal(err)
	}
	exe := "protoc-gen-x"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	want := append([]string{filepath.Join(os.Getenv("PATH"), exe)}, args...)
	if strings.Join(got.Process.Argv, "\x00") != strings.Join(want, "\x00") || got.Process.WorkDir != root || got.Image != nil {
		t.Fatalf("process = %+v, want argv %q from %s and no image", got.Process, want, root)
	}
	args[0] = "changed"
	if got.Process.Argv[1] != "tool" {
		t.Fatal("the process shares the caller's argument slice")
	}
	if got, err := a.Acquire("tools/bin/gen", nil); err != nil || len(got.Process.Argv) != 1 || got.Process.Argv[0] != filepath.Join(root, "tools", "bin", "gen") {
		t.Fatalf("a root-relative command: %+v, %v", got, err)
	}
}

// The host platform is spelled <os>/<arch>, and the production
// acquirer resolves for it; a root that is not an absolute host path
// is refused before any lookup; the legal path spellings resolve.
func TestPlatformAndRoot(t *testing.T) {
	if plugin.HostPlatform().String() != runtime.GOOS+"/"+runtime.GOARCH {
		t.Fatalf("host platform = %q", plugin.HostPlatform())
	}
	root, _ := fixture(t)
	a := &Acquirer{Root: root}
	abs := filepath.Join(root, "tools", "bin", "gen")
	for _, value := range []string{"./tools/bin/gen", filepath.ToSlash(abs)} {
		got, err := a.Acquire(value, nil)
		if err != nil || got.Process.Argv[0] != abs {
			t.Fatalf("%q: %+v, %v", value, got, err)
		}
	}
	relative := &Acquirer{Root: "tmp/ws"}
	if _, err := relative.Resolve("tools/bin/gen"); err == nil || !strings.Contains(err.Error(), "not an absolute host path") {
		t.Fatalf("relative root: %v", err)
	}
	if _, err := relative.Resolve("protoc-gen-x"); err == nil || !strings.Contains(err.Error(), "not an absolute host path") {
		t.Fatalf("relative root, bare name: %v", err)
	}
}
