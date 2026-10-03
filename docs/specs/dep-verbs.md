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
`update`, `graph`, `why`, `verify`, invoked as `pb dep <verb>` (`pb clean`,
the stores' verb, stands beside them, REQ-dep-clean). Every
verb runs against the resolution root governing the working directory,
located per `workspace.md` (the nearest workspace above the directory,
else the nearest module root), and fails with no governing root or when
`REQ-work-membership` rejects the directory's module.

**module cache** (term): The persistent directory of fetched, verified
module artifacts: the `cache` setting — `$PBCACHE`, or the user
configuration file's `cache` key (`user-config.md`) — when set,
otherwise `pb/mod` under the platform user cache directory. The cache
is shared across resolution roots — its entries are version-addressed
and content-verified, so no root-local fact may live in it. Under
`vcs` it holds the origin repositories the `direct` source fetches
into (module-proxy.md REQ-proxy-direct-fetch), one directory per
origin URL holding two bare repositories, `snapshots` and `history`,
and the `lock` one process holds at a time, reused across roots and
runs, a fetch cache holding no root-local fact and no fact trusted on
presence.

**direct requirement** (term): A declared dependency of a workspace
module — an entry in the `deps` map of a module the resolution root
uses.

## Module cache

**REQ-dep-cache-layout** (wire): A version-addressed artifact MUST be
stored at `<cache>/<escaped module path>/@v/<escaped
version>.<digest>.<kind>` with `<kind>` one of `mod`, `zip`, `prov`
and `<digest>` the archive's module digest (`module-archive.md`
REQ-archive-digest) spelled with its `:` as `-`, so the name fixes
the content and two roots pinning one pair at two digests hold two
entries, each its own pin's — the info object, which no digest fixes,
at `<escaped version>.info` — and escaping per `module-proxy.md` —
the cache shares proxy storage's case-insensitivity constraint.
Entries are written atomically and whole; apart from the transient
temporary files atomic writes leave on interruption — never read as
entries — and the entries of the layout before the digest,
`<escaped version>.<kind>`, which no operation reads and the emptying
removes (REQ-dep-clean), no other content lives under the cache root.

**REQ-dep-cache-transparent** (invariant): The cache MUST carry no
authority: an artifact read from the cache verifies against digests,
pins, and provenance exactly as a fetched one does, and every
operation's outcome is identical whether its artifacts come from the
cache or from sources. A cache entry failing content verification is
discarded as if absent and refetched — local corruption is not an
origin rewrite, and the refetched bytes still verify against the pin —
so cache state can change what is fetched, never what is accepted.
Version-addressed entries are otherwise never rewritten: the immutable
artifacts of `REQ-proxy-immutable` are cached without revalidation,
and an entry is named by the digest that fixes its content
(REQ-dep-cache-layout), so a write — a first use's, a refetch's —
rewrites no bytes another root's pin names: an entry present under
the name holds the content the name fixes or is corruption, replaced
by the verified bytes.

## Verbs

**REQ-dep-init** (behavior): `init <module path>` MUST write a
canonical module file declaring the given path in the working
directory, failing when a module file already exists there or when the
path violates `REQ-resolve-path-syntax`. It writes nothing else; a
lockfile appears when a resolving verb first records a pin.

**REQ-dep-tidy** (behavior): `tidy` MUST make each workspace module's
declared dependencies exactly the modules whose files that module's own
protobuf imports are satisfied by, and the modules whose files the
imports of every module it reaches — by its declarations and by this
carrying, transitively — are satisfied by where the reached module's own
declarations, at its selected version, do not name the provider: a
synthesized module declares no dependency (`module-resolution.md`
REQ-resolve-synthesis), so the workspace module declaring it carries all
its files need, and a declaring module whose files import beyond its
declarations has that gap carried the same way, the consumer's
declaration the one that can, each such declaration reported, once per
tidy, in one line per module it is carried for, in one order — at the
versions the tidied graph selects — dropping declarations no import of
the declaring module or of a reached module's gap uses, adding a
declaration for any build-list module the declaring module imports
directly, and re-emitting each changed module file canonically. An
import satisfied by no build-list module and no well-known import fails
tidy per `REQ-resolve-unsatisfied-imports`: with no registry, an import
path names no module, so tidy never invents a dependency. An import
satisfied by a workspace-local module keeps that module's existing
declared version — the local working copy has none to record — and fails
tidy when no declared version exists to keep. Tidy removes lockfile pins
for (module path, version) pairs outside the tidied requirement graph,
and plugin pins no entry of the root's generation file names by
reference and scheme as written — every plugin pin where the root
has no generation file — so a pin outlives no declaration and a
reference declared again is a first use again (`module-lockfile.md`
REQ-lock-first-use), as `go mod tidy` prunes `go.sum`; and is
idempotent: a second run changes nothing. Tidy reads the graph
through the resolution root's replacements (`workspace.md`
REQ-work-replace, REQ-work-replace-dir): a replaced module's files
and declarations are its replacement's, a declaration stays on the
replaced path, a pinned replacement's pin is inside the tidied graph
and the replaced path's own is outside it, and a directory
replacement has none.

**REQ-dep-ruleset-declarations** (behavior): The verbs that act on
declared dependencies MUST act on ruleset imports where they live —
the lint file's `rulesets` and each imported rule file's `imports`
(`check-rules.md`) — exactly as they act on module files' `deps`:
`download` fetches, verifies and pins every fetched import's
artifacts, the rule files' imports among them, its line a module's (`<path>@<version>`, a replaced path's
`=> <replacement>`), a working-tree import fetching nothing; `update`
moves an import forward to the highest discovered release, rewriting
the lint file canonically and pinning the moved pair — a named path the lint file imports is found as one a workspace
module requires is (a working-tree import having nothing to move), a
replaced import is reported left as a replaced declaration is, and
of a path imported at several versions the highest moves while
another that would read the same release under a second alias is
left and reported by the sweep and fails the run, before any
declaration or import moves, when the path is named (a rule file's import, a fetched ruleset's or a working-tree
module's, is that ruleset's own and is never rewritten); `verify` covers the rulesets' pins after
the modules', a ruleset pin's line saying so; `graph` prints each
import as an edge from the lint file (`pb.lint.yaml
<path>@<version>`, a working-tree import's `pb.lint.yaml <path>`, the
working copy having no version) and from a rule file
(`<path>@<version> <path>@<version>`); `why` answers over those
edges too. Tidy leaves the imports alone:
they are no protobuf dependency, so no module file declares one and
nothing carries one; it removes the ruleset pins no import names — the lint file's or a
rule file's — as it removes module pins outside the tidied graph
(`module-lockfile.md` REQ-lock-pins-only).

**REQ-dep-download** (behavior): `download` MUST fetch, verify, and
pin the artifacts of every non-local build-list module into the module
cache — a replaced module's replacement in its place, its line naming
both as `<path>@<version> => <replacement>`, the replacement as the
workspace file spells it (REQ-work-emission) — a directory
replacement fetches nothing (`workspace.md` REQ-work-replace-dir) —
archive,
module file (when the module declares one), info, and
provenance envelope (when the source has one) — recording first-use
pins per `REQ-lock-first-use` and recording provenance `none` only as
`REQ-prov-unsigned-recorded` allows. For a pin already recording
verified evidence, the envelope is cached only after re-verification
reproduces exactly the recorded provenance facts, held to the recorded
identity — or, for a pinned-key record, to the recorded key as the
trust policy of the day still pins it for the module (`provenance.md`,
REQ-prov-pinned-key-recorded); served evidence that cannot fails per
`REQ-lock-no-silent-downgrade`, and evidence objects failing
verification for reasons other than absence or non-acceptance — an
identity the rule does not accept, a signer no pinned key vouches for
— are rejected exactly as at first use.

**REQ-dep-update** (behavior): `update` MUST move requirements forward
explicitly, to the highest tagged release discovered for the module —
proxy `@v/list` and origin release tags are exactly the advisory
discovery `REQ-proxy-list-advisory` sanctions. With module-path
arguments it updates the named modules and fails on a name no workspace
module requires, on a name whose discovery finds no release, and on a
name whose highest discovered release is lower than a declaration — an
origin that regressed is surfaced, never papered over. Without
arguments it updates every direct requirement with a discoverable
release higher than its declaration, skipping the rest. An import of
the lint file written without a version — a migration whose
discovery found none writes one (`migrate.md` REQ-migrate-rules) —
is a requirement with no release at all, below every release: the
sweep moves it to the highest discovered and naming the path moves
it, the sweep reporting one it finds no release for as left. A
requirement
on a replaced path (`workspace.md` REQ-work-replace,
REQ-work-replace-dir) is never discovered — the workspace consults
the replacement, never the replaced path's origin, and what the
replacement names is the workspace file's to move: the sweep leaves
the declaration and reports it as
replaced, and naming the path fails. Updated declarations are
rewritten canonically in their declaring module files;
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
edge per line as `<requirer> <path>@<version>`, where the requirer is `<path>@<version>` for a graph node and the
bare module path for a workspace module (the local working copy has
no version), the lint file's import edges among them
(REQ-dep-ruleset-declarations), a working-tree import's target the
bare path likewise. Output is
sorted lexically by line: a pure function of the graph, byte-identical
across runs (`REQ-resolve-determinism`). The graph's edges alone: a
replaced path's edges are the ones read from its replacement
(`workspace.md` REQ-work-replace), which `why` and `download` name.

**REQ-dep-why** (behavior): `why <module path>...` MUST print, for each
named path, a shortest requirement chain from a workspace module to
that path through the requirement graph — among equal-length chains the
lexically least — or state that the module is not needed. A chain
ending at a replaced path carries the replacement as its last line,
`<path>@<version> => <replacement>` as `download` spells it; naming a
replacement's module path answers, after any chain of its own, through
each path it replaces, in raw-byte order, each with its chain and
replacement line — a pinned replacement is needed by what it stands
for. The answer is deterministic and derives from the same graph
`graph` prints.

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
`verify` covers the full pin set; the entry checked is the one under
the pin's digest (REQ-dep-cache-layout), another digest's entry for
the pair being another pin's, and a pin recording no digest names no
entry and is outside the scope the same way. This is the one sanctioned
exception to `REQ-dep-cache-transparent`'s outcome-identity: `verify`'s
subject IS the cache state, so its report legitimately depends on what
is cached — never on cached content evading verification.

**REQ-dep-clean** (behavior): `pb clean` MUST empty the stores pb
keeps on the machine and nothing else — each store's layout names
what is its, and a directory the cache setting pointed at loses only
that — so the next operation refetches and re-verifies everything
from the lockfiles' pins, and the trusted root, the user
configuration, the lint files, the lockfiles and every resolution
root's files are never touched: `--modules` empties the module cache
— every artifact and every temporary under an `@v` directory whose
parent spells an escaped module path removed, the entries of the
layout before the digest among them, and the directories that
emptying left empty with them, recognized by name and layout alone
(REQ-dep-cache-layout: another tool's cache of the same shape
under the named directory is not this one's, a directory empty
already is not touched), and each origin under `vcs` emptied of its
repositories and probes under the origin's own lock, the directory
and its lock left for the origin's next fetch to initialize into; a
fetch in flight in another process either fails its write or lands a
whole verified entry that outlives the emptying, never a torn one,
and an emptying racing such a write may itself fail, a rerun by the
user succeeding;
`--plugins` empties the plugin store through the store's own removal
and collection (`plugin-execution.md` REQ-plugin-core-verifies names
the store): every root severed at once and the content collected
without its retention grace, an image a live run holds or a live
mount serves kept and reported by the platform manifest the store
materialized — it stands until that run's end or the next emptying —
a collection
halted or left incomplete failing the verb naming why, a store the
library refuses to open (a layout it does not recognize) not emptied
and the refusal, which names what to delete, reported; the kept
evidence (`provenance.md` REQ-prov-plugin-evidence-store) emptied with
it, temporary files included; and the residue of local runs — a
run's directory of copies left by a run that ended without removing
it — removed, a live run's kept, held by its lock
(`plugin-execution.md` REQ-plugin-local-pin); `--sources` empties the dependency
source store (`lsp.md` REQ-lsp-dependency-files names it) — every
module and well-known directory under it and every temporary an
atomic extraction left, recognized by name and layout alone as the
module cache is; with no flag every store is emptied, and a flag
empties its own store and no other. A plugin
running in another process reads its image from the store's export,
which its acquirer holds for the run's life (a hold, ocifs's api
contract) from the moment its acquisition returns: an emptying
landing in the instant between the acquisition and the hold collects
the image, and that run's export then fails loudly, a rerun by the
user succeeding — never a wrong result; otherwise the emptying
reports the held image
among the kept and its export stands until that run's end, when the
store's own collection reclaims it. The report names each
store emptied and every image kept. The verb is machine-scoped: it
needs no resolution root, and a store absent is emptied already,
never created to say so.
