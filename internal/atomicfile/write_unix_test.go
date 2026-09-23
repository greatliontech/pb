//go:build unix

package atomicfile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
)

// On a real filesystem a file lands at the mode asked for less the
// process's umask — 0644 the creation default, never the temporary's
// owner-only mode — and a replaced file takes that mode too.
func TestWriteModeOnDisk(t *testing.T) {
	// A known umask, so the test tells 0644 from the temporary's 0600
	// whatever the environment's umask; the package runs no test in
	// parallel.
	const umask = 0o022
	prev := syscall.Umask(umask)
	defer syscall.Umask(prev)
	root := t.TempDir()
	fsys := osfs.New(root)
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(fsys, "f", ".tmp-", 0o644, []byte("new")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, "f"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v, want 0644 under umask %04o", fi.Mode().Perm(), umask)
	}
}
