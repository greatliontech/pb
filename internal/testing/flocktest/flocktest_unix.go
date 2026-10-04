//go:build unix

package flocktest

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// tryLock takes the file's lock, exclusive, without waiting: false
// where another descriptor holds it.
func tryLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

// unlock releases the file's lock.
func unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// Held reports whether another descriptor holds the file's lock: a
// non-blocking exclusive attempt refused means held; one granted is
// released again and means free. The descriptor is read-only, which
// the lock does not mind, so a lock file the test cannot write is
// probed all the same; one it cannot open fails the test.
func Held(tb testing.TB, path string) bool {
	tb.Helper()
	f, err := os.Open(path)
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	taken, err := tryLock(f)
	if err != nil {
		tb.Fatal(err)
	}
	if taken {
		_ = unlock(f)
	}
	return !taken
}
