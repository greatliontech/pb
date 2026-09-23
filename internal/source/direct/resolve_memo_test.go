package direct

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/greatliontech/pb/internal/module/version"
)

// A Repo is one snapshot: views derived before storage grows do not
// change when it does. The open()-based fixture shares storage with
// the builder, so growing it after a first read is exactly the state a
// fetch snapshot can never observe — the memoized views pin that.
func TestRepoViewsAreSnapshots(t *testing.T) {
	f := newFixture(t)
	c1 := f.Commit("one", time.Unix(1700000000, 0))
	f.Ref("refs/tags/v1.0.0", c1)
	f.Ref("refs/heads/main", c1)
	f.Symref("HEAD", "refs/heads/main")
	repo := f.open()

	refs, err := repo.Refs()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ResolveVersion(context.Background(), mustParse(t, "v1.0.0"), ""); err != nil {
		t.Fatal(err)
	}
	// Resolving a pseudo-version builds the commit index and ancestry
	// now — the snapshot the growth below must not reach.
	pseudo1 := pseudoFor(t, repo, c1, "")
	if _, err := repo.ResolveVersion(context.Background(), pseudo1, ""); err != nil {
		t.Fatal(err)
	}

	// Storage grows after the first reads.
	c2 := f.Commit("two", time.Unix(1700003600, 0), c1)
	f.Ref("refs/tags/v2.0.0", c2)

	refs2, err := repo.Refs()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs2) != len(refs) {
		t.Fatalf("ref listing grew across the snapshot: %d -> %d", len(refs), len(refs2))
	}
	// The new commit is invisible to prefix binding through this Repo:
	// absent, specifically — a rebuilt index would instead find it and
	// fail base derivation (or resolve it), never report absence.
	if _, err := repo.ResolveVersion(context.Background(), pseudoOf(t, c2, time.Unix(1700003600, 0)), ""); !errors.Is(err, ErrCommitAbsent) {
		t.Fatalf("post-snapshot commit through the snapshot: err = %v, want ErrCommitAbsent", err)
	}
	// The pre-growth pseudo-version still resolves, its base unchanged
	// by the post-snapshot tag.
	if _, err := repo.ResolveVersion(context.Background(), pseudo1, ""); err != nil {
		t.Fatalf("pre-snapshot pseudo-version: %v", err)
	}
}

// pseudoFor derives the origin-expected pseudo-version of a commit
// through a fresh view, for use as a resolvable input.
func pseudoFor(t *testing.T, repo *Repo, h plumbing.Hash, subtree string) version.Version {
	t.Helper()
	c, err := repo.r.CommitObject(h)
	if err != nil {
		t.Fatal(err)
	}
	v, err := repo.expectedPseudo(context.Background(), c, subtree)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// pseudoOf builds the zero-base pseudo-version naming a commit.
func pseudoOf(t *testing.T, h plumbing.Hash, when time.Time) version.Version {
	t.Helper()
	v, err := version.PseudoVersion(nil, when, h.String())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Memoized views equal fresh recomputation: resolution through one
// long-lived Repo and through per-call fresh Repos agree on every
// outcome — caching a pure function changes cost, never results.
func TestMemoizedResolutionEqualsFresh(t *testing.T) {
	f := newFixture(t)
	c1 := f.Commit("one", time.Unix(1700000000, 0))
	c2 := f.Commit("two", time.Unix(1700003600, 0), c1)
	c3 := f.Commit("three", time.Unix(1700007200, 0), c2)
	f.Ref("refs/tags/v1.0.0", c1)
	f.Ref("refs/tags/sub/v2.0.0", c2)
	f.Ref("refs/heads/main", c3)
	f.Symref("HEAD", "refs/heads/main")

	memo := f.open()
	inputs := []struct {
		v       version.Version
		subtree string
	}{
		{mustParse(t, "v1.0.0"), ""},
		{mustParse(t, "v2.0.0"), "sub"},
		{pseudoFor(t, f.open(), c2, ""), ""},
		{pseudoFor(t, f.open(), c3, ""), ""},
		{pseudoFor(t, f.open(), c3, "sub"), "sub"},
		{mustParse(t, "v9.9.9"), ""},
	}
	for _, in := range inputs {
		got, gotErr := memo.ResolveVersion(context.Background(), in.v, in.subtree)
		want, wantErr := f.open().ResolveVersion(context.Background(), in.v, in.subtree)
		if (gotErr == nil) != (wantErr == nil) || got != want {
			t.Fatalf("%s (subtree %q): memoized (%v, %v) != fresh (%v, %v)",
				in.v, in.subtree, got, gotErr, want, wantErr)
		}
	}
	refsMemo, err1 := memo.Refs()
	refsFresh, err2 := f.open().Refs()
	if err1 != nil || err2 != nil || len(refsMemo) != len(refsFresh) {
		t.Fatalf("Refs: memoized (%d, %v) != fresh (%d, %v)", len(refsMemo), err1, len(refsFresh), err2)
	}
}

// PseudoCommit binds a pseudo-version to its commit with no judgment
// of the base (REQ-resolve-pseudo-commit): a consumer judges the
// namespace at that commit before ResolveVersion checks the base in it.
func TestPseudoCommitBindsWithoutBase(t *testing.T) {
	ctx := context.Background()
	c := newChain(t)
	opts, _ := c.recording()
	repo, err := Fetcher{ClientOptions: opts}.Fetch(ctx, "file:///")
	if err != nil {
		t.Fatal(err)
	}
	// c3 carries a tag in the root namespace, so a zero-base
	// pseudo-version over it binds and yet fails the base.
	v := mustV(t, "v0.0.0-"+c.t0.Add(2*time.Hour).UTC().Format(version.PseudoTimeLayout)+"-"+c.c3.String()[:12])
	bound, err := repo.PseudoCommit(ctx, v)
	if err != nil || bound.Hash != c.c3 {
		t.Fatalf("PseudoCommit = %v, %v", bound, err)
	}
	if _, err := repo.ResolveVersion(ctx, v, ""); !errors.Is(err, ErrBaseInconsistent) {
		t.Fatalf("ResolveVersion = %v", err)
	}
}
