//go:build unix

package flocktest

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// Held reports whether another descriptor holds the file's lock: a
// non-blocking exclusive attempt refused means held; one granted is
// released again and means free. A file that cannot be opened fails
// the test.
func Held(tb testing.TB, path string) bool {
	tb.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		tb.Fatal(err)
	}
	return true
}
