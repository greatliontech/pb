# Dependency management

Go-module-style: git origin, dumb proxy, content-hash integrity, sigstore
provenance. The pieces Go's model gets right must be deliberately replicated,
not assumed for free.

## Settled direction

- **Origin is git.** Module identity is a path — a host followed by at least
  one path segment, possibly extending into a repo subtree — resolved at the
  source. No registry accounts, no push.
- **Proxy is a dumb cache.** Stateless, anyone can run one. Design the
  protocol so proxies are unable to tamper: signatures and attestations
  travel with the archive and verify against origin identity.
- **Canonical archive format is a first-class contract.** The complaint about
  buf's "hashing obscurities" is really that their digest scheme is an
  undocumented moving target. The fix: a boring, fully documented archive —
  sorted paths, normalized metadata, plain SHA-256, explicitly versioned.
  This is a persisted/wire encoding and belongs in the spec before any
  fetching code exists. Likely the first spec this repo gets.
- **Versioning: semver tags + pseudo-versions + MVS + lockfile.** Buf's dep
  model is flat and registry-resolved; real version resolution is an
  improvement, not a relocation. Pseudo-versions (commit-based) matter more
  here than in Go because the proto repos everyone depends on (googleapis,
  grpc-gateway, protovalidate) don't cut semver tags for their protos.
- **Provenance via sigstore.** skillset (`../skillset`) already carries the
  verification machinery (cosign, fulcio, rekor, trusted-root handling) —
  reuse, don't rebuild.

## Open questions

- **Third-party module boundary (the overlay problem) — hardest open
  question, settle early because it shapes everything downstream.** Go
  requires `go.mod` in the origin repo. Requiring a `pb.yaml` upstream means
  no googleapis until Google adds one — i.e., never. Likely need a way to
  declare a module *over* an uncooperative repo subtree (an overlay/shim
  definition). This is philosophically impure — module identity no longer
  lives solely at the origin — and it interacts badly with provenance: who
  signs an overlay?
- **Identity policy for provenance.** What Fulcio identity is *expected* to
  have signed module X, and where is that expectation pinned — config,
  trust-on-first-use, or a checksum-DB/transparency-log analog?
- **Module unit definition.** Is a module a repo, or a subtree of one (Go
  allows subdir modules tagged `subdir/vX.Y.Z`)? Interacts with the overlay
  question.
- **Proxy protocol shape.** Adopt Go's GOPROXY protocol semantics verbatim
  (`$base/$module/@v/list`, …) so existing infra patterns transfer, or define
  a same-shaped variant? Also needs GOPRIVATE/GONOSUMDB analogs.
- **Bridge to existing BSR deps.** Adoption hinges on consuming the modules
  people already depend on. The big ones live on GitHub as protos anyway, so
  git origin genuinely works — but a migration story for BSR-only deps
  (mirror via pbr?) needs an answer.
- **Cache layout.** Module zips + compiled descriptors keyed by content hash;
  documented and boring, in explicit contrast to buf's cache churn. Where the
  layout is a persisted contract vs. an implementation detail needs deciding.
