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
	// TrustedRoot pins the sigstore material evidence verifies against.
	// With a nil root no evidence can verify: absent under
	// allow-unsigned, a failure under require-provenance.
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

	origins map[string]origin.Origin
	repos   map[string]*direct.Repo
}

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

// directArtifact constructs one artifact from the origin repository —
// the direct source's half of REQ-proxy-direct-equivalence, over the
// internal/source/direct construction layer.
func (c *Client) directArtifact(ctx context.Context, modPath string, v version.Version, kind string) (proxy.Unverified, error) {
	o, err := c.origin(ctx, modPath)
	if err != nil {
		return nil, err
	}
	repo, err := c.repo(ctx, o.Repo)
	if err != nil {
		return nil, err
	}
	// The version resolves in the namespace the listing names — a
	// release through its tag there, a pseudo-version's base over the
	// release tags there (REQ-resolve-pseudo-base).
	at, err := c.atHead(ctx, repo, o)
	if err != nil {
		return nil, err
	}
	commit, err := repo.ResolveVersion(ctx, v, at.namespace)
	if err == nil && v.IsPseudo() {
		// A pseudo-version names a commit on the consumer's own say,
		// no claim of the origin's: where the module root is absent at
		// that commit the origin has no artifact of any kind for it —
		// not-here for info and archive alike, so the two never
		// disagree — whereas a release tag over a commit lacking the
		// root is the origin's claim and aborts below as any failing
		// walk does.
		err = repo.Rooted(ctx, commit.Hash, o.Subtree)
		if errors.Is(err, direct.ErrNoModuleRoot) {
			return nil, fmt.Errorf("%w: origin %s: %v", proxy.ErrNotHere, o.Repo, err)
		}
	}
	switch {
	case errors.Is(err, direct.ErrUnknownVersion), errors.Is(err, direct.ErrCommitAbsent):
		// The origin does not have this version — the direct analog of a
		// proxy 404 (REQ-proxy-not-found): a tag can be legitimately
		// unknown here while a proxy in the list archived it. Ambiguity
		// and base inconsistency are different: those are integrity
		// failures resolution must fail on rather than route around
		// (REQ-resolve-pseudo-commit, REQ-resolve-pseudo-base), so they
		// abort below like any non-not-here failure.
		return nil, fmt.Errorf("%w: origin %s: %v", proxy.ErrNotHere, o.Repo, err)
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
			return nil, fmt.Errorf("%w: origin %s has no module file for %s@%s", proxy.ErrNotHere, o.Repo, modPath, v)
		}
		return b, nil
	case KindZip:
		var buf bytes.Buffer
		if _, err := repo.Archive(ctx, &buf, commit.Hash, o.Subtree); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case KindProv:
		b, ok, err := repo.VerificationPack(ctx, v, at.namespace)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: origin %s has no provenance evidence for %s@%s", proxy.ErrNotHere, o.Repo, modPath, v)
		}
		return b, nil
	}
	return nil, fmt.Errorf("fetch: unknown artifact kind %q", kind)
}
