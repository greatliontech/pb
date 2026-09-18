// Package direct implements the fetch layer of the `direct` source
// (module-proxy.md §Client behavior): fetching a module's origin
// repository and binding versions to commits over its materialized
// commit graph. Artifact construction — the proxy-equivalence half of
// REQ-proxy-direct-equivalence — builds on this layer.
//
// Transport is go-git, as in internal/origin and for the same reasons:
// pb stays self-contained with no runtime dependency on an installed
// git, the operations are protocol-level, and the library line is
// pinned in go.mod. The clone is bare, in memory, all branches and tags
// with full history: pseudo-version enforcement needs ancestry
// (REQ-resolve-pseudo-base), which a shallow or single-ref fetch cannot
// answer.
package direct

import (
	"context"
	"fmt"
	"slices"
	"strings"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"
	"github.com/greatliontech/pb/internal/origin"
)

// Fetcher fetches origin repositories. The zero value is ready to use.
type Fetcher struct {
	// ClientOptions extends the transport client per fetch — the
	// transport seam: fixture tests (here and in consumers) root the
	// file-transport loader in an in-memory filesystem, keeping the
	// whole filesystem out of the tests' observed inputs.
	ClientOptions []client.Option
}

// Fetch clones the origin repository: bare, in memory, every branch and
// tag with full history. "Present at the origin" everywhere above this
// seam means present in what the origin serves for its advertised heads
// and tags — a commit reachable only from refs outside those namespaces
// is absent for resolution exactly as it is for `git clone`. An origin
// padding its pack with unreachable objects widens presence, and gains
// nothing by it: the origin controls its refs outright, and nothing is
// trusted on presence alone — every artifact still verifies per its own
// contract.
func (f Fetcher) Fetch(ctx context.Context, repoURL string) (*Repo, error) {
	// The nil worktree filesystem is what makes the clone bare — go-git
	// derives bareness from it, and checkout never happens without a
	// worktree — so no clone option restates either.
	r, err := git.CloneContext(ctx, memory.NewStorage(), nil, &git.CloneOptions{
		URL:           repoURL,
		Tags:          git.AllTags,
		ClientOptions: f.ClientOptions,
	})
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", repoURL, err)
	}
	return &Repo{r: r}, nil
}

// Repo is a fetched origin repository: the refs and commit graph the
// direct source resolves versions against. The views a resolution
// reads repeatedly — the ref listing, the commit index behind
// pseudo-version prefix lookup, per-head ancestry — are derived lazily
// from the fetched storage and memoized: the storage is immutable
// after Fetch, so a derived view can never diverge from it, and one
// Repo answers every version of an origin at one snapshot — caching a
// pure function changes cost, never results. Not safe for concurrent
// use, matching the single-threaded resolution pipeline above it.
type Repo struct {
	r *git.Repository

	refs     []origin.Ref
	refsDone bool

	commits map[string]*object.Commit // full hash -> commit
	hashes  []string                  // commit hashes, sorted
	indexed bool

	ancestry map[string]map[string]bool // commit hash -> reachability set
}

// Head resolves the origin's default-branch head commit — the fallback
// pseudo-version's commit for a tagless synthesized module
// (REQ-resolve-synthesized-tags). The clone's HEAD follows the origin's
// HEAD advertisement.
func (r *Repo) Head() (origin.Commit, error) {
	ref, err := r.r.Head()
	if err != nil {
		return origin.Commit{}, fmt.Errorf("resolving origin HEAD: %w", err)
	}
	c, err := r.r.CommitObject(ref.Hash())
	if err != nil {
		return origin.Commit{}, fmt.Errorf("reading HEAD commit %s: %w", ref.Hash(), err)
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

// Refs lists the clone's branch and tag refs in the advertised shape
// origin.ReleaseTags consumes: an annotated tag contributes its tag
// object ref and a fully dereferenced `^{}` entry, exactly as a
// reference listing advertises it. The result is sorted by name, so
// storage iteration order never leaks. The listing is computed once
// per Repo: callers share the fetch-time snapshot.
func (r *Repo) Refs() ([]origin.Ref, error) {
	if r.refsDone {
		return r.refs, nil
	}
	refs, err := r.listRefs()
	if err != nil {
		return nil, err
	}
	r.refs, r.refsDone = refs, true
	return refs, nil
}

func (r *Repo) listRefs() ([]origin.Ref, error) {
	iter, err := r.r.References()
	if err != nil {
		return nil, fmt.Errorf("listing refs: %w", err)
	}
	var out []origin.Ref
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().String()
		if ref.Type() != plumbing.HashReference ||
			(!strings.HasPrefix(name, "refs/heads/") && !strings.HasPrefix(name, "refs/tags/")) {
			return nil
		}
		out = append(out, origin.Ref{Name: name, Hash: ref.Hash().String()})
		if peeled, ok := r.peel(ref.Hash()); ok && peeled != ref.Hash() {
			out = append(out, origin.Ref{Name: name + "^{}", Hash: peeled.String()})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing refs: %w", err)
	}
	slices.SortFunc(out, func(a, b origin.Ref) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// peel fully dereferences a chain of annotated tag objects, returning
// the ultimate non-tag object's hash. A non-tag input peels to itself.
// Tag objects are content-addressed, so a reference cycle cannot be
// encoded; the chain always terminates.
func (r *Repo) peel(h plumbing.Hash) (plumbing.Hash, bool) {
	for {
		tag, err := r.r.TagObject(h)
		if err != nil {
			if err == plumbing.ErrObjectNotFound {
				// Not a tag object: already fully peeled (or absent, in
				// which case the caller's own lookup reports it).
				return h, true
			}
			return plumbing.ZeroHash, false
		}
		h = tag.Target
	}
}
