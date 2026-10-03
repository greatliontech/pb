//go:build unix

package lsp

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/greatliontech/lsp/protocol"
	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/source/fetch"
	srcorigin "github.com/greatliontech/pb/internal/source/origin"
	"github.com/greatliontech/pb/internal/testing/fetchtest"
	"github.com/greatliontech/pb/internal/testing/flocktest"
	"github.com/greatliontech/pb/internal/testing/gittest"
	"github.com/greatliontech/pb/internal/testing/scratchtest"
)

// A judgement that fetched through the direct source releases what
// it opened of the origin at its end: the origin's lock in the
// store is free once the judgement has published, so a verb opening
// the origin after it proceeds without waiting on the server
// (REQ-lsp-session, module-proxy.md REQ-proxy-direct-fetch).
func TestJudgementReleasesTheOrigin(t *testing.T) {
	fx := newFixture(t, checkTree())
	// The std module served from the fixture's repository, at the tag
	// the tree requires, the store's lock on a real filesystem.
	commit := fx.CommitFor(stdModule(), fetchtest.GitWhen)
	fx.Repo.Ref("refs/tags/v1.0.0", commit)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")
	store := scratchtest.Dir(t)
	fx.source = "direct"
	// The pinning verb's client, a holder of the origin in this very
	// process, releases as its run's end would, so the server's hold
	// is the one the lock reports.
	fx.wireClient = func(c *fetch.Client) { c.Fetcher.Store = osfs.New(store) }
	pinner := fx.newClient()
	s, err := dep.Load(dep.Config{WS: fx.ws, Dir: "ws", Client: pinner})
	if err != nil {
		t.Fatal(err)
	}
	var verb strings.Builder
	if err := dep.Lint(context.Background(), s, &verb, io.Discard); err != nil && err != dep.ErrFindings {
		t.Fatalf("pinning: %v", err)
	}
	pinner.Release()
	origins, err := os.ReadDir(store)
	if err != nil || len(origins) != 1 {
		t.Fatalf("the store after the pinning: %v, %v", origins, err)
	}
	lock := filepath.Join(store, origins[0].Name(), "lock")
	locked := func() bool { return flocktest.Held(t, lock) }
	if locked() {
		t.Fatal("the pinning verb's release left the lock held")
	}
	// The server's client holds its own module cache, so its first
	// judgement fetches through the origin: held at the judgement's
	// seam, where the judgement is computed and not yet published,
	// released once it has.
	// The origin's redirect changes after the first judgement: the
	// second resolution names a second repository, served at its own
	// URL in the fixture's loader, holding the same release.
	redirected := gittest.NewAt(t, fx.Repo.FS, "redirected")
	moved := redirected.CommitTree(fx.TreeFor(redirected, stdModule()), "release", fetchtest.GitWhen)
	redirected.Ref("refs/tags/v1.0.0", moved)
	redirected.Ref("refs/heads/main", moved)
	redirected.Symref("HEAD", "refs/heads/main")
	var cache *fetch.Cache
	var resolved atomic.Int32
	fx.ResolveOverride = func(ctx context.Context, modPath string) (srcorigin.Origin, error) {
		repo := "file:///"
		if resolved.Add(1) > 1 {
			repo = "file:///redirected"
		}
		return srcorigin.Origin{Repo: repo, Subtree: fx.Subtrees[modPath]}, nil
	}
	fx.wireClient = func(c *fetch.Client) {
		c.Fetcher.Store = osfs.New(store)
		cache = c.Cache
	}
	fx.start(t)
	h := fx.holdNext()
	fx.initialize(t, protocol.ClientCapabilities{})
	h.held(t, "the first judgement")
	if !locked() {
		t.Fatal("the judgement computed with the origin's lock free: it fetched through nothing")
	}
	close(h.release)
	// The judgement over the direct source is the verb's: computed
	// to the end, the module fetched, not a failure published.
	fx.parity(t, fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work"), verb.String())
	fx.until(t, "the origin's lock released after the judgement", func() bool { return !locked() })
	if n := resolved.Load(); n != 1 {
		t.Fatalf("the origin resolved %d times by the first judgement, want once", n)
	}
	// A redirect changed between two judgements is followed by the
	// next: the release forgot the origin, so a reload that fetches
	// again — the cache emptied — resolves it anew, opens the
	// repository the new answer names, holds its lock at the seam and
	// releases it after; the judgement, over the same release, is
	// unchanged, so nothing is published again.
	entries, err := cache.FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := util.RemoveAll(cache.FS, e.Name()); err != nil {
			t.Fatal(err)
		}
	}
	h = fx.holdNext()
	fx.watched(t, "ws/pb.lock", protocol.FileChangeTypeChanged)
	h.held(t, "the reload's judgement")
	if n := resolved.Load(); n != 2 {
		t.Fatalf("the origin resolved %d times by the reload, want twice", n)
	}
	origins, err = os.ReadDir(store)
	if err != nil || len(origins) != 2 {
		t.Fatalf("the store after the reload: %v, %v; want the redirected origin opened beside the first", origins, err)
	}
	var redirectedLock string
	for _, o := range origins {
		if filepath.Join(store, o.Name(), "lock") != lock {
			redirectedLock = filepath.Join(store, o.Name(), "lock")
		}
	}
	if !flocktest.Held(t, redirectedLock) {
		t.Fatal("the reload's judgement computed with the redirected origin's lock free")
	}
	close(h.release)
	fx.until(t, "the redirected origin's lock released after the reload", func() bool { return !flocktest.Held(t, redirectedLock) })
	// The reload's judgement succeeded: the same diagnostics stand,
	// nothing published again, no failure reported.
	fx.none(t, "ws/pb.work")
	select {
	case m := <-fx.client.messages:
		t.Fatalf("the reload's judgement reported: %q", m)
	default:
	}
}
