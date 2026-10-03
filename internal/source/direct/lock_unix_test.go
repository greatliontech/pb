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

	"github.com/greatliontech/pb/internal/testing/flocktest"
	"github.com/greatliontech/pb/internal/testing/scratchtest"
)

// assertLocked asserts that another open file description holds the
// file's lock: a non-blocking flock on a fresh descriptor is refused.
func assertLocked(t *testing.T, path string) {
	t.Helper()
	if !flocktest.Held(t, path) {
		t.Fatal("the process's lock was released behind it")
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

// assertUnlocked asserts that no open file description holds the
// file's lock: a non-blocking flock on a fresh descriptor succeeds.
func assertUnlocked(t *testing.T, path string) {
	t.Helper()
	if flocktest.Held(t, path) {
		t.Fatal("the lock is still held")
	}
}

// An origin's lock is held while any opening of it is unreleased and
// goes with the last: two openings in one process hold it once,
// closing one keeps it, closing the other frees it for another
// process, closing twice is nothing, and the next opening takes it
// again (REQ-proxy-direct-fetch).
func TestCloseReleasesTheLockWithTheLastOpening(t *testing.T) {
	ctx := context.Background()
	c := newChain(t)
	store := osfs.New(scratchtest.Dir(t))
	fetcher := Fetcher{ClientOptions: c.ClientOptions(), Store: store}
	first, err := fetcher.Fetch(ctx, "file:///")
	if err != nil {
		t.Fatal(err)
	}
	second, err := fetcher.Fetch(ctx, "file:///")
	if err != nil {
		t.Fatal(err)
	}
	dirs, _ := store.ReadDir(".")
	lock := filepath.Join(store.Root(), dirs[0].Name(), lockName)
	assertLocked(t, lock)
	first.Close()
	assertLocked(t, lock)
	second.Close()
	assertUnlocked(t, lock)
	second.Close()
	assertUnlocked(t, lock)
	third, err := fetcher.Fetch(ctx, "file:///")
	if err != nil {
		t.Fatal(err)
	}
	assertLocked(t, lock)
	third.Close()
	assertUnlocked(t, lock)
	// An opening the store carries no lock for releases nothing.
	inMemory, err := (Fetcher{ClientOptions: c.ClientOptions()}).Fetch(ctx, "file:///")
	if err != nil {
		t.Fatal(err)
	}
	inMemory.Close()
}

// An opening that fails past the hold — an origin with no ref to list
// — holds nothing: the lock is released with the failure, no caller
// having a repository to close (REQ-proxy-direct-fetch).
func TestFailedOpeningHoldsNothing(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	store := osfs.New(scratchtest.Dir(t))
	if _, err := (Fetcher{ClientOptions: f.ClientOptions(), Store: store}).Fetch(ctx, "file:///"); err == nil || !strings.Contains(err.Error(), "listing refs") {
		t.Fatalf("an origin with no ref: %v", err)
	}
	dirs, err := store.ReadDir(".")
	if err != nil || len(dirs) != 1 {
		t.Fatalf("the store after the failed opening: %v %v", dirs, err)
	}
	assertUnlocked(t, filepath.Join(store.Root(), dirs[0].Name(), lockName))
}
