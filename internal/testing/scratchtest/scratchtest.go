// Package scratchtest hands a test a fresh directory inside the module
// under test, under its testdata/scratch, rather than the host's
// temporary directory: a mutation-test oracle then observes no
// directory outside the tree, and a search up the directory's
// ancestors has a root of the test's own to stop at. The directory
// is removed with the test, and the scratch root with it once empty.
package scratchtest

import (
	"os"
	"path/filepath"
	"testing"
)

// Dir is a fresh directory under the package's testdata/scratch,
// absolute, removed when the test ends.
func Dir(tb testing.TB) string {
	tb.Helper()
	root, err := filepath.Abs(filepath.Join("testdata", "scratch"))
	if err != nil {
		tb.Fatal(err)
	}
	// Another test's cleanup may remove the root between the two
	// steps; the second attempt sees it made again.
	var dir string
	for attempt := 0; ; attempt++ {
		if err := os.MkdirAll(root, 0o755); err != nil {
			tb.Fatal(err)
		}
		dir, err = os.MkdirTemp(root, "x")
		if err == nil || attempt == 1 {
			break
		}
	}
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		os.RemoveAll(dir)
		os.Remove(root)
	})
	return dir
}
