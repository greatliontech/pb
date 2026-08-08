package direct

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/greatliontech/pb/internal/origin"
	"github.com/greatliontech/pb/internal/version"
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
// resolves through its tag (REQ-resolve-release-tags); a pseudo-version
// resolves only to the commit whose hash and time it embeds, present at
// the origin and with a base consistent with the module's release-tag
// history (REQ-resolve-pseudo-commit, REQ-resolve-pseudo-base).
func (r *Repo) ResolveVersion(v version.Version, subtree string) (origin.Commit, error) {
	if v.IsPseudo() {
		return r.resolvePseudo(v, subtree)
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

// tagRef looks up the module's tag ref for a release version, mapping
// absence to ErrUnknownVersion; resolveTag and VerificationPack share
// the one lookup.
func (r *Repo) tagRef(v version.Version, subtree string) (*plumbing.Reference, error) {
	ref, err := r.r.Reference(plumbing.ReferenceName(tagRefName(v, subtree)), true)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil, fmt.Errorf("%w: %s (tag %s)", ErrUnknownVersion, v, tagRefName(v, subtree))
		}
		return nil, fmt.Errorf("resolving %s: %w", v, err)
	}
	return ref, nil
}

func (r *Repo) resolveTag(v version.Version, subtree string) (origin.Commit, error) {
	ref, err := r.tagRef(v, subtree)
	if err != nil {
		return origin.Commit{}, err
	}
	c, ok, err := r.tagCommit(ref.Hash())
	if err != nil {
		return origin.Commit{}, fmt.Errorf("resolving %s: tag %s: %w", v, tagRefName(v, subtree), err)
	}
	if !ok {
		return origin.Commit{}, fmt.Errorf("resolving %s: tag %s does not name a commit", v, tagRefName(v, subtree))
	}
	return commitIdentity(c), nil
}

// tagCommit peels a tag ref's target and reads the commit it names.
// ok=false reports the one in-spec non-release shape — a tag ultimately
// naming a non-commit object. Everything else (an undecodable tag
// object, a peeled target absent from storage) is corruption and
// errors: both callers must fail loudly on it, base derivation in
// particular must never let corruption shrink the release history it
// derives from (REQ-resolve-pseudo-base).
func (r *Repo) tagCommit(h plumbing.Hash) (*object.Commit, bool, error) {
	peeled, ok := r.peel(h)
	if !ok {
		return nil, false, fmt.Errorf("peeling %s failed", h)
	}
	obj, err := r.r.Object(plumbing.AnyObject, peeled)
	if err != nil {
		return nil, false, fmt.Errorf("tag target %s: %w", peeled, err)
	}
	c, isCommit := obj.(*object.Commit)
	return c, isCommit, nil
}

func (r *Repo) resolvePseudo(v version.Version, subtree string) (origin.Commit, error) {
	_, prefix, _ := v.Pseudo()
	matches, err := r.commitsWithPrefix(prefix)
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
	expected, err := r.expectedPseudo(c, subtree)
	if err != nil {
		return origin.Commit{}, fmt.Errorf("resolving %s: %w", v, err)
	}
	if expected.String() != v.String() {
		return origin.Commit{}, fmt.Errorf("%w: %s names commit %s, whose pseudo-version is %s",
			ErrBaseInconsistent, v, c.Hash, expected)
	}
	return commitIdentity(c), nil
}

// commitsWithPrefix lists the commits whose hash carries the given
// prefix, in storage iteration order; uniqueCommit owns making the
// outcome order-independent.
func (r *Repo) commitsWithPrefix(prefix string) ([]*object.Commit, error) {
	iter, err := r.r.CommitObjects()
	if err != nil {
		return nil, fmt.Errorf("iterating commits: %w", err)
	}
	var matches []*object.Commit
	err = iter.ForEach(func(c *object.Commit) error {
		if strings.HasPrefix(c.Hash.String(), prefix) {
			matches = append(matches, c)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("iterating commits: %w", err)
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
// Ancestry is reachability — the commit itself included. A
// pseudo-version-shaped tag never seeds a base: origin.ReleaseTags
// excludes it from the release list, and version.PseudoVersion refuses
// one as precedent regardless.
func (r *Repo) expectedPseudo(c *object.Commit, subtree string) (version.Version, error) {
	refs, err := r.Refs()
	if err != nil {
		return version.Version{}, err
	}
	ancestors, err := r.ancestors(c)
	if err != nil {
		return version.Version{}, err
	}
	tags := origin.ReleaseTags(refs, subtree)
	var precedent *version.Version
	for i := len(tags) - 1; i >= 0; i-- {
		t := tags[i]
		tc, ok, err := r.tagCommit(plumbing.NewHash(t.Hash))
		if err != nil {
			return version.Version{}, fmt.Errorf("release tag %s: %w", t.Version, err)
		}
		if !ok {
			// A tag naming a non-commit object tags nothing on any
			// ancestor; it is no release of the module.
			continue
		}
		if ancestors[tc.Hash.String()] {
			precedent = &t.Version
			break
		}
	}
	return version.PseudoVersion(precedent, c.Committer.When, c.Hash.String())
}

// ancestors is the commit's reachability set, itself included.
func (r *Repo) ancestors(c *object.Commit) (map[string]bool, error) {
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
			parent, err := r.r.CommitObject(ph)
			if err != nil {
				return nil, fmt.Errorf("reading ancestor %s of %s: %w", ph, c.Hash, err)
			}
			queue = append(queue, parent)
		}
	}
	return seen, nil
}
