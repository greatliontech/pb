//go:build unix

package direct

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-billy/v6/util"

	"github.com/greatliontech/pb/internal/testing/scratchtest"
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

// Empty takes each origin's lock before emptying it: a lock another
// open file description holds — another process's, in effect —
// blocks the emptying until it is released (REQ-dep-clean).
func TestEmptyWaitsForTheOriginsLock(t *testing.T) {
	store := osfs.New(scratchtest.Dir(t))
	origin := strings.Repeat("ab", 32) // an origin's directory, named by its URL's hash
	if err := util.WriteFile(store, store.Join(origin, "snapshots", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(store.Root(), origin, lockName), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := Empty(ctx, store); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Empty under a held lock: %v, want the wait ended by the deadline", err)
	}
	if _, err := store.Stat(store.Join(origin, "snapshots", "HEAD")); err != nil {
		t.Fatalf("the origin was emptied under another holder's lock: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := Empty(context.Background(), store); err != nil {
		t.Fatalf("Empty after the release: %v", err)
	}
	if _, err := store.Stat(store.Join(origin, "snapshots")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the origin's repositories after the emptying: %v", err)
	}
}
