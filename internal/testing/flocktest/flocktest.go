// Package flocktest asks whether a file's advisory lock is held, for
// a test that watches a lock taken and released by the code under
// test: the probe is a non-blocking exclusive attempt on a descriptor
// of the test's own, released at once where it succeeds, so the
// question changes nothing of what it observes.
package flocktest

import (
	"os"
	"testing"

	"github.com/greatliontech/pb/internal/flock"
)

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
	taken, err := flock.TryLock(f)
	if err != nil {
		tb.Fatal(err)
	}
	if taken {
		_ = flock.Unlock(f)
	}
	return !taken
}
