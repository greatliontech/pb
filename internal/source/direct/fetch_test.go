package direct

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/plumbing/format/config"
	"github.com/go-git/go-git/v6/plumbing/format/packfile"
	packutil "github.com/go-git/go-git/v6/plumbing/format/packfile/util"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage/filesystem"
	gogitbinary "github.com/go-git/go-git/v6/utils/binary"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/testing/gittest"
	"github.com/greatliontech/pb/internal/testing/scratchtest"
)

// chain is an origin of three commits on one branch, the first two
// released under annotated tags, the third the branch's head.
type chain struct {
	*repoFixture
	c1, c2, c3 plumbing.Hash
	t0         time.Time
}

func newChain(t *testing.T) chain {
	f := newFixture(t)
	t0 := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	c1 := f.Commit("one", t0)
	c2 := f.Commit("two", t0.Add(time.Hour), c1)
	c3 := f.Commit("three", t0.Add(2*time.Hour), c2)
	f.AnnotatedTag("v1.0.0", c1, plumbing.CommitObject, t0)
	f.AnnotatedTag("v1.1.0", c2, plumbing.CommitObject, t0.Add(time.Hour))
	f.Ref("refs/heads/main", c3)
	f.Head("main")
	return chain{f, c1, c2, c3, t0}
}

// inSnapshots and inHistory report a commit's presence in each
// repository.
func inSnapshots(repo *Repo, h plumbing.Hash) bool {
	_, err := repo.r.CommitObject(h)
	return err == nil
}

func inHistory(repo *Repo, h plumbing.Hash) bool {
	_, err := repo.hist.CommitObject(h)
	return err == nil
}

func mustV(t *testing.T, s string) version.Version {
	t.Helper()
	v, err := version.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// need renders a recorded fetch request as the need it expressed:
// its wants sorted, its depth, its filter.
func need(req *transport.FetchRequest) string {
	var wants []string
	for _, h := range req.Wants {
		wants = append(wants, h.String()[:7])
	}
	slices.Sort(wants)
	return "wants " + strings.Join(wants, ",") + " depth " + strconv.Itoa(req.Depth) + " filter " + string(req.Filter)
}

// needs renders the requests recorded since the last call, in order.
func (r *recorder) needs() []string {
	var out []string
	for _, req := range r.reqs {
		out = append(out, need(req))
	}
	r.reqs = nil
	return out
}

func short(h plumbing.Hash) string { return h.String()[:7] }

// The origin is fetched by need (REQ-proxy-direct-fetch), each fetch
// asking exactly what its decision needs: opening lists the refs and
// fetches nothing; a release version its tag alone at depth one; the
// head its commit at depth one; a pseudo-version the history — every
// head and tag, whole, commits and tags alone where the origin offers
// the filter and whole where it refuses, as the in-process server
// does — into the history repository, the snapshots left as they
// were; a second opening over the store finds what the first fetched.
func TestFetchByNeed(t *testing.T) {
	ctx := context.Background()
	c := newChain(t)
	opts, rec := c.recording()
	store := memfs.New()
	fe := Fetcher{ClientOptions: opts, Store: store}
	repo, err := fe.Fetch(ctx, "file:///")
	if err != nil {
		t.Fatal(err)
	}
	// The listing, peeled entries included, with nothing fetched.
	refs, err := repo.Refs()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range refs {
		names = append(names, r.Name)
	}
	if strings.Join(names, ",") != "refs/heads/main,refs/tags/v1.0.0,refs/tags/v1.0.0^{},refs/tags/v1.1.0,refs/tags/v1.1.0^{}" {
		t.Fatalf("refs = %v", names)
	}
	if got := rec.needs(); len(got) != 0 || inSnapshots(repo, c.c3) || inHistory(repo, c.c3) {
		t.Fatalf("at the opening: fetched %v", got)
	}
	// A release version: its tag, at depth one, into the snapshots.
	v11 := mustV(t, "v1.1.0")
	tag11 := repo.hashes["refs/tags/v1.1.0"]
	got, err := repo.ResolveVersion(ctx, v11, "")
	if err != nil || got.Hash != c.c2.String() {
		t.Fatalf("v1.1.0: %+v %v", got, err)
	}
	if got := rec.needs(); len(got) != 1 || got[0] != "wants "+short(tag11)+" depth 1 filter " {
		t.Fatalf("a release version fetched: %v", got)
	}
	if !inSnapshots(repo, c.c2) || inSnapshots(repo, c.c3) || inHistory(repo, c.c2) {
		t.Fatalf("after a release: snapshots c2 %v c3 %v, history c2 %v", inSnapshots(repo, c.c2), inSnapshots(repo, c.c3), inHistory(repo, c.c2))
	}
	// Its artifacts, from what is in hand.
	if b, ok, err := repo.ModuleFileBytes(ctx, c.c2.String(), ""); err != nil || ok || b != nil {
		t.Fatalf("module file of a commit in hand: %q %v %v", b, ok, err)
	}
	if _, err := repo.ResolveVersion(ctx, v11, ""); err != nil {
		t.Fatal(err)
	}
	if got := rec.needs(); len(got) != 0 {
		t.Fatalf("a release version resolved again fetched: %v", got)
	}
	// An unknown version: refused from the listing, nothing fetched.
	if _, err := repo.ResolveVersion(ctx, mustV(t, "v9.0.0"), ""); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("an unknown version: %v", err)
	}
	if got := rec.needs(); len(got) != 0 {
		t.Fatalf("an unknown version fetched: %v", got)
	}
	// The head: its commit at depth one, into the snapshots.
	head, err := repo.Head(ctx)
	if err != nil || head.Hash != c.c3.String() || !inSnapshots(repo, c.c3) {
		t.Fatalf("head: %+v %v", head, err)
	}
	if got := rec.needs(); len(got) != 1 || got[0] != "wants "+short(c.c3)+" depth 1 filter " {
		t.Fatalf("the head fetched: %v", got)
	}
	// A pseudo-version: the history — every head and tag, whole, the
	// filter asked first and refused by the in-process server, the
	// whole fetched then — into the history repository; the base
	// derived from the tags on its ancestors.
	pseudo, err := version.PseudoVersion(&v11, head.Time, head.Hash)
	if err != nil {
		t.Fatal(err)
	}
	got, err = repo.ResolveVersion(ctx, pseudo, "")
	if err != nil || got.Hash != c.c3.String() {
		t.Fatalf("the head's pseudo-version: %+v %v", got, err)
	}
	tag10 := repo.hashes["refs/tags/v1.0.0"]
	whole := "wants " + strings.Join(slices.Sorted(slices.Values([]string{short(c.c3), short(tag10), short(tag11)})), ",") + " depth 0 filter "
	if got := rec.needs(); len(got) != 2 || got[0] != whole+string(packp.FilterTreeDepth(0)) || got[1] != whole {
		t.Fatalf("the history fetched: %v", got)
	}
	if !inHistory(repo, c.c1) || !inHistory(repo, c.c2) || !inHistory(repo, c.c3) {
		t.Fatalf("after a pseudo-version: history c1 %v c2 %v c3 %v", inHistory(repo, c.c1), inHistory(repo, c.c2), inHistory(repo, c.c3))
	}
	if _, err := repo.ResolveVersion(ctx, pseudo, ""); err != nil {
		t.Fatal(err)
	}
	if got := rec.needs(); len(got) != 0 {
		t.Fatalf("a pseudo-version resolved again fetched: %v", got)
	}
	// A second opening over the store: the listing again, the objects
	// already there, a release version in hand fetched for nothing.
	again, err := fe.Fetch(ctx, "file:///")
	if err != nil {
		t.Fatal(err)
	}
	if !inHistory(again, c.c1) || !inSnapshots(again, c.c3) {
		t.Fatal("the store not reused")
	}
	rec.needs()
	if got, err := again.ResolveVersion(ctx, v11, ""); err != nil || got.Hash != c.c2.String() {
		t.Fatalf("v1.1.0 over the reused store: %+v %v", got, err)
	}
	if got := rec.needs(); len(got) != 0 {
		t.Fatalf("a release version in hand fetched: %v", got)
	}
	// A pseudo-version over a fresh store: the history from nothing,
	// the snapshots untouched.
	fresh, err := Fetcher{ClientOptions: opts, Store: memfs.New()}.Fetch(ctx, "file:///")
	if err != nil {
		t.Fatal(err)
	}
	rec.needs()
	if got, err := fresh.ResolveVersion(ctx, pseudo, ""); err != nil || got.Hash != c.c3.String() || !inHistory(fresh, c.c1) || inSnapshots(fresh, c.c3) {
		t.Fatalf("a pseudo-version over a fresh store: %+v %v", got, err)
	}
	if got := rec.needs(); len(got) != 2 {
		t.Fatalf("the history over a fresh store fetched: %v", got)
	}
	// The zero fetcher keeps its store in memory for the process.
	if zero, err := (Fetcher{ClientOptions: c.ClientOptions()}).Fetch(ctx, "file:///"); err != nil {
		t.Fatal(err)
	} else if got, err := zero.ResolveVersion(ctx, v11, ""); err != nil || got.Hash != c.c2.String() {
		t.Fatalf("the zero fetcher: %+v %v", got, err)
	}
}

// A store keyed by the origin's URL holds one origin: two origins on
// one loader filesystem land in two stores, and one origin's listing
// never answers for the other. Each store is the two repositories
// and the lock.
func TestFetchStoresPerOrigin(t *testing.T) {
	ctx := context.Background()
	c := newChain(t)
	other := gittest.NewAt(t, c.FS, "other")
	other.Ref("refs/heads/main", other.Commit("elsewhere", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)))
	store := memfs.New()
	a, err := Fetcher{ClientOptions: c.ClientOptions(), Store: store}.Fetch(ctx, "file:///")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Fetcher{ClientOptions: c.ClientOptions(), Store: store}.Fetch(ctx, "file:///other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ResolveVersion(ctx, mustV(t, "v1.0.0"), ""); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("the other origin answering for the first: %v", err)
	}
	if got, err := a.ResolveVersion(ctx, mustV(t, "v1.0.0"), ""); err != nil || got.Hash != c.c1.String() {
		t.Fatalf("the first origin: %+v %v", got, err)
	}
	dirs, err := store.ReadDir(".")
	if err != nil || len(dirs) != 2 {
		t.Fatalf("stores: %v %v", dirs, err)
	}
	for _, d := range dirs {
		entries, err := store.ReadDir(d.Name())
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if strings.Join(names, ",") != "history,lock,snapshots" {
			t.Fatalf("the store of %s holds %v", d.Name(), names)
		}
	}
}

// Decisions are made over the listing an opening took, never over
// what the store holds from earlier runs: a tag the origin retracted
// is unknown though the snapshots hold it, and a commit the origin
// moved its head away from is absent though the history holds it.
func TestFetchDecidesOverListing(t *testing.T) {
	ctx := context.Background()
	c := newChain(t)
	store := memfs.New()
	fe := Fetcher{ClientOptions: c.ClientOptions(), Store: store}
	repo, err := fe.Fetch(ctx, "file:///")
	if err != nil {
		t.Fatal(err)
	}
	v11 := mustV(t, "v1.1.0")
	if _, err := repo.ResolveVersion(ctx, v11, ""); err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pseudo, err := version.PseudoVersion(&v11, head.Time, head.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ResolveVersion(ctx, pseudo, ""); err != nil {
		t.Fatal(err)
	}
	// The origin retracts the tag and moves its head to a commit
	// beside the old one.
	if err := c.St.RemoveReference("refs/tags/v1.1.0"); err != nil {
		t.Fatal(err)
	}
	c4 := c.Commit("four", c.t0.Add(3*time.Hour), c.c2)
	c.Ref("refs/heads/main", c4)
	again, err := fe.Fetch(ctx, "file:///")
	if err != nil {
		t.Fatal(err)
	}
	if !inSnapshots(again, c.c2) || !inHistory(again, c.c3) {
		t.Fatal("the store not reused")
	}
	if _, err := again.ResolveVersion(ctx, v11, ""); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("a retracted tag: %v", err)
	}
	if _, err := again.ResolveVersion(ctx, pseudo, ""); !errors.Is(err, ErrCommitAbsent) {
		t.Fatalf("a commit the head moved away from: %v", err)
	}
	v10 := mustV(t, "v1.0.0")
	moved, err := version.PseudoVersion(&v10, c.t0.Add(3*time.Hour), c4.String())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := again.ResolveVersion(ctx, moved, ""); err != nil || got.Hash != c4.String() {
		t.Fatalf("the new head's pseudo-version: %+v %v", got, err)
	}
}

// An origin that moves a ref between the listing and its fetch is
// reported, never read as what the listing named.
func TestFetchMovedSinceListing(t *testing.T) {
	ctx := context.Background()
	t.Run("a tag", func(t *testing.T) {
		c := newChain(t)
		repo, err := Fetcher{ClientOptions: c.ClientOptions()}.Fetch(ctx, "file:///")
		if err != nil {
			t.Fatal(err)
		}
		c.Ref("refs/tags/v1.1.0", c.c3)
		if _, err := repo.ResolveVersion(ctx, mustV(t, "v1.1.0"), ""); err == nil || !strings.Contains(err.Error(), "moved since the listing") {
			t.Fatalf("a tag moved before its fetch: %v", err)
		}
	})
	t.Run("the head", func(t *testing.T) {
		c := newChain(t)
		repo, err := Fetcher{ClientOptions: c.ClientOptions()}.Fetch(ctx, "file:///")
		if err != nil {
			t.Fatal(err)
		}
		c.Ref("refs/heads/main", c.c1)
		if _, err := repo.Head(ctx); err == nil || !strings.Contains(err.Error(), "moved since the listing") {
			t.Fatalf("a head moved before its fetch: %v", err)
		}
	})
}

// A listed ref the history lacks after its fetch is probed by its
// name without touching the history: a lightweight tag naming a blob,
// which the object filter leaves out, names no root and the blob is
// never stored; a ref naming what the origin no longer serves is
// reported; a commit the history's fetch left behind is reported.
func TestFetchHistoryRoots(t *testing.T) {
	ctx := context.Background()
	c := newChain(t)
	// The store on disk, as the module cache's is; a probe's
	// repository a crashed run left behind is cleared at opening.
	store := osfs.New(scratchtest.Dir(t))
	sum := sha256.Sum256([]byte("file:///"))
	stale := filepath.Join(hex.EncodeToString(sum[:]), probeDir+"-stale")
	if err := store.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	repo, err := Fetcher{ClientOptions: c.ClientOptions(), Store: store}.Fetch(ctx, "file:///")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a stale probe after the opening: %v", err)
	}
	if err := repo.ensureHistory(ctx); err != nil {
		t.Fatal(err)
	}
	// The origin gains refs after the history's fetch; the listing is
	// told of them as an opening's listing would have been.
	blob := c.Blob("not a commit\n")
	c.Tag("v2.0.0", blob)
	c.Tag("v3.0.0", c.c1)
	list := func(name string, h plumbing.Hash) {
		repo.listed = append(repo.listed, plumbing.NewHashReference(plumbing.ReferenceName(name), h))
		repo.hashes[name] = h
		repo.refs, repo.refsDone = nil, false
	}
	list("refs/tags/v2.0.0", blob)
	roots, err := repo.roots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 3 || roots[0] != c.c3 || roots[1] != c.c1 || roots[2] != c.c2 {
		t.Fatalf("roots = %v", roots)
	}
	if _, err := repo.hist.BlobObject(blob); err != nil {
		t.Fatalf("the blob kept in the history after the probe: %v", err)
	}
	// The probe's repository is gone with the probe.
	entries, err := repo.origin.dir.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), probeDir) {
			t.Fatalf("the probe's repository %s outlived the probe", e.Name())
		}
	}
	// The blob tag, a release by name, seeds no base: the head's
	// pseudo-version derives its base from v1.1.0 beside it.
	head, err := repo.hist.CommitObject(c.c3)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := repo.expectedPseudo(ctx, head, ""); err != nil || !strings.HasPrefix(v.String(), "v1.1.1-0.") {
		t.Fatalf("the head's pseudo-version beside a blob tag: %v %v", v, err)
	}
	// A listed hash the origin no longer serves under the name.
	bogus := plumbing.NewHash(strings.Repeat("ab", 20))
	list("refs/tags/v3.0.0", bogus)
	if _, err := repo.roots(ctx); err == nil || !strings.Contains(err.Error(), "moved since the listing") {
		t.Fatalf("a listed ref the origin moved: %v", err)
	}
	// The probes left the history as it was: never shallow.
	if shallows, err := repo.hist.Storer.(interface {
		Shallow() ([]plumbing.Hash, error)
	}).Shallow(); err != nil || len(shallows) != 0 {
		t.Fatalf("the history after the probes: shallow %v %v", shallows, err)
	}
	// A commit the history's fetch did not bring.
	list("refs/tags/v3.0.0", c.c1)
	repo.hist, _, _, err = openStore(memfs.New(), "x", historyDir, "file:///")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.roots(ctx); err == nil || !strings.Contains(err.Error(), "left out") {
		t.Fatalf("a commit the history lacks: %v", err)
	}
}

// An origin's store is held by one process at a time: an opening
// waits for the lock another holder has, and a process holds an
// origin's lock once however many times it opens the origin.
func TestFetchHoldsTheStore(t *testing.T) {
	ctx := context.Background()
	c := newChain(t)
	dir := scratchtest.Dir(t)
	store := osfs.New(dir)
	sum := sha256.Sum256([]byte("file:///"))
	origin := hex.EncodeToString(sum[:])
	if err := store.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	other, err := store.OpenFile(filepath.Join(origin, lockName), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.(billy.Locker).Lock(); err != nil {
		t.Fatal(err)
	}
	opened := make(chan error, 1)
	released := make(chan struct{})
	go func() {
		_, err := Fetcher{ClientOptions: c.ClientOptions(), Store: store}.Fetch(ctx, "file:///")
		select {
		case <-released:
			opened <- err
		default:
			opened <- errors.New("opened while another holder held the lock")
		}
	}()
	time.Sleep(200 * time.Millisecond)
	close(released)
	if err := other.(billy.Locker).Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := <-opened; err != nil {
		t.Fatal(err)
	}
	// The process holds the lock now: a second opening returns at once.
	done := make(chan error, 1)
	go func() {
		_, err := Fetcher{ClientOptions: c.ClientOptions(), Store: osfs.New(dir)}.Fetch(ctx, "file:///")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a second opening in the process waited on the process's own lock")
	}
	// The lock outlives the taking: the locked file stays reachable,
	// so no finalizer closes it — a fresh handle still waits after the
	// collector ran.
	for range 5 {
		runtime.GC()
	}
	assertLocked(t, filepath.Join(dir, origin, lockName))
	// An opening waiting on another holder's lock answers its context:
	// a second origin, its lock held by another handle, the opening's
	// context cancelled while it waits.
	sum2 := sha256.Sum256([]byte("file:///other"))
	origin2 := hex.EncodeToString(sum2[:])
	if err := store.MkdirAll(origin2, 0o755); err != nil {
		t.Fatal(err)
	}
	holder, err := store.OpenFile(filepath.Join(origin2, lockName), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.(billy.Locker).Lock(); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	if _, err := (Fetcher{ClientOptions: c.ClientOptions(), Store: store}).Fetch(cancelled, "file:///other"); !errors.Is(err, context.Canceled) {
		t.Fatalf("an opening waiting on a held lock: %v", err)
	}
	// The abandoned taking wins the lock once the holder releases it,
	// and releases it again: nobody asked for it. A fresh handle can
	// take it.
	if err := holder.(billy.Locker).Unlock(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	fresh, err := store.OpenFile(filepath.Join(origin2, lockName), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	locked := make(chan error, 1)
	go func() { locked <- fresh.(billy.Locker).Lock() }()
	select {
	case err := <-locked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the lock an abandoned opening won is still held")
	}
	_ = fresh.(billy.Locker).Unlock()
	_ = fresh.Close()
}

// A store whose ref storage is broken fails the fetch that reads it,
// loudly — never as an unknown version, which callers may treat as
// in-spec absence — though no decision reads the store's refs.
func TestFetchCorruptStoreFailsLoudly(t *testing.T) {
	ctx := context.Background()
	c := newChain(t)
	store := memfs.New()
	fe := Fetcher{ClientOptions: c.ClientOptions(), Store: store}
	repo, err := fe.Fetch(ctx, "file:///")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ResolveVersion(ctx, mustV(t, "v1.1.0"), ""); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("file:///"))
	packed := filepath.Join(hex.EncodeToString(sum[:]), snapshotsDir, "packed-refs")
	f, err := store.OpenFile(packed, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("this is not a packed-refs file\n@@garbage@@\n")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := fe.Fetch(ctx, "file:///")
	if err != nil {
		t.Fatalf("the opening over broken ref storage: %v", err)
	}
	if _, err := again.ResolveVersion(ctx, mustV(t, "v1.0.0"), ""); err == nil || errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("a fetch over broken ref storage: %v", err)
	}
}

// peakHeap samples the live heap every interval while fn runs and
// reports its largest growth over the heap before fn, collecting
// before every sample so garbage counts for nothing — an interval
// short enough to see the peak, long enough that the collections do
// not starve fn. The heap is the process's: the measure holds while
// no test in this package runs in parallel.
func peakHeap(interval time.Duration, fn func()) int64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base := ms.HeapAlloc
	var peak int64
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			runtime.GC()
			runtime.ReadMemStats(&ms)
			if g := int64(ms.HeapAlloc) - int64(base); g > peak {
				peak = g
			}
			select {
			case <-done:
				return
			case <-time.After(interval):
			}
		}
	}()
	fn()
	close(done)
	<-finished
	return peak
}

// A pack written into the store is indexed within a memory bounded
// independently of its decoded size (REQ-proxy-direct-fetch): the
// fetch path hands the arriving pack to the storage's pack writer,
// whose close builds the index by parsing it. A pack of 320 versions
// of a megabyte file, each a delta on the one before, decodes to
// 320 MiB, three times go-git's delta base cache budget; indexing it
// grows the live heap by less than the budget and a fixed allowance
// — one chain step, the pack writer's buffers, the index's tables —
// where the parser that held every resolved object grows it by the
// pack.
func TestPackIndexBounded(t *testing.T) {
	const versions, size = 320, 1 << 20
	const budget = packfile.DefaultDeltaBaseCacheLimit
	if versions*size < 3*budget {
		t.Fatalf("a pack of %d decoded bytes cannot witness a budget of %d", versions*size, budget)
	}
	pack, last := buildVersionsPack(t, versions, size)

	st := filesystem.NewStorage(osfs.New(scratchtest.Dir(t)), cache.NewObjectLRU(cache.MiByte))
	var indexErr error
	growth := peakHeap(20*time.Millisecond, func() {
		w, err := st.PackfileWriter()
		if err != nil {
			indexErr = err
			return
		}
		if _, err := w.Write(pack); err != nil {
			indexErr = err
			return
		}
		indexErr = w.Close()
	})
	if indexErr != nil {
		t.Fatal(indexErr)
	}
	if _, err := st.EncodedObject(plumbing.BlobObject, last); err != nil {
		t.Fatalf("the last version after indexing: %v", err)
	}
	if growth > budget+48<<20 {
		t.Fatalf("the live heap grew by %d bytes indexing a pack of %d decoded bytes under a budget of %d", growth, versions*size, budget)
	}
}

// buildVersionsPack returns a pack of one base blob and versions
// OFS-deltas, each a version of the base on the one before — the
// base's bytes with a distinct line appended — and the last
// version's hash.
func buildVersionsPack(t *testing.T, versions, size int) ([]byte, plumbing.Hash) {
	t.Helper()
	rnd := rand.New(rand.NewPCG(9, 10))
	body := make([]byte, size)
	for i := range body {
		body[i] = byte('a' + rnd.UintN(26))
	}
	blobHash := func(content []byte) plumbing.Hash {
		h := plumbing.NewHasher(config.SHA1, plumbing.BlobObject, int64(len(content)))
		_, _ = h.Write(content)
		return h.Sum()
	}
	header := func(w io.Writer, typ plumbing.ObjectType, n int64) {
		first := byte(typ)<<4 | byte(n&0x0F)
		rest := uint(n >> 4)
		if rest != 0 {
			first |= 0x80
		}
		_, _ = w.Write([]byte{first})
		if rest != 0 {
			_ = packutil.EncodeLEB128ToWriter(w, rest)
		}
	}
	payload := func(w io.Writer, b []byte) {
		zw := zlib.NewWriter(w)
		_, _ = zw.Write(b)
		_ = zw.Close()
	}

	var buf bytes.Buffer
	sum := sha1.New()
	w := io.MultiWriter(&buf, sum)
	_, _ = w.Write([]byte("PACK"))
	_ = binary.Write(w, binary.BigEndian, uint32(2))
	_ = binary.Write(w, binary.BigEndian, uint32(versions+1))

	prev := body
	prevOffset := int64(buf.Len())
	header(w, plumbing.BlobObject, int64(len(prev)))
	payload(w, prev)
	var last plumbing.Hash
	for i := 0; i < versions; i++ {
		next := append(append([]byte{}, body...), []byte(fmt.Sprintf("\nversion %d\n", i))...)
		last = blobHash(next)
		delta := packfile.DiffDelta(prev, next)
		offset := int64(buf.Len())
		header(w, plumbing.OFSDeltaObject, int64(len(delta)))
		_ = gogitbinary.WriteVariableWidthInt(w, offset-prevOffset)
		payload(w, delta)
		prev, prevOffset = next, offset
	}
	_, _ = buf.Write(sum.Sum(nil))
	return buf.Bytes(), last
}
