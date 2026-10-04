// Package fetch implements the client side of module artifact
// acquisition: fetching through the configured source list
// (module-proxy.md §Client behavior), verifying every byte against
// digests, pins, and provenance before use
// (REQ-proxy-client-verification), recording lockfile pins
// (module-lockfile.md §Semantics), and maintaining the module cache
// (dep-verbs.md §Module cache).
//
// The trust posture is uniform across sources: proxy bytes, origin
// bytes, and cache bytes are all unverified inputs to the same
// verification pipeline, so no source — the cache included — can
// influence what is accepted, only what is fetched. First use of an
// unpinned (module path, version) runs the whole pipeline over freshly
// fetched bytes and records the complete pin: digest, module-file hash,
// and evaluated provenance in one step (REQ-lock-first-use), so a pin
// never exists in a partially evaluated state.
package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/pb/internal/module/archive"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/pb/internal/source/direct"
	"github.com/greatliontech/pb/internal/source/origin"
	"github.com/greatliontech/pb/internal/source/proxy"
)

// fetchLimit bounds every artifact response (REQ-proxy-client-
// verification's resource clause): no artifact of a valid module can
// exceed the archive size limit, so reading past it could only trust a
// source with unbounded memory. The container overhead of a maximal
// file set fits: the limit applies per response, and each artifact —
// zip included, whose members compress — is bounded by the file-set
// limit up to framing that the slack in practice never approaches.
const fetchLimit = archive.MaxTotalSize

// Client fetches, verifies, pins, and caches module artifacts. All
// fields except HTTP, Policy, and TrustedRoot are required. Not safe
// for concurrent use: the pin store and memoization maps are unguarded,
// matching the single-threaded resolution driver.
type Client struct {
	// HTTP performs proxy and vanity fetches; nil uses
	// http.DefaultClient. Every request goes through source.HTTPClient,
	// the proxy fetches through proxy.Get and the vanity lookups
	// through origin's discovery.
	HTTP *http.Client
	// Sources is the parsed source-list configuration (REQ-proxy-config).
	Sources proxy.Config
	// Cache is the module cache; required.
	Cache *Cache
	// Lock is the resolution root's pin store; required, mutated in
	// place as first-use pins are recorded.
	Lock *lockfile.File
	// Policy is the trust policy; nil means the zero policy
	// (allow-unsigned, no identity rules).
	Policy *trust.Policy
	// TrustedRoot pins the sigstore material evidence verifies against:
	// an input to every judgement of sigstore evidence, so sigstore
	// evidence served with a nil root fails the operation under either
	// posture, while a source serving none leaves the subject unsigned
	// and pinned-key evidence needs no root (provenance.md, the trusted
	// root term).
	TrustedRoot *gitprov.TrustedRoot
	// ResolveOrigin maps a module path to its origin; required. Called
	// lazily — the direct source always needs it, proxy fetches only
	// when provenance evidence must be tied to the origin
	// (REQ-prov-origin-consistency) — and memoized per path. The
	// resolver must not reenter this Client: a reentrant call for the
	// same pair would race the pin-absence guard the first-use pipeline
	// relies on.
	ResolveOrigin func(ctx context.Context, modPath string) (origin.Origin, error)
	// Fetcher fetches origin repositories for the direct source. The
	// zero value is ready; tests inject a file transport.
	Fetcher direct.Fetcher
	// ReadOnly refuses first use: a pair the pin store does not pin is
	// answered with an *UnpinnedError instead of being fetched and
	// pinned, so a reader that may not write the lockfile — the
	// language server (lsp.md REQ-lsp-session) — resolves at the pins
	// alone and never records one.
	ReadOnly bool

	origins map[string]origin.Origin
	repos   map[string]*direct.Repo
	// heldPairs is the pinned pairs whose evidence record this client
	// has held to the trusted root in its life, by pair and digest:
	// the record is held wherever the archive is read, once per
	// client (provenance.md REQ-prov-pin-held).
	heldPairs map[string]struct{}
}

// ErrPinUnderPolicy is a pin the trust policy of the day no longer
// accepts as recorded — unsigned where it requires provenance, signed
// by an identity its rule no longer names — which only an explicit
// update re-resolves (provenance.md REQ-prov-pin-held).
var ErrPinUnderPolicy = errors.New("the pin is not accepted by the trust policy of the day")

// origin resolves and memoizes a module path's origin. Failures are not
// memoized: origin resolution is a network operation whose transient
// failures must not poison the rest of the run.
func (c *Client) origin(ctx context.Context, modPath string) (origin.Origin, error) {
	if o, ok := c.origins[modPath]; ok {
		return o, nil
	}
	if c.ResolveOrigin == nil {
		return origin.Origin{}, fmt.Errorf("fetch: no origin resolver configured (needed for %s)", modPath)
	}
	o, err := c.ResolveOrigin(ctx, modPath)
	if err != nil {
		return origin.Origin{}, err
	}
	if c.origins == nil {
		c.origins = map[string]origin.Origin{}
	}
	c.origins[modPath] = o
	return o, nil
}

// Release gives back what the client holds of every origin — the
// repositories opened, their listings, the origins resolved — so
// another process's opening of an origin proceeds and the next
// resolution lists and resolves anew: a reader that outlives a verb's
// run, the language server, releases at each judgement's end
// (lsp.md REQ-lsp-session, module-proxy.md REQ-proxy-direct-fetch);
// a verb never needs to, its scope the process.
func (c *Client) Release() {
	for url, r := range c.repos {
		r.Close()
		delete(c.repos, url)
	}
	clear(c.origins)
}

// repo opens and memoizes an origin repository: one listing serves
// every version resolved against it, the objects fetched as each
// decision needs them.
func (c *Client) repo(ctx context.Context, repoURL string) (*direct.Repo, error) {
	if r, ok := c.repos[repoURL]; ok {
		return r, nil
	}
	r, err := c.Fetcher.Fetch(ctx, repoURL)
	if err != nil {
		return nil, err
	}
	if c.repos == nil {
		c.repos = map[string]*direct.Repo{}
	}
	c.repos[repoURL] = r
	return r, nil
}

// fetch acquires one artifact through the source list
// (REQ-proxy-fallthrough): proxies by endpoint URL, the origin through
// the direct construction. The bytes are Unverified; not-here from
// every source surfaces as a proxy.ErrNotHere-wrapped error for the
// caller to classify (a module file or provenance envelope legitimately
// has none).
func (c *Client) fetch(ctx context.Context, modPath string, v version.Version, kind string) (proxy.Unverified, error) {
	sources := c.Sources.SourcesFor(modPath)
	return proxy.Fallthrough(sources, func(s proxy.Source) (proxy.Unverified, error) {
		if s.Direct {
			return c.directArtifact(ctx, modPath, v, kind)
		}
		var url string
		switch kind {
		case KindInfo:
			url = proxy.InfoURL(s.URL, modPath, v)
		case KindMod:
			url = proxy.ModURL(s.URL, modPath, v)
		case KindZip:
			url = proxy.ZipURL(s.URL, modPath, v)
		case KindProv:
			url = proxy.ProvURL(s.URL, modPath, v)
		default:
			return nil, fmt.Errorf("fetch: unknown artifact kind %q", kind)
		}
		return proxy.Get(ctx, c.HTTP, url, fetchLimit)
	})
}

// ErrAmbiguousRelease is wrapped when a release version of a subtree
// module is named by two tags at once — the subtree's own over a
// commit where the subtree is declared, and the repository's over one
// where it is synthesized — so the version binds no commit uniquely
// (REQ-resolve-release-tags): an integrity failure resolution fails
// on rather than picks through.
var ErrAmbiguousRelease = errors.New("release version named by the subtree's tag and the repository's alike")

// resolveAt binds a version of the module to its commit by the
// subtree's state at that commit, the one commit a tag or a
// pseudo-version speaks of (REQ-resolve-release-tags,
// REQ-resolve-pseudo-base), and yields the namespace the version was
// judged in, the verification pack's. A root module has one namespace.
// A release of a subtree module is the subtree's tag where a module
// file lies at the subtree at the tagged commit, the repository's tag
// where the subtree lies there holding none; both is ambiguous; the
// subtree's tag over a commit lacking the subtree is the origin's
// claim of a module the commit has not, and aborts. A pseudo-version
// binds its commit first, the namespace is judged there — a subtree
// absent at that commit names nothing of the module, not-here for
// every artifact alike — and the base is then checked in it.
func (c *Client) resolveAt(ctx context.Context, repo *direct.Repo, o origin.Origin, v version.Version) (origin.Commit, string, error) {
	if o.Subtree == "" {
		commit, err := repo.ResolveVersion(ctx, v, "")
		return commit, "", err
	}
	if v.IsPseudo() {
		bound, err := repo.PseudoCommit(ctx, v)
		if err != nil {
			return origin.Commit{}, "", err
		}
		at, err := c.namespaceAt(ctx, repo, o, bound.Hash.String())
		if err != nil {
			return origin.Commit{}, "", err
		}
		if !at.rooted {
			return origin.Commit{}, "", fmt.Errorf("%w: origin %s: %s names commit %s, where %s is absent", proxy.ErrNotHere, o, v, bound.Hash, o.Subtree)
		}
		commit, err := repo.ResolveVersion(ctx, v, at.namespace)
		return commit, at.namespace, err
	}
	var found []struct {
		commit    origin.Commit
		namespace string
	}
	for _, namespace := range []string{o.Subtree, ""} {
		commit, err := repo.ResolveVersion(ctx, v, namespace)
		if errors.Is(err, direct.ErrUnknownVersion) {
			continue
		}
		if err != nil {
			return origin.Commit{}, "", err
		}
		declared, err := repo.Declared(ctx, commit.Hash, o.Subtree)
		switch {
		case errors.Is(err, direct.ErrNoModuleRoot) && namespace == "":
			continue // the repository's release, of a commit holding no such subtree
		case err != nil:
			return origin.Commit{}, "", err
		}
		if declared == (namespace == o.Subtree) {
			found = append(found, struct {
				commit    origin.Commit
				namespace string
			}{commit, namespace})
		}
	}
	switch len(found) {
	case 0:
		return origin.Commit{}, "", fmt.Errorf("%w: %s (no tag of %s names it at a commit it is at)", direct.ErrUnknownVersion, v, o.Subtree)
	case 1:
		return found[0].commit, found[0].namespace, nil
	}
	return origin.Commit{}, "", fmt.Errorf("%w: %s at %s and %s", ErrAmbiguousRelease, v, found[0].commit.Hash, found[1].commit.Hash)
}

// directArtifact constructs one artifact from the origin repository —
// the direct source's half of REQ-proxy-direct-equivalence, over the
// internal/source/direct construction layer.
func (c *Client) directArtifact(ctx context.Context, modPath string, v version.Version, kind string) (proxy.Unverified, error) {
	o, err := c.origin(ctx, modPath)
	if err != nil {
		return nil, err
	}
	remote, err := o.Remote()
	if err != nil {
		return nil, err
	}
	repo, err := c.repo(ctx, remote)
	if err != nil {
		return nil, err
	}
	commit, namespace, err := c.resolveAt(ctx, repo, o, v)
	switch {
	case errors.Is(err, direct.ErrUnknownVersion), errors.Is(err, direct.ErrCommitAbsent):
		// The origin does not have this version — the direct analog of a
		// proxy 404 (REQ-proxy-not-found): a tag can be legitimately
		// unknown here while a proxy in the list archived it. Ambiguity
		// and base inconsistency are different: those are integrity
		// failures resolution must fail on rather than route around
		// (REQ-resolve-pseudo-commit, REQ-resolve-pseudo-base), so they
		// abort below like any non-not-here failure.
		return nil, fmt.Errorf("%w: origin %s: %v", proxy.ErrNotHere, o, err)
	case err != nil:
		return nil, err
	}
	switch kind {
	case KindInfo:
		return direct.InfoJSON(v, commit)
	case KindMod:
		b, ok, err := repo.ModuleFileBytes(ctx, commit.Hash, o.Subtree)
		if err != nil {
			return nil, err
		}
		if !ok {
			// A synthesized module has no standalone module file
			// (REQ-proxy-not-found: .mod answers not-here).
			return nil, fmt.Errorf("%w: origin %s has no module file for %s@%s", proxy.ErrNotHere, o, modPath, v)
		}
		return b, nil
	case KindZip:
		var buf bytes.Buffer
		if _, err := repo.Archive(ctx, &buf, commit.Hash, o.Subtree); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case KindProv:
		b, ok, err := repo.VerificationPack(ctx, v, namespace, o.Subtree)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: origin %s has no provenance evidence for %s@%s", proxy.ErrNotHere, o, modPath, v)
		}
		return b, nil
	}
	return nil, fmt.Errorf("fetch: unknown artifact kind %q", kind)
}
