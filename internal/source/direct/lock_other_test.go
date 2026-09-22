//go:build !unix

package direct

import "testing"

// assertLocked has no non-blocking probe off Unix: the lock's survival
// past the collector is witnessed on Unix alone.
func assertLocked(t *testing.T, path string) {
	t.Helper()
	t.Logf("no non-blocking lock probe on this platform: %s not probed", path)
}
