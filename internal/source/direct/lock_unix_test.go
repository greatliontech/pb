//go:build unix

package direct

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// assertLocked asserts that another open file description holds the
// file's lock: a non-blocking flock on a fresh descriptor is refused.
func assertLocked(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		t.Fatal("the process's lock was released behind it")
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatal(err)
	}
}
