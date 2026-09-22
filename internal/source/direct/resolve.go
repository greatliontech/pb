package direct

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/source/origin"
)

// ErrUnknownVersion is wrapped when no tag at the origin names a
// requested release version.
var ErrUnknownVersion = errors.New("version has no tag at the origin")

// ErrCommitAbsent is wrapped when a pseudo-version names a commit
// absent from the origin (REQ-resolve-pseudo-commit): nothing reachable
// from the origin's heads and tags carries the embedded hash prefix.
var ErrCommitAbsent = errors.New("pseudo-version names a commit absent from the origin")

// ErrCommitAmbiguous is wrapped when a pseudo-version's embedded hash
// prefix is carried by more than one commit at the origin: the version
// binds no commit uniquely, and picking one would make resolution a
// function of repository iteration order (REQ-resolve-determinism).
var ErrCommitAmbiguous = errors.New("pseudo-version prefix is ambiguous at the origin")

// ErrBaseInconsistent is wrapped when a pseudo-version's base does not
// derive from the origin's release-tag history at the embedded commit
// (REQ-resolve-pseudo-base) — the crafted-version failure that would
// otherwise let a pseudo-version outrank real releases in selection.
var ErrBaseInconsistent = errors.New("pseudo-version base inconsistent with the origin's release tags")

// ResolveVersion binds a version to the commit it names for the module
// at subtree ("" for a module at the repository root, and for a
// synthesized module's repository-level namespace). A release version
// resolves through its tag (REQ-resolve-release-tags), the listing's,
// fetched at depth one where the snapshots lack it; a pseudo-version
// resolves only to the commit whose hash and time it embeds, present
// at the origin and with a base consistent with the module's
// release-tag history (REQ-resolve-pseudo-commit,
// REQ-resolve-pseudo-base), the commit graph fetched into the history
// for the decision.
func (r *Repo) ResolveVersion(ctx context.Context, v version.Version, subtree string) (origin.Commit, error) {
	if v.IsPseudo() {
		return r.resolvePseudo(ctx, v, subtree)
	}
	if err := r.ensureTag(ctx, tagRefName(v, subtree)); err != nil {
		return origin.Commit{}, fmt.Errorf("resolving %s: %w", v, err)
	}
	return r.resolveTag(v, subtree)
}

// tagRefName is the tag ref a release version of the module at subtree
// corresponds to (REQ-resolve-release-tags).
func tagRefName(v version.Version, subtree string) string {
	name := "refs/tags/"
	if subtree != "" {
		name += subtree + "/"
	}
	return name + v.String()
}

// tagHash is the listing's hash for the module's tag ref of a release
// version, mapping absence to ErrUnknownVersion; resolveTag and
// VerificationPack share the one lookup, and the store's refs are no
// part of it — a tag the origin retracted is unknown however many
// runs fetched it before.
func (r *Repo) tagHash(v version.Version, subtree string) (plumbing.Hash, error) {
	h, ok := r.hashes[tagRefName(v, subtree)]
	if !ok {
		return plumbing.ZeroHash, fmt.Errorf("%w: %s (tag %s)", ErrUnknownVersion, v, tagRefName(v, subtree))
	}
	return h, nil
}

func (r *Repo) resolveTag(v version.Version, subtree string) (origin.Commit, error) {
	h, err := r.tagHash(v, subtree)
	if err != nil {
		return origin.Commit{}, err
	}
	c, ok, err := tagCommit(r.r, h)
	if err != nil {
		return origin.Commit{}, fmt.Errorf("resolving %s: tag %s: %w", v, tagRefName(v, subtree), err)
	}
	if !ok {
		return origin.Commit{}, fmt.Errorf("resolving %s: tag %s does not name a commit", v, tagRefName(v, subtree))
	}
	return commitIdentity(c), nil
}

// tagCommit peels a tag ref's target in a repository and reads the
// commit it names. ok=false reports the one in-spec non-release shape
// — a tag ultimately naming a non-commit object, decided by the tag
// object's declared target type where one was read, so the history,
// which holds no tree or blob, decides it without the object.
// Everything else (an undecodable tag object, a peeled target absent
// from the repository) is corruption and errors: both callers must
// fail loudly on it, base derivation in particular must never let
// corruption shrink the release history it derives from
// (REQ-resolve-pseudo-base).
func tagCommit(repo *git.Repository, h plumbing.Hash) (*object.Commit, bool, error) {
	peeled, typ, ok := peel(repo, h)
	if !ok {
		return nil, false, fmt.Errorf("peeling %s failed", h)
	}
	if typ != plumbing.AnyObject && typ != plumbing.CommitObject {
		return nil, false, nil
	}
	obj, err := repo.Object(plumbing.AnyObject, peeled)
	if err != nil {
		return nil, false, fmt.Errorf("tag target %s: %w", peeled, err)
	}
	c, isCommit := obj.(*object.Commit)
	return c, isCommit, nil
}

func (r *Repo) resolvePseudo(ctx context.Context, v version.Version, subtree string) (origin.Commit, error) {
	_, prefix, _ := v.Pseudo()
	matches, err := r.commitsWithPrefix(ctx, prefix)
	if err != nil {
		return origin.Commit{}, fmt.Errorf("resolving %s: %w", v, err)
	}
	c, err := uniqueCommit(v, matches)
	if err != nil {
		return origin.Commit{}, err
	}
	// Binding before base: the embedded time must match the commit in
	// hand (REQ-resolve-pseudo-commit) before its tag history means
	// anything.
	if err := origin.VerifyPseudo(v, commitIdentity(c)); err != nil {
		return origin.Commit{}, err
	}
	expected, err := r.expectedPseudo(ctx, c, subtree)
	if err != nil {
		return origin.Commit{}, fmt.Errorf("resolving %s: %w", v, err)
	}
	if expected.String() != v.String() {
		return origin.Commit{}, fmt.Errorf("%w: %s names commit %s, whose pseudo-version is %s",
			ErrBaseInconsistent, v, c.Hash, expected)
	}
	return commitIdentity(c), nil
}

// commitIndex is the memoized index over the commit graph the listing
// reaches: every commit reachable from the listing's heads and tags in
// the history, keyed by full hash, plus the sorted hash list prefix
// lookup ranges over. What the history holds beyond the listing's
// reach — commits earlier runs fetched that the origin since moved
// away from — is no part of it: presence is the listing's. Built on
// the first pseudo-version resolution, the history fetched for it; a
// driver resolving many versions against one Repo walks the graph
// once.
func (r *Repo) commitIndex(ctx context.Context) (map[string]*object.Commit, []string, error) {
	if r.indexed {
		return r.commits, r.sorted, nil
	}
	if err := r.ensureHistory(ctx); err != nil {
		return nil, nil, err
	}
	roots, err := r.roots(ctx)
	if err != nil {
		return nil, nil, err
	}
	commits := map[string]*object.Commit{}
	queue := roots
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		if _, seen := commits[h.String()]; seen {
			continue
		}
		c, err := r.hist.CommitObject(h)
		if err != nil {
			return nil, nil, fmt.Errorf("iterating commits: reading %s: %w", h, err)
		}
		commits[h.String()] = c
		queue = append(queue, c.ParentHashes...)
	}
	hashes := slices.Sorted(maps.Keys(commits))
	r.commits, r.sorted, r.indexed = commits, hashes, true
	return commits, hashes, nil
}

// roots are the commits the listing's heads and tags name, peeled in
// the history: the graph's entry points. The answer for each listed
// ref — the commit it peels to, or none — is recorded by the ref's
// hash for base derivation, so the question is asked once and
// answered once. A listed object the history lacks after its fetch
// is either a lightweight tag's non-commit target, which the object
// filter left out, or a ref the origin moved since the listing; the
// ref is probed by name, a non-commit kept in the history so the
// next run finds it, a commit the listing named that the origin no
// longer serves reported.
func (r *Repo) roots(ctx context.Context) ([]plumbing.Hash, error) {
	refs, err := r.Refs()
	if err != nil {
		return nil, err
	}
	var roots []plumbing.Hash
	targets := map[plumbing.Hash]plumbing.Hash{}
	for _, ref := range refs {
		if strings.HasSuffix(ref.Name, "^{}") {
			continue
		}
		h := plumbing.NewHash(ref.Hash)
		if _, err := r.hist.Object(plumbing.AnyObject, h); errors.Is(err, plumbing.ErrObjectNotFound) && r.remote != nil {
			if err := r.probeRef(ctx, ref.Name, h); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, fmt.Errorf("reading %s at %s: %w", ref.Name, h, err)
		}
		c, ok, err := tagCommit(r.hist, h)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", ref.Name, err)
		}
		if ok {
			roots = append(roots, c.Hash)
			targets[h] = c.Hash
		} else {
			targets[h] = plumbing.ZeroHash
		}
	}
	r.targets = targets
	return roots, nil
}

// probeRef fetches a listed ref the history lacks by its name, at
// depth one and unfiltered, into a repository thrown away after: the
// history is never fetched at a depth, so a moved ref's commit must
// not leave a shallow boundary in it. What arrives decides: the
// listing's object absent, the origin moved the ref; a non-commit,
// the ref names no root, and the object is copied into the history
// so the next run reads it there; a commit, one the history's fetch
// of every head and tag should have held — reported, never patched
// in.
func (r *Repo) probeRef(ctx context.Context, name string, h plumbing.Hash) error {
	probe, err := git.Init(memory.NewStorage())
	if err != nil {
		return err
	}
	remote, err := probe.CreateRemote(&config.RemoteConfig{Name: remoteName, URLs: r.remote.Config().URLs})
	if err != nil {
		return err
	}
	if err := r.fetchListed(ctx, remote, probe, name, h); err != nil {
		return err
	}
	if _, ok, err := tagCommit(probe, h); err != nil {
		return fmt.Errorf("reading %s: %w", name, err)
	} else if ok {
		return fmt.Errorf("fetching the history: %s at %s is a commit the origin left out of it", name, h)
	}
	obj, err := probe.Storer.EncodedObject(plumbing.AnyObject, h)
	if err != nil {
		return fmt.Errorf("reading %s: %w", name, err)
	}
	if _, err := r.hist.Storer.SetEncodedObject(obj); err != nil {
		return fmt.Errorf("keeping %s in the history: %w", name, err)
	}
	return nil
}

// commitsWithPrefix lists the commits whose hash carries the given
// prefix, in hash order over the memoized index; uniqueCommit owns the
// zero/one/many decision.
func (r *Repo) commitsWithPrefix(ctx context.Context, prefix string) ([]*object.Commit, error) {
	commits, hashes, err := r.commitIndex(ctx)
	if err != nil {
		return nil, err
	}
	start, _ := slices.BinarySearch(hashes, prefix)
	var matches []*object.Commit
	for _, h := range hashes[start:] {
		if !strings.HasPrefix(h, prefix) {
			break
		}
		matches = append(matches, commits[h])
	}
	return matches, nil
}

// uniqueCommit is the binding decision of REQ-resolve-pseudo-commit's
// origin lookup: exactly one commit may carry the embedded prefix —
// zero is an absent commit, two or more bind nothing uniquely. The
// ambiguity report sorts the hashes so storage iteration order never
// reaches a caller (REQ-resolve-determinism).
func uniqueCommit(v version.Version, matches []*object.Commit) (*object.Commit, error) {
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("%w: %s", ErrCommitAbsent, v)
	case 1:
		return matches[0], nil
	}
	hashes := make([]string, len(matches))
	for i, c := range matches {
		hashes[i] = c.Hash.String()
	}
	slices.Sort(hashes)
	return nil, fmt.Errorf("%w: %s: commits %s share the embedded prefix",
		ErrCommitAmbiguous, v, strings.Join(hashes, ", "))
}

// expectedPseudo reconstructs the pseudo-version the origin's state
// assigns to a commit for the module at subtree: the highest release
// tag in the module's namespace on an ancestor of the commit seeds the
// base, the zero base when none exists (REQ-resolve-pseudo-base).
// Ancestry is reachability — the commit itself included. A release
// tag's commit is what the graph's roots recorded for it, decided
// once for every listed ref. A pseudo-version-shaped tag never seeds
// a base: origin.ReleaseTags excludes it from the release list, and
// version.PseudoVersion refuses one as precedent regardless.
func (r *Repo) expectedPseudo(ctx context.Context, c *object.Commit, subtree string) (version.Version, error) {
	refs, err := r.Refs()
	if err != nil {
		return version.Version{}, err
	}
	if _, _, err := r.commitIndex(ctx); err != nil {
		return version.Version{}, err
	}
	ancestors, err := r.ancestors(ctx, c)
	if err != nil {
		return version.Version{}, err
	}
	tags := origin.ReleaseTags(refs, subtree)
	var precedent *version.Version
	for i := len(tags) - 1; i >= 0; i-- {
		t := tags[i]
		tc, known := r.targets[plumbing.NewHash(t.Hash)]
		if !known {
			return version.Version{}, fmt.Errorf("release tag %s: %s is not among the listing's refs", t.Version, t.Hash)
		}
		if tc.IsZero() {
			// A tag naming a non-commit object tags nothing on any
			// ancestor; it is no release of the module.
			continue
		}
		if ancestors[tc.String()] {
			precedent = &t.Version
			break
		}
	}
	return version.PseudoVersion(precedent, c.Committer.When, c.Hash.String())
}

// ancestors is the commit's reachability set in the history, itself
// included, memoized per resolved commit: repeated resolution of the
// same pseudo-version (and the same commit across subtree namespaces)
// walks history once.
func (r *Repo) ancestors(ctx context.Context, c *object.Commit) (map[string]bool, error) {
	if seen, ok := r.ancestry[c.Hash.String()]; ok {
		return seen, nil
	}
	if err := r.ensureHistory(ctx); err != nil {
		return nil, err
	}
	seen := map[string]bool{c.Hash.String(): true}
	queue := []*object.Commit{c}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, ph := range cur.ParentHashes {
			if seen[ph.String()] {
				continue
			}
			seen[ph.String()] = true
			parent, err := r.hist.CommitObject(ph)
			if err != nil {
				return nil, fmt.Errorf("reading ancestor %s of %s: %w", ph, c.Hash, err)
			}
			queue = append(queue, parent)
		}
	}
	if r.ancestry == nil {
		r.ancestry = map[string]map[string]bool{}
	}
	r.ancestry[c.Hash.String()] = seen
	return seen, nil
}
