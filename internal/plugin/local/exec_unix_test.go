//go:build unix

package local

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The process the run executes is the copy: a binary rebuilt at its
// place between the acquisition and the run leaves the run executing
// the bytes the pin names (REQ-plugin-local-pin).
func TestRunExecutesTheAcquiredBytes(t *testing.T) {
	root, a := fixture(t)
	got, err := a.Acquire(ctx, "tools/bin/gen", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tools", "bin", "gen"), []byte("#!/bin/sh\necho rebuilt\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(got.Process.Argv[0]); err != nil || fi.Mode().Perm()&0o222 != 0 || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the copy's mode: %v, %v; want executable and not writable", fi.Mode(), err)
	}
	cmd := exec.Command(got.Process.Argv[0], got.Process.Argv[1:]...)
	cmd.Dir = got.Process.WorkDir
	out, err := cmd.Output()
	if err != nil || string(out) != "gen\n" {
		t.Fatalf("the run's output: %q, %v; want the acquired binary's", out, err)
	}
}
