# Test support

The fixture packages every suite builds on, one per domain, each named
`<domain>test` after the Go convention for a test-only package
(`net/http/httptest`), so a reader knows from the import that nothing
here ships, which the layering test at `internal/` enforces:

- `gittest` — bare git repositories in memory at the plumbing level:
  blobs, trees, commits, tags in both object formats, refs, corrupt
  objects, and the client options that serve one in-process.
- `rapidtest` — the property-oracle discipline: rapid's seed pinned and
  its failure files off, so a property test is deterministic and doubles
  as a mutation-testing oracle; exploration is CI's varying-seed legs.
- `fetchtest` — the fetch client's fixtures: an origin repository
  over the git transport and a socket-free proxy serving an endpoint
  map, with `assemble` closing them over a client one package removed,
  since the client's own suite imports the fixture.
- `provtest` — gitsign-shaped signed tags over gitprov's synthetic
  sigstore, the evidence the provenance and fetch suites verify.
- `imagetest` — a registry double answering the OCI referrers API
  as the specification describes it, and the pushes cosign's
  conventions make.

A fixture lives here when more than one package builds on it or when
its shapes are a domain's, not a package's. It stays a package of its
own: merging domains would pull every fixture's dependencies into every
suite that needs one. Nothing here reads the packages it fixtures
beyond what a consumer would; a fixture that needs a package's own
symbols is that package's test file, not a fixture.
