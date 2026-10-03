// Package direct implements the fetch layer of the `direct` source
// (module-proxy.md §Client behavior): fetching a module's origin
// repository and binding versions to commits over its commit graph.
// Artifact construction — the proxy-equivalence half of
// REQ-proxy-direct-equivalence — builds on this layer.
//
// Transport is go-git, as in internal/source/origin and for the same reasons:
// pb stays self-contained with no runtime dependency on an installed
// git, the operations are protocol-level, and the library line is
// pinned in go.mod. The origin is fetched by need into two bare
// repositories kept in the module cache and reused across runs
// (REQ-proxy-direct-fetch): the snapshots, holding each commit a
// decision or an artifact needed with its tree, fetched at depth one
// and never deepened; and the history, holding the commit graph — the
// commits and tag objects reachable from every head and tag, no tree
// or blob, through git's object filter where the origin offers one
// and whole where it does not — fetched for a pseudo-version's
// decision alone. Two repositories because go-git tells an origin
// what the store holds by its refs, and a commit held without its
// tree would tell the origin the tree is in hand, the snapshot fetch
// then short of it. Opening an origin lists its refs and fetches
// nothing; every decision is made over that listing — a tag's or a
// head's hash is the listing's, the store's refs never read, and the
// commit graph is what the listing's heads and tags reach in the
// history, what earlier runs left behind counting for nothing. No
// depth stands in for a decision's need: an origin the size of
// googleapis is listed for nothing, its head fetched for one commit's
// tree, and its history — thirteen thousand commits, a few megabytes
// without their trees — reached only for a pseudo-version. An origin
// refusing the filter sends the whole history, trees and blobs with
// it; every fetch lands in a repository on the store's filesystem,
// the probe's throwaway one included, where go-git indexes a pack of
// any size within a delta base cache budget and one chain step (the
// fork pinned in go.mod carries that bound, upstream's parser having
// held every resolved object until the pack was parsed; a repository
// in memory, parsing the stream as it arrives, holds the pack
// decoded), so the whole fetch costs time and disk, never the
// process.
//
// An origin's repositories are held by one process at a time: the
// lock beside them is taken at opening and kept while an opening is
// unreleased, another run waiting at its opening. go-git writes a pack's index in
// place before the pack itself, so a reader beside a writer would
// read a partial index; the lock is coarse because the reads are
// everywhere and the writes are rare.
package direct

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage/filesystem"
	"github.com/greatliontech/pb/internal/source/origin"
)

// Fetcher opens origin repositories. The zero value is ready to use,
// keeping its repositories in memory.
type Fetcher struct {
	// ClientOptions extends the transport client per operation — the
	// transport seam: fixture tests (here and in consumers) root the
	// file-transport loader in an in-memory filesystem, keeping the
	// whole filesystem out of the tests' observed inputs.
	ClientOptions []client.Option
	// Store holds the repositories, one directory per origin URL,
	// reused across runs: the module cache's `vcs` directory (dep-verbs.md,
	// the module cache term). Nil keeps each in memory for the process.
	Store billy.Filesystem
}

// remoteName is the one remote of a store, the origin.
const remoteName = "origin"

// The layout of an origin's directory in the store (dep-verbs.md, the
// module cache term): the two repositories and the lock, and a
// probe's repository while a probe runs.
const (
	snapshotsDir = "snapshots"
	historyDir   = "history"
	probeDir     = "probe" // the prefix of a probe's directory, numbered per probe
	lockName     = "lock"
)

// StoreDir is the store's directory under the module cache root (the
// module cache term, dep-verbs.md).
const StoreDir = "vcs"

// Empty empties every origin's directory in the store of its
// repositories and probes, each under the origin's own lock, leaving
// the directory and its lock for the origin's next fetch to
// initialize into (REQ-dep-clean): the lock file stays because a
// process waiting on it holds the file open, and a recreated file
// would grant a second holder over a live claim. Only what the
// store's layout recognizes is touched — an origin's directory by its
// name, its entries by theirs — so a directory the cache setting
// named that is not the store's loses nothing. A store absent is
// empty already.
func Empty(ctx context.Context, store billy.Filesystem) error {
	origins, err := store.ReadDir(".")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("emptying the store: %w", err)
	}
	for _, origin := range origins {
		if !origin.IsDir() || !isOriginDir(origin.Name()) {
			continue
		}
		if err := hold(ctx, store, origin.Name()); err != nil {
			return fmt.Errorf("emptying the store: %s: %w", origin.Name(), err)
		}
		dir, err := store.Chroot(origin.Name())
		if err != nil {
			return fmt.Errorf("emptying the store: %s: %w", origin.Name(), err)
		}
		entries, err := dir.ReadDir(".")
		if err != nil {
			return fmt.Errorf("emptying the store: %s: %w", origin.Name(), err)
		}
		for _, e := range entries {
			if !isOriginEntry(e.Name()) {
				continue
			}
			if err := util.RemoveAll(dir, e.Name()); err != nil {
				return fmt.Errorf("emptying the store: %s: %w", origin.Name(), err)
			}
		}
	}
	return nil
}

// isOriginDir reports whether a name under the store is an origin's
// directory: the hex of the origin URL's SHA-256, as Fetch names it.
func isOriginDir(name string) bool {
	if len(name) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(name)
	return err == nil
}

// isOriginEntry reports whether a name under an origin's directory is
// the store's to empty: a repository or a probe, never the lock.
func isOriginEntry(name string) bool {
	return name == snapshotsDir || name == historyDir || strings.HasPrefix(name, probeDir+"-")
}

// Fetch opens the origin's repositories in the store, initializing
// them on first use, and lists the origin's refs — the one round trip
// every decision starts from, fetching no object. "Present at the
// origin" everywhere above this seam means present in what the origin
// serves for its advertised heads and tags — a commit reachable only
// from refs outside those namespaces is absent for resolution exactly
// as it is for `git clone`. An origin padding its pack with
// unreachable objects widens presence, and gains nothing by it: the
// origin controls its refs outright, and nothing is trusted on
// presence alone — every artifact still verifies per its own
// contract.
func (f Fetcher) Fetch(ctx context.Context, repoURL string) (*Repo, error) {
	store := f.Store
	if store == nil {
		store = memfs.New()
	}
	sum := sha256.Sum256([]byte(repoURL))
	dir := hex.EncodeToString(sum[:])
	if err := store.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("fetching %s: %w", repoURL, err)
	}
	if err := hold(ctx, store, dir); err != nil {
		return nil, fmt.Errorf("fetching %s: %w", repoURL, err)
	}
	// An opening that fails past the hold releases it: the lock is
	// the repository's, which no caller gets.
	opened := false
	defer func() {
		if !opened {
			release(store, dir)
		}
	}()
	origin, err := store.Chroot(dir)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", repoURL, err)
	}
	snap, snapSt, remote, err := openStore(store, dir, snapshotsDir, repoURL)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", repoURL, err)
	}
	hist, _, histRemote, err := openStore(store, dir, historyDir, repoURL)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", repoURL, err)
	}
	listed, err := remote.ListContext(ctx, &git.ListOptions{ClientOptions: f.ClientOptions, PeelingOption: git.AppendPeeled})
	if err != nil {
		return nil, fmt.Errorf("fetching %s: listing refs: %w", repoURL, err)
	}
	// Probe repositories a crashed run left behind go now: this
	// process holds the origin, so none is in use.
	if err := clearProbes(origin); err != nil {
		return nil, fmt.Errorf("fetching %s: %w", repoURL, err)
	}
	repo := newRepo(snap, snapSt, hist, listed)
	repo.origin = &connection{remote: remote, histRemote: histRemote, client: f.ClientOptions, dir: origin}
	repo.store, repo.dir = store, dir
	if len(repo.hashes) == 0 {
		return nil, fmt.Errorf("fetching %s: the origin advertises no ref", repoURL)
	}
	opened = true
	return repo, nil
}

// Close releases the opening's hold on the origin: the lock goes with
// the last opening released, and another process's opening proceeds
// (REQ-proxy-direct-fetch). A repository opened in place, or one
// closed already, releases nothing.
func (r *Repo) Close() {
	if r.store == nil || r.closed {
		return
	}
	r.closed = true
	release(r.store, r.dir)
}

// held is the process's table of origin locks: one process holds an
// origin's lock once, however many times it opens the origin — a
// second client in one run would otherwise wait on the run's own
// lock — and holds it while any opening of the origin is unreleased
// (REQ-proxy-direct-fetch): a verb never releases, its scope the
// process; the language server releases what a judgement opened at
// the judgement's end (lsp.md REQ-lsp-session). The lock is a file
// lock, so the table is per process by nature; what the process
// still holds at its end is released with it.
var held = struct {
	sync.Mutex
	locks map[string]*originLock
}{locks: map[string]*originLock{}}

// originLock is one origin's lock being taken or held: the taking
// runs on its own goroutine, so a waiting opening answers its context
// while the file lock, which cannot be interrupted, is still waited
// for. The openings waiting are counted: a lock won after every
// waiter gave up is released again, never held for nobody; the
// openings holding are counted too, the lock released with the last
// of them.
type originLock struct {
	done    chan struct{}
	err     error
	waiters int
	holders int
	file    billy.File // the locked file, kept open — and reachable — while held
}

// hold takes the origin's lock in the store, exclusive, waiting for
// another process to release it or for the context to end. A store
// whose files carry no lock (the in-memory filesystem) is the
// process's own, and needs none.
func hold(ctx context.Context, store billy.Filesystem, dir string) error {
	key := lockKey(store, dir)
	held.Lock()
	l, ok := held.locks[key]
	if !ok {
		l = &originLock{done: make(chan struct{})}
		held.locks[key] = l
		go func() {
			f, err := lockFile(store, store.Join(dir, lockName))
			held.Lock()
			defer held.Unlock()
			if f != nil && l.waiters == 0 {
				_ = f.(billy.Locker).Unlock()
				_ = f.Close()
				f = nil
			}
			if f == nil {
				delete(held.locks, key)
			}
			l.file, l.err = f, err
			close(l.done)
		}()
	}
	l.waiters++
	held.Unlock()
	select {
	case <-l.done:
		held.Lock()
		l.waiters--
		if l.err == nil {
			l.holders++
		}
		held.Unlock()
		return l.err
	case <-ctx.Done():
		held.Lock()
		l.waiters--
		select {
		case <-l.done:
			// Won in the same instant as this opening gave up: the
			// lock is held for the other openings, or for nobody —
			// then released.
			if l.err == nil && l.waiters == 0 && l.holders == 0 {
				unlock(key, l)
			}
		default:
		}
		held.Unlock()
		return fmt.Errorf("waiting for the store's lock: %w", ctx.Err())
	}
}

// unlock releases and forgets an origin's lock, under held's mutex.
// The entry is this lock's: the taking forgets an entry whose file
// carries no lock itself, and a waiter that gave up finds the taking
// done only where the file is held and the entry still stands.
func unlock(key string, l *originLock) {
	if l.file != nil {
		_ = l.file.(billy.Locker).Unlock()
		_ = l.file.Close()
	}
	delete(held.locks, key)
}

// release gives back one opening's hold on the origin's lock: the
// lock is released and forgotten with the last holder, so another
// process's opening proceeds; an origin whose store carries no lock
// has nothing to release.
func release(store billy.Filesystem, dir string) {
	key := lockKey(store, dir)
	held.Lock()
	defer held.Unlock()
	l, ok := held.locks[key]
	if !ok {
		return
	}
	l.holders--
	if l.holders > 0 || l.waiters > 0 {
		return
	}
	unlock(key, l)
}

// lockKey names an origin's lock in the process's table: the store
// and the origin's directory in it.
func lockKey(store billy.Filesystem, dir string) string { return store.Root() + "\x00" + dir }

// lockFile opens the lock file and locks it, returning the locked
// file to keep open while held; a file that carries no lock is
// closed again and nothing returned.
func lockFile(store billy.Filesystem, path string) (billy.File, error) {
	f, err := store.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening the store's lock: %w", err)
	}
	locker, ok := f.(billy.Locker)
	if !ok {
		return nil, f.Close()
	}
	if err := locker.Lock(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("locking the store: %w", err)
	}
	return f, nil
}

// openStore opens one of an origin's bare repositories in the store,
// initializing it on first use, with the origin as its one remote —
// a store keyed by the origin's URL holds one origin ever, so an
// existing remote is the same.
func openStore(store billy.Filesystem, dir, name, repoURL string) (*git.Repository, *filesystem.Storage, *git.Remote, error) {
	path := store.Join(dir, name)
	if err := store.MkdirAll(path, 0o755); err != nil {
		return nil, nil, nil, err
	}
	sub, err := store.Chroot(path)
	if err != nil {
		return nil, nil, nil, err
	}
	st := filesystem.NewStorage(sub, cache.NewObjectLRUDefault())
	r, err := git.Open(st, nil)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		if r, err = git.Init(st); err != nil {
			return nil, nil, nil, fmt.Errorf("initializing the %s store: %w", name, err)
		}
	} else if err != nil {
		return nil, nil, nil, fmt.Errorf("opening the %s store: %w", name, err)
	}
	remote, err := r.Remote(remoteName)
	if errors.Is(err, git.ErrRemoteNotFound) {
		remote, err = r.CreateRemote(&config.RemoteConfig{Name: remoteName, URLs: []string{repoURL}})
	}
	if err != nil {
		return nil, nil, nil, err
	}
	return r, st, remote, nil
}

// Repo is an origin opened in the store: the origin's ref listing,
// and behind it the two repositories the listing's needs are fetched
// into. The views a resolution reads repeatedly — the ref listing,
// the commit index behind pseudo-version prefix lookup, per-commit
// ancestry — are derived lazily and memoized: one Repo answers every
// version of an origin at one listing, and caching a pure function
// changes cost, never results. Not safe for concurrent use, matching
// the single-threaded resolution pipeline above it.
type Repo struct {
	r      *git.Repository // the snapshots: each needed commit with its tree, at depth one
	st     *filesystem.Storage
	hist   *git.Repository // the history: the commit graph, no tree or blob, never shallow
	origin *connection     // nil for a repository opened in place, every object present

	listed     []*plumbing.Reference    // the origin's advertised refs, peeled entries appended
	hashes     map[string]plumbing.Hash // the listing's hash refs by name
	headBranch plumbing.ReferenceName
	headHash   plumbing.Hash

	fetchedTags    map[string]bool // tag refs fetched into the snapshots
	headFetched    bool
	historyFetched bool

	store  billy.Filesystem // the store the opening holds the origin's lock in; nil for a repository opened in place
	dir    string
	closed bool

	refs     []origin.Ref
	refsDone bool

	commits map[string]*object.Commit       // full hash -> commit, the graph the listing reaches
	sorted  []string                        // commit hashes, sorted
	targets map[plumbing.Hash]plumbing.Hash // listed ref hash -> the commit it peels to, zero for none
	indexed bool

	ancestry map[string]map[string]bool // commit hash -> reachability set
}

// connection is what a Repo opened over an origin fetches with: the
// two repositories' remotes, the transport client's options, and the
// origin's directory in the store, where a probe's repository lives
// while a probe runs. A Repo opened in place has none, and fetches
// nothing.
type connection struct {
	remote     *git.Remote
	histRemote *git.Remote
	client     []client.Option
	dir        billy.Filesystem
}

// probes names the probes this process runs, each in a directory of
// its own, so two repositories over one origin in one process — the
// lock is the process's — never share one: a counter for the names,
// and the names in flight, which clearing an origin's residue leaves
// alone.
var probes = struct {
	sync.Mutex
	count uint64
	inUse map[string]bool
}{inUse: map[string]bool{}}

// openProbe takes a probe directory's name and marks it in flight.
func openProbe() string {
	probes.Lock()
	defer probes.Unlock()
	probes.count++
	name := fmt.Sprintf("%s-%d", probeDir, probes.count)
	probes.inUse[name] = true
	return name
}

// closeProbe removes a probe's directory and its mark.
func closeProbe(dir billy.Filesystem, name string) {
	probes.Lock()
	defer probes.Unlock()
	delete(probes.inUse, name)
	_ = util.RemoveAll(dir, name)
}

// clearProbes removes the probe repositories an origin's directory
// holds from runs that ended mid-probe, leaving this process's own
// probes in flight.
func clearProbes(dir billy.Filesystem) error {
	entries, err := dir.ReadDir(".")
	if err != nil {
		return err
	}
	probes.Lock()
	defer probes.Unlock()
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), probeDir) && !probes.inUse[e.Name()] {
			if err := util.RemoveAll(dir, e.Name()); err != nil {
				return fmt.Errorf("clearing %s: %w", e.Name(), err)
			}
		}
	}
	return nil
}

// newRepo builds a Repo over its repositories and a listing: the
// listing's hash refs indexed by name, its HEAD read as the origin
// advertises it — a symbolic ref to the default branch where the
// origin says so, else the hash alone.
func newRepo(snap *git.Repository, st *filesystem.Storage, hist *git.Repository, listed []*plumbing.Reference) *Repo {
	repo := &Repo{r: snap, st: st, hist: hist, listed: listed, hashes: map[string]plumbing.Hash{}, fetchedTags: map[string]bool{}}
	for _, ref := range listed {
		switch {
		case ref.Name() == plumbing.HEAD && ref.Type() == plumbing.SymbolicReference:
			repo.headBranch = ref.Target()
		case ref.Name() == plumbing.HEAD:
			repo.headHash = ref.Hash()
		case ref.Type() == plumbing.HashReference && !strings.HasSuffix(ref.Name().String(), "^{}"):
			repo.hashes[ref.Name().String()] = ref.Hash()
		}
	}
	return repo
}

// fetch runs one fetch of the origin into a repository: the refspecs
// at the depth given, the objects filtered where asked; nothing new
// no error. Its two callers are the two forms a fetch takes —
// fetchOne, one ref at depth one with every object, and
// fetchHistory, every head and tag whole — so no other depth or
// filter is expressible.
func (r *Repo) fetch(ctx context.Context, remote *git.Remote, depth int, filter packp.Filter, specs ...config.RefSpec) error {
	err := remote.FetchContext(ctx, &git.FetchOptions{
		RemoteName:    remoteName,
		RefSpecs:      specs,
		Depth:         depth,
		Filter:        filter,
		Tags:          plumbing.NoTags,
		Force:         true,
		ClientOptions: r.origin.client,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) && !errors.Is(err, transport.ErrNoChange) {
		return err
	}
	return nil
}

// fetchOne fetches one ref at depth one with every object it needs:
// the form every fetch but the history's takes.
func (r *Repo) fetchOne(ctx context.Context, remote *git.Remote, spec config.RefSpec) error {
	return r.fetch(ctx, remote, 1, "", spec)
}

// ensureTag fetches a tag ref into the snapshots at depth one — the
// tag's objects and its commit with its tree, the history behind it
// left at the origin — and holds the listing's hash for it present
// after: a tag the origin moved since the listing is reported, never
// read as what the listing named.
func (r *Repo) ensureTag(ctx context.Context, name string) error {
	if r.origin == nil || r.fetchedTags[name] {
		return nil
	}
	h, ok := r.hashes[name]
	if !ok {
		return nil
	}
	if err := r.fetchListed(ctx, r.origin.remote, r.r, name, h); err != nil {
		return err
	}
	r.fetchedTags[name] = true
	return nil
}

// fetchListed fetches a listed ref by its name into a repository at
// depth one and holds the listing's object present after: an
// object the listing named absent after the fetch that should have
// brought it means the origin moved the ref between the listing and
// the fetch, reported rather than read as what the listing named.
func (r *Repo) fetchListed(ctx context.Context, remote *git.Remote, repo *git.Repository, name string, h plumbing.Hash) error {
	if err := r.fetchOne(ctx, remote, config.RefSpec("+"+name+":"+name)); err != nil {
		return fmt.Errorf("fetching %s: %w", name, err)
	}
	if _, err := repo.Object(plumbing.AnyObject, h); err != nil {
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			return fmt.Errorf("fetching %s: the origin moved since the listing: %s is no longer served", name, h)
		}
		return fmt.Errorf("fetching %s: %w", name, err)
	}
	return nil
}

// headCommit is the listing's hash for the origin's HEAD: the default
// branch's where HEAD is symbolic, the advertised hash otherwise.
func (r *Repo) headCommit() (plumbing.Hash, error) {
	switch {
	case r.headBranch != "":
		h, ok := r.hashes[r.headBranch.String()]
		if !ok {
			return plumbing.ZeroHash, fmt.Errorf("resolving origin HEAD: %s is not advertised", r.headBranch)
		}
		return h, nil
	case !r.headHash.IsZero():
		return r.headHash, nil
	}
	return plumbing.ZeroHash, errors.New("resolving origin HEAD: the origin advertises none")
}

// ensureHead fetches the origin's default-branch head into the
// snapshots at depth one: by the branch's name where HEAD is
// symbolic, by its hash where the origin advertises the commit alone.
func (r *Repo) ensureHead(ctx context.Context) error {
	if r.origin == nil || r.headFetched {
		return nil
	}
	h, err := r.headCommit()
	if err != nil {
		return err
	}
	if r.headBranch == "" {
		if err := r.ensureCommit(ctx, h); err != nil {
			return fmt.Errorf("fetching the origin's HEAD: %w", err)
		}
	} else if err := r.fetchListed(ctx, r.origin.remote, r.r, r.headBranch.String(), h); err != nil {
		return fmt.Errorf("fetching the origin's HEAD: %w", err)
	}
	r.headFetched = true
	return nil
}

// commitRef is the one local ref a by-hash fetch lands on, overwritten
// by the next: a handle for the fetch, the objects kept regardless.
const commitRef = "refs/pb/commit"

// ensureCommit fetches a commit the snapshots lack at depth one by its
// hash — its tree with it — where the origin serves commits by hash,
// as an artifact of a commit resolved before or named outright needs.
func (r *Repo) ensureCommit(ctx context.Context, h plumbing.Hash) error {
	if r.origin == nil {
		return nil
	}
	if _, err := r.r.CommitObject(h); err == nil {
		return nil
	}
	if err := r.fetchOne(ctx, r.origin.remote, config.RefSpec("+"+h.String()+":"+commitRef)); err != nil {
		return fmt.Errorf("fetching commit %s: %w", h, err)
	}
	return nil
}

// ensureHistory fetches the commit graph into the history once per
// opening.
func (r *Repo) ensureHistory(ctx context.Context) error {
	if r.origin == nil || r.historyFetched {
		return nil
	}
	if err := r.fetchHistory(ctx); err != nil {
		return fmt.Errorf("fetching the history: %w", err)
	}
	r.historyFetched = true
	return nil
}

// fetchHistory fetches every head and tag whole into the history, the
// commits and tag objects alone where the origin offers git's object
// filter and every object where it refuses one. The history is never
// fetched at a depth, so it is never shallow.
func (r *Repo) fetchHistory(ctx context.Context) error {
	specs := []config.RefSpec{"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"}
	err := r.fetch(ctx, r.origin.histRemote, 0, packp.FilterTreeDepth(0), specs...)
	if errors.Is(err, transport.ErrFilterNotSupported) {
		err = r.fetch(ctx, r.origin.histRemote, 0, "", specs...)
	}
	return err
}

// Head resolves the origin's default-branch head commit — the fallback
// pseudo-version's commit for a tagless synthesized module
// (REQ-resolve-synthesized-tags) — the listing's hash, its commit
// fetched at depth one where the snapshots lack it.
func (r *Repo) Head(ctx context.Context) (origin.Commit, error) {
	if err := r.ensureHead(ctx); err != nil {
		return origin.Commit{}, err
	}
	h, err := r.headCommit()
	if err != nil {
		return origin.Commit{}, err
	}
	c, err := r.r.CommitObject(h)
	if err != nil {
		return origin.Commit{}, fmt.Errorf("reading HEAD commit %s: %w", h, err)
	}
	return commitIdentity(c), nil
}

// commitIdentity maps a commit to its resolution-relevant identity. The
// commit time is the committer timestamp — the time the commit entered
// history, which is what a pseudo-version embeds — not the author
// timestamp, which survives rebases and cherry-picks unchanged.
func commitIdentity(c *object.Commit) origin.Commit {
	return origin.Commit{Hash: c.Hash.String(), Time: c.Committer.When}
}

// Refs lists the origin's branch and tag refs in the advertised shape
// origin.ReleaseTags consumes: heads and tags, an annotated tag
// contributing its tag object ref and the fully dereferenced `^{}`
// entry the listing appended. The result is sorted by name, so
// iteration order never leaks. Computed once per Repo: callers share
// the snapshot.
func (r *Repo) Refs() ([]origin.Ref, error) {
	if r.refsDone {
		return r.refs, nil
	}
	var out []origin.Ref
	for _, ref := range r.listed {
		name := ref.Name().String()
		base := strings.TrimSuffix(name, "^{}")
		if ref.Type() != plumbing.HashReference ||
			(!strings.HasPrefix(base, "refs/heads/") && !strings.HasPrefix(base, "refs/tags/")) {
			continue
		}
		out = append(out, origin.Ref{Name: name, Hash: ref.Hash().String()})
	}
	slices.SortFunc(out, func(a, b origin.Ref) int { return strings.Compare(a.Name, b.Name) })
	r.refs, r.refsDone = out, true
	return out, nil
}

// peel fully dereferences a chain of annotated tag objects in a
// repository, returning the ultimate non-tag object's hash and, where
// a tag object named it, the type the tag declares for it (AnyObject
// where no tag object was read, the hash's own). A non-tag input
// peels to itself. Tag objects are content-addressed, so a reference
// cycle cannot be encoded; the chain always terminates.
func peel(repo *git.Repository, h plumbing.Hash) (plumbing.Hash, plumbing.ObjectType, bool) {
	typ := plumbing.AnyObject
	for {
		tag, err := repo.TagObject(h)
		if err != nil {
			if err == plumbing.ErrObjectNotFound {
				// Not a tag object: already fully peeled (or absent, in
				// which case the caller's own lookup reports it).
				return h, typ, true
			}
			return plumbing.ZeroHash, typ, false
		}
		h, typ = tag.Target, tag.TargetType
	}
}
