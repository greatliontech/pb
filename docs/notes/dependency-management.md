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

## Provenance: signed tags are the artifact (bundle-at-origin, settled)

The provenance artifact is the **gitsign-signed annotated tag** — skillset's
architecture generalized. There is no separate signing artifact and no
storage problem: provenance travels with every clone/mirror because it *is*
a tag, and the author-side act is "sign your tags" (gitsign, ambient OIDC in
CI) — no pb-specific ceremony.

**The trust shape is the reason** (decided over the digest-bundle
alternative): the identity signs the *source of truth* (tag→commit→tree) and
the verifier **recomputes the derivation** — tree hash from archive
contents, matched against the signed commit — so the archive builder is
outside the trusted base entirely; a poisoned archive fails verification
arithmetic. A bundle signed over the archive digest would instead move
derivation *inside* the trust boundary: a compromised release step gets a
poisoned archive validly signed. Direct-digest signing was therefore
rejected, not deferred for cost.

- Verification is fully offline: embedded CMS/Rekor proof
  (`rekorMode=offline`) against a pinned TUF trusted root, never querying
  Rekor — skillset's proven path, extracted as a shared library.
- The proxy serves a **verification pack** per version: tag object + commit
  object + (for subtree modules) the root-to-subtree tree-object path, with
  the embedded CMS/Rekor material. Endpoint `@v/<version>.prov`, 404 when
  none; its media type is an **envelope** from day one — an empty socket so
  statement-shaped evidence could slot in later without a protocol change.
  No ref namespace, no second signing flow, no second verification path
  now.
- SHA-256 object-format repos: tree recomputation extends to them; the
  shared-library extraction is the moment to lift skillset's SHA-1-only
  fail-closed scope. Until then the binding rides git's hardened SHA-1 —
  stated in the spec; lockfile SHA-256 digests guard integrity
  independently either way.
- Richer predicates (SLSA/in-toto) are deliberately out for modules: a
  module archive isn't built, it's derived from source — its provenance
  question *is* source authenticity. Build provenance belongs to plugin
  images, where cosign/OCI owns it.

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
  signed tags embed the inclusion proofs. Accepted gap, on record: unsigned
  modules have no first-fetch protection; a sumdb-analog can be layered on
  later without touching the format.

## Proxy protocol

Specced — `docs/specs/module-proxy.md` is authoritative (endpoints
including `.mod` and `.prov`, escaping, fall-through, client verification,
`PBPROXY`/`PBNOPROXY`; a private proxy or origin through the credential
file and SSH, `PBNETRC`/`PBSSH`, per `module-resolution.md`). pbr implements the protocol as its proxy role.

**No default proxy — default is `direct`** (decided after weighing the
reverse). pbr.dev is the *recommended, one-line opt-in* public proxy, not a
default. Rationale on record: without the (deferred) transparency-log
analog, an unsigned module's first fetch has no cross-check, so a default
proxy would add a trusted party to every user's first-fetch path — direct
trusts only the origin host, whom the user already trusts for the code
itself. Also avoided: availability coupling (fall-through is 404/410-only,
so a proxy outage aborts default-config builds), fetch-pattern privacy, and
central-default optics. Permanence and speed are earned via opt-in
adoption (docs, CI templates), not a default. **Revisit trigger, written
down:** defaulting to pbr.dev becomes eligible when the first-fetch gap
closes — a transparency log (which pbr.dev itself could serve) or broad
signed-tag coverage.

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

Specced — `docs/specs/module-archive.md` is authoritative (manifest-based
digest, wire ZIP, git-tree binding, file-set rules). The lockfile is
specced in `docs/specs/module-lockfile.md`.

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

None — the dependency side is fully specced: `module-archive.md`,
`module-lockfile.md`, `module-proxy.md`, `module-file.md`,
`module-resolution.md`, `provenance.md`, `workspace.md`.
