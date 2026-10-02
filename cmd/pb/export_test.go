package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An output path is named as the working tree names it: absolute
// against the working directory, without the root's slash.
func TestTreePath(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	got, err := treePath("out/x")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimPrefix(filepath.ToSlash(filepath.Join(root, "out", "x")), "/"); got != want {
		t.Fatalf("treePath = %q, want %q", got, want)
	}
}

// A working directory reached through a symbolic link is named as the
// filesystem names it, so the tree bound at the root serves it.
func TestWorkingTreeResolvesSymlinkedCwd(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(real, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, "proj"), filepath.Join(real, "link")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(real, "link"))
	_, dir, err := workingTree()
	if err != nil {
		t.Fatal(err)
	}
	// The directory is spelled relative to the tree's root: the volume
	// on windows, `/` elsewhere.
	volume := filepath.ToSlash(filepath.VolumeName(real) + string(filepath.Separator))
	if want := strings.TrimPrefix(filepath.ToSlash(filepath.Join(real, "proj")), volume); dir != want {
		t.Fatalf("working directory %q, want %q", dir, want)
	}
}
