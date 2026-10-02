//go:build windows

package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
)

// On windows a target another handle holds open cannot be replaced:
// the write fails naming the file and the target keeps its bytes
// whole, the rename being the only step that touches it; released,
// the same write succeeds (platforms.md REQ-plat-files).
func TestWriteRefusesOpenTargetOnWindows(t *testing.T) {
	root := t.TempDir()
	fsys := osfs.New(root)
	target := filepath.Join(root, "f")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	err = Write(fsys, "f", ".tmp-", 0o644, []byte("new"))
	var link *os.LinkError
	if err == nil || !errors.As(err, &link) || link.New != "f" || !errors.Is(err, fs.ErrPermission) {
		held.Close()
		t.Fatalf("a write over an open file: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "old" {
		held.Close()
		t.Fatalf("the target after the refusal: %q", b)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		held.Close()
		t.Fatalf("the temporary left behind: %v", entries)
	}
	held.Close()
	if err := Write(fsys, "f", ".tmp-", 0o644, []byte("new")); err != nil {
		t.Fatalf("released: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "new" {
		t.Fatalf("after the release: %q", b)
	}
}
