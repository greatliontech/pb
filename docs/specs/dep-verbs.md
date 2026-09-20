# pb — dependency operations

The dep verbs are the user-facing surface of module resolution: they
compose path resolution and version selection (`module-resolution.md`),
source fetching (`module-proxy.md`), content verification
(`module-archive.md`, `provenance.md`), and the pin store
(`module-lockfile.md`) over a resolution root (`workspace.md`). This
document defines the verbs and the module cache; every fetching,
verification, and pinning obligation it composes is defined in those
documents and holds here unchanged — in particular, no verb accepts any
artifact on transport trust (REQ-proxy-client-verification) and no verb
rewrites a pin outside the explicit-update sanction
(REQ-lock-no-silent-downgrade).

**dep verb** (term): One of the operations `init`, `tidy`, `download`,
`update`, `graph`, `why`, `verify`, invoked as `pb dep <verb>`. Every
verb runs against the resolution root governing the working directory,
located per `workspace.md` (the nearest workspace above the directory,
else the nearest module root), and fails with no governing root or when
`REQ-work-membership` rejects the directory's module.

**module cache** (term): The persistent directory of fetched, verified
module artifacts: the `cache` setting — `$PBCACHE`, or the user
configuration file's `cache` key (`user-config.md`) — when set,
otherwise `pb/mod` under the platform user cache directory. The cache
is shared across resolution roots — its entries are version-addressed
and content-verified, so no root-local fact may live in it.

**direct requirement** (term): A declared dependency of a workspace
module — an entry in the `deps` map of a module the resolution root
uses.

## Module cache

**REQ-dep-cache-layout** (wire): A version-addressed artifact MUST be
stored at `<cache>/<escaped module path>/@v/<escaped version>.<kind>`
with `<kind>` one of `info`, `mod`, `zip`, `prov` and escaping per
`module-proxy.md` — the cache shares proxy storage's case-insensitivity
constraint. Entries are written atomically and whole; apart from the
transient temporary files atomic writes leave on interruption — never
read as entries — no other content lives under the cache root.

**REQ-dep-cache-transparent** (invariant): The cache MUST carry no
authority: an artifact read from the cache verifies against digests,
pins, and provenance exactly as a fetched one does, and every
operation's outcome is identical whether its artifacts come from the
cache or from sources. A cache entry failing content verification is
discarded as if absent and refetched — local corruption is not an
origin rewrite, and the refetched bytes still verify against the pin —
so cache state can change what is fetched, never what is accepted.
Version-addressed entries are otherwise never rewritten: the immutable
artifacts of `REQ-proxy-immutable` are cached without revalidation.

## Verbs

**REQ-dep-init** (behavior): `init <module path>` MUST write a
canonical module file declaring the given path in the working
directory, failing when a module file already exists there or when the
path violates `REQ-resolve-path-syntax`. It writes nothing else; a
lockfile appears when a resolving verb first records a pin.

**REQ-dep-tidy** (behavior): `tidy` MUST make each workspace module's
declared dependencies exactly the modules whose files that module's own
protobuf imports are satisfied by, at the versions the tidied graph
selects — dropping declarations no import of the declaring module uses,
adding a declaration for any build-list module it imports directly, and
re-emitting each changed module file canonically. An import satisfied
by no build-list module and no well-known import fails tidy per
`REQ-resolve-unsatisfied-imports`: with no registry, an import path
names no module, so tidy never invents a dependency. An import
satisfied by a workspace-local module keeps that module's existing
declared version — the local working copy has none to record — and
fails tidy when no declared version exists to keep. Tidy removes
lockfile pins for (module path, version) pairs outside the tidied
requirement graph, and is idempotent: a second run changes nothing.

**REQ-dep-download** (behavior): `download` MUST fetch, verify, and pin
the artifacts of every non-local build-list module into the module
cache — archive, module file (when the module declares one), info, and
provenance envelope (when the source has one) — recording first-use
pins per `REQ-lock-first-use` and recording provenance `none` only as
`REQ-prov-unsigned-recorded` allows. For a pin already recording
verified evidence, the envelope is cached only after re-verification
reproduces exactly the recorded provenance facts, held to the recorded
identity; served evidence that cannot fails per
`REQ-lock-no-silent-downgrade`, and evidence objects failing
verification for reasons other than absence or identity non-acceptance
are rejected exactly as at first use.

**REQ-dep-update** (behavior): `update` MUST move requirements forward
explicitly, to the highest tagged release discovered for the module —
proxy `@v/list` and origin release tags are exactly the advisory
discovery `REQ-proxy-list-advisory` sanctions. With module-path
arguments it updates the named modules and fails on a name no workspace
module requires, on a name whose discovery finds no release, and on a
name whose highest discovered release is lower than a declaration — an
origin that regressed is surfaced, never papered over. Without
arguments it updates every direct requirement with a discoverable
release higher than its declaration, skipping the rest. Updated
declarations are rewritten canonically in their declaring module files;
pin changes for updated versions follow `REQ-lock-first-use`, and any
rewrite of an existing pin is the explicit user-invoked update
`REQ-lock-no-silent-downgrade` sanctions. An argument naming an `oci`
plugin reference the root's generation configuration declares — that
configuration alone, and the plugin before any module of the same
spelling — updates that plugin's pin instead: the reference is
resolved anew — its tag to the digest it names now — its evidence
fetched anew and judged under the trust policy, and the pin's digest
and provenance record rewritten to what resolved and what was judged,
the same explicit update, reported with the record's transition; a
plugin reference with no pin, or one whose evidence the policy
requires and refuses, fails and leaves the pin as it was, and a
plugin is refused under a trust policy that forbids the `oci`
execution scheme, as generation refuses it. Every argument is placed
before any is moved, so an argument naming nothing fails the run
whole and moves nothing. Without arguments plugins are left as
pinned: a tag is its author's pointer, not a release order to move
along.

**REQ-dep-graph** (behavior): `graph` MUST print the requirement graph
— every edge of the reachable graph `REQ-resolve-mvs` defines — one
edge per line as `<requirer> <path>@<version>`, where the requirer is
`<path>@<version>` for a graph node and the bare module path for a
workspace module (the local working copy has no version). Output is
sorted lexically by line: a pure function of the graph, byte-identical
across runs (`REQ-resolve-determinism`).

**REQ-dep-why** (behavior): `why <module path>...` MUST print, for each
named path, a shortest requirement chain from a workspace module to
that path through the requirement graph — among equal-length chains the
lexically least — or state that the module is not needed. The answer is
deterministic and derives from the same graph `graph` prints.

**REQ-dep-verify** (behavior): `verify` MUST recompute, for every
pinned (module path, version) pair whose artifacts are present in the
module cache, the module digest and module-file hash against the pin,
reporting every mismatch — exhaustively and deterministically ordered —
and failing when any exists. The cached archive is the attested
artifact: the module-file hash is recomputed from its in-archive copy
(the same bytes as any standalone copy, by
`REQ-lock-modfile-consistency`), and a pair whose archive is not
cached is outside `verify`'s scope even when other artifact kinds are
— there are no attestable module bytes, and `download` followed by
`verify` covers the full pin set. This is the one sanctioned
exception to `REQ-dep-cache-transparent`'s outcome-identity: `verify`'s
subject IS the cache state, so its report legitimately depends on what
is cached — never on cached content evading verification.
