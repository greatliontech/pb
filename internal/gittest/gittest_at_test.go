package gittest

import (
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

// Two repositories rooted at sibling directories of one filesystem are
// distinct: each holds its own objects and refs, and the shared base
// serves both to the file transport.
func TestNewAtSiblingsAreDistinct(t *testing.T) {
	fs := memfs.New()
	a := NewAt(t, fs, "a")
	b := NewAt(t, fs, "b")
	when := time.Unix(1700000000, 0)
	ca := a.Commit("in-a", when)
	cb := b.Commit("in-b", when)
	if ca == cb {
		t.Fatal("sibling repositories produced the same commit hash for different content")
	}
	if _, err := a.St.EncodedObject(plumbing.CommitObject, cb); err == nil {
		t.Fatal("repository a can read repository b's commit: storages alias")
	}
	ra, err := git.Open(a.St, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ra.CommitObject(ca); err != nil {
		t.Fatalf("repository a cannot read its own commit: %v", err)
	}
}

// New is exactly NewAt at the filesystem root.
func TestNewDelegatesToRoot(t *testing.T) {
	r := New(t)
	when := time.Unix(1700000000, 0)
	c := r.Commit("x", when)
	if _, err := r.St.EncodedObject(plumbing.CommitObject, c); err != nil {
		t.Fatalf("root repository cannot read its own commit: %v", err)
	}
	// The repository lives at the root: its storage layout is directly
	// under FS, not a subdirectory.
	if _, err := r.FS.Stat("objects"); err != nil {
		t.Fatalf("root repository has no objects directory at the fs root: %v", err)
	}
}

// Initializing twice at one directory fails loudly through the Failer —
// aliasing two fixtures onto one repository is never silent.
func TestNewAtDoubleInitFailsLoudly(t *testing.T) {
	fs := memfs.New()
	rf := &recordingFailer{}
	func() {
		defer func() { recover() }()
		NewAt(rf, fs, "a")
		NewAt(rf, fs, "a")
	}()
	if !rf.hit {
		t.Fatal("double init at one directory did not fail")
	}
}
