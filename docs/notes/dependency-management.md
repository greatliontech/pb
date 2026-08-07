# Dependency management

Go-module-style: git origin, dumb proxy, content-hash integrity, sigstore
provenance. The former open questions are settled (decisions accepted
2026-08); what follows is the settled design. Ripe for promotion into the
first real specs — the canonical archive format, the lockfile, and the proxy
protocol are the persisted/wire contracts.

## Module identity and resolution

A module path is `host/one/or/more/segments`, arbitrary depth. Resolution,
in order:

1. A path containing a `.git` segment resolves directly to that git remote;
   segments after it are a subtree within the repo.
2. Otherwise, HTTPS meta-tag discovery (Go's `?go-get=1` pattern, own tag):
   request `https://host/path?pb-get=1`, read
   `<meta name="pb-import" content="<prefix> git <repo-url>">`. Vanity paths
   and self-hosted git with no hardcoded hosting-provider list.
3. Fallback: progressively probe prefixes with `git ls-remote` until one
   answers — `github.com/org/repo/sub/tree` works with zero configuration.

## Module unit

A module is a directory: the root is where the module file sits (name open:
`pb.yaml` vs `proto.mod`), and the proto include root is that directory. One
repo can host many modules. Repo-root modules tag `vX.Y.Z`; subtree modules
use Go's prefixed-tag convention `sub/tree/vX.Y.Z`.

## Third-party modules: synthesis, not overlays

Go's own answer to pre-module repos, adopted wholesale. A repo — or any
path-addressable subtree of it — without a module file is consumable as a
**synthesized module**: include root = the subtree root, file set = every
`.proto` under it, dependencies = undeclared. Undeclared deps mean the
consumer's resolution must satisfy the synthesized module's imports; pb
reports exactly which imports are unsatisfied and from which module.

- `github.com/googleapis/googleapis`: include root = repo root;
  `google/api/annotations.proto` just works.
- `github.com/bufbuild/protovalidate/proto/protovalidate`: subtree as module
  path; `buf/validate/validate.proto` works.
- Versioning: repo-level tags if present, else pseudo-versions.

No overlay artifact exists — nothing to author, share, or sign. Identity
stays origin-derived, integrity is the canonical archive hash of the
subtree, provenance is whatever the origin genuinely offers.

## Provenance identity policy

- **Default, zero config — origin consistency:** a signature verifies iff
  its Fulcio identity matches the module's own origin (e.g. cert SAN is a
  workflow of the same repo for GitHub Actions OIDC, or a gitsign-signed tag
  by a repo maintainer).
- **Per-path-prefix overrides** in consumer config: pin an exact issuer/SAN
  pattern for a module prefix.
- **Absence:** unsigned modules are consumable by default; the lockfile
  records `provenance: none` per module; strict mode (global or per-prefix
  `require-provenance`) refuses them.
- **No custom checksum-DB service.** The lockfile pins version→digest on
  first resolution (go.sum semantics); Rekor is the transparency log —
  sigstore bundles carry inclusion proofs. Accepted gap, on record: unsigned
  modules have no first-fetch protection; a sumdb-analog can be layered on
  later without touching the format.

## Proxy protocol

The GOPROXY protocol shape as-is, renamed: `$base/<module>/@v/list`,
`@v/<version>.info`, `@v/<version>.zip` (the canonical archive), `@latest`,
plus `@v/<version>.bundle` serving the sigstore bundle as a sidecar (404
when none exists). `PBPROXY` (comma-separated, `direct` fallback, Go
semantics), `PBNOPROXY`/`PBPRIVATE` analogs. pbr implements this protocol
as its proxy role.

## Version resolution

MVS exactly as Go, with a lockfile, **without** semantic-import-versioning
(`/vN` path suffixes). Protos carry their versioning idiom inside the schema
— package-level version suffixes (`google.api.v1`) — so breaking changes
idiomatically create new proto packages, not new module paths; `/vN` would
version the same thing twice. Cost accepted: MVS's "same path ⇒ compatible"
assumption weakens, so pb warns loudly when resolution crosses a major.
Side-by-side majors keyed by (path, major) can be added later if real-world
pain shows; the reverse retreat (from `/vN`) would be harder.

Semver tags + pseudo-versions (commit-based). Pseudo-versions matter more
here than in Go: the proto repos everyone depends on don't cut semver tags
for their protos.

## Canonical archive format

First-class contract, likely the first spec this repo gets: sorted paths,
normalized metadata, plain SHA-256, explicitly versioned. A boring,
fully documented archive — the direct answer to buf's undocumented,
moving-target digest scheme. Signatures and attestations travel with the
archive and verify against origin identity, so proxies cannot tamper.

## BSR bridge

- **Mapping table in `pb migrate`:** rewrites well-known BSR deps to their
  GitHub sources (`buf.build/googleapis/googleapis` →
  `github.com/googleapis/googleapis`).
- **Protocol bridge in pbr** for genuinely BSR-only deps: pbr serves the pb
  proxy protocol backed by a BSR remote — fetches the buf module, repacks
  deterministically into the canonical archive, stable digest,
  `provenance: none`. pb itself never learns anything about BSR; the bridge
  is pbr's job and dies when no longer needed.

## Cache

Explicitly **not** contract — the persisted contracts are the canonical
archive format and the lockfile only. Content-addressed under
`${XDG_CACHE_HOME}/pb`: module archives keyed by digest; derived artifacts
(compiled descriptor sets) keyed by (module digest, compiler version,
options). Documented properties: disposable at any time, no cross-version
compatibility promise, correctness never depends on layout — deleting the
cache is always safe; resolution re-derives everything from origin +
lockfile.

## Remaining open

- Module file name: `pb.yaml` vs `proto.mod` (and its minimal schema).
- Sigstore machinery reuse from `../skillset` (cosign/fulcio/rekor,
  trusted-root handling): reuse, don't rebuild — extraction shape TBD.
