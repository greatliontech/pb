package direct

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/testing/scratchtest"
)

// objectCounts counts a repository's objects by type.
func objectCounts(t *testing.T, r *git.Repository) map[plumbing.ObjectType]int {
	t.Helper()
	iter, err := r.Storer.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[plumbing.ObjectType]int{}
	err = iter.ForEach(func(o plumbing.EncodedObject) error {
		counts[o.Type()]++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

// Against an origin, the needs the fixture's in-process server cannot
// witness (REQ-proxy-direct-fetch): a release version's tag fetched
// at depth one leaves the snapshots holding the tag's commit alone,
// shallow at it, the history behind it left at the origin; and the
// history, fetched for a pseudo-version, holds the commit graph
// through the origin's object filter — commits and tags, no tree or
// blob — the pseudo-version deciding over it. Runs only where
// PB_LIVE_ORIGINS is set — an absolute path keeps the store there.
func TestLiveFetchByNeed(t *testing.T) {
	live := os.Getenv("PB_LIVE_ORIGINS")
	if live == "" {
		t.Skip("PB_LIVE_ORIGINS unset: the origin is not reached")
	}
	dir := live
	if !filepath.IsAbs(dir) {
		dir = scratchtest.Dir(t)
	}
	ctx := context.Background()
	repo, err := Fetcher{Store: osfs.New(filepath.Join(dir, "vcs"))}.Fetch(ctx, "https://github.com/prometheus/client_model")
	if err != nil {
		t.Fatal(err)
	}
	v, err := version.Parse("v0.6.1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.ResolveVersion(ctx, v, "")
	if err != nil {
		t.Fatal(err)
	}
	shallows, err := repo.st.Shallow()
	if err != nil {
		t.Fatal(err)
	}
	if len(shallows) != 1 || shallows[0].String() != got.Hash {
		t.Fatalf("the snapshots after a tag: shallow at %v, the tag's commit %s", shallows, got.Hash)
	}
	if n := objectCounts(t, repo.r)[plumbing.CommitObject]; n != 1 {
		t.Fatalf("%d commits in the snapshots after one tag at depth one", n)
	}
	// The head's pseudo-version: the history fetched, filtered.
	head, err := repo.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	commits, _, err := repo.commitIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := objectCounts(t, repo.hist)
	if counts[plumbing.TreeObject] != 0 || counts[plumbing.BlobObject] != 0 || counts[plumbing.CommitObject] < 100 {
		t.Fatalf("the history holds %v", counts)
	}
	c, ok := commits[head.Hash]
	if !ok {
		t.Fatalf("the head %s is not in the graph", head.Hash)
	}
	pseudo, err := repo.expectedPseudo(ctx, c, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(pseudo.String(), "v0.0.0-") {
		t.Fatalf("the head's pseudo-version %s derives no base from the release tags", pseudo)
	}
	if got, err := repo.ResolveVersion(ctx, pseudo, ""); err != nil || got.Hash != head.Hash {
		t.Fatalf("the head's pseudo-version: %+v %v", got, err)
	}
	if n := objectCounts(t, repo.r)[plumbing.CommitObject]; n != 2 {
		t.Fatalf("%d commits in the snapshots after the tag and the head", n)
	}
}
