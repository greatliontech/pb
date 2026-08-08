# pb — module proxy protocol

The proxy protocol is the HTTP interface through which module artifacts are
fetched. A proxy is a dumb, stateless cache: it holds no accounts, accepts
no uploads, and carries no authority — every artifact it serves is verified
by the client against content digests and provenance evidence rooted at the
origin, so a proxy (or a chain of them) cannot tamper with module content.
Module digests, archives, and tree binding are defined in
`module-archive.md`; pins in `module-lockfile.md`.

**proxy** (term): An HTTP server implementing the endpoint set of this
document under a base URL.

**source list** (term): The ordered list of fetch sources a client
consults: proxies by base URL, the literal `direct` (fetch from origin),
or the literal `off` (fail when reached, consulting nothing further).

**escaped path** (term): A module path or version string encoded for use
in a URL: every uppercase ASCII letter is replaced by `!` followed by its
lowercase form (`!` itself is encoded as `!!`), leaving the path safe for
case-insensitive storage.

**pseudo-version** (term): A version string synthesized for an untagged
commit: `v0.0.0-<timestamp>-<hash>` when no tagged release precedes the
commit, `vX.Y.(Z+1)-0.<timestamp>-<hash>` when release `vX.Y.Z` precedes
it, and `vX.Y.Z-<pre>.0.<timestamp>-<hash>` when prerelease `vX.Y.Z-<pre>`
precedes it — "precedes" meaning the release is tagged on an ancestor of
the commit, the commit itself included
(module-resolution.md REQ-resolve-pseudo-base), `<timestamp>`
is the commit time as UTC `yyyymmddhhmmss`, and `<hash>` is the first 12
hex digits of the commit hash.

**verification pack** (term): The provenance evidence for a module
version under the `git-signed-tag` evidence type: the raw signed tag
object, the commit object it references, and, for a subtree module, the
tree objects on the path from the commit's root tree to the module root —
sufficient to verify the signature and, with the archive, recompute and
match the module root's tree hash offline.

**provenance envelope** (term): The JSON document served for a version's
provenance: `formatVersion` (integer `1`) and `evidence`, a list of
evidence objects each carrying a `type` string and type-specific fields.
An entry that is not a JSON object carrying a string `type` makes the
envelope malformed; a well-formed entry of unrecognized type is ignored
(REQ-proxy-prov-unknown), its type-specific fields opaque to the
consumer.

## Endpoints

**REQ-proxy-endpoints** (wire): A proxy MUST serve, under
`<base>/<escaped module path>/`, the GET endpoints:
`@v/list` (`text/plain`: zero or more tagged release versions, one per
line); `@v/<escaped version>.info` (`application/json`: object with
`version`, the canonical version string, and optionally `time`, an RFC
3339 timestamp); `@v/<escaped version>.mod` (`application/yaml`: the
module file bytes exactly as in the module's file set); `@v/<escaped
version>.zip` (`application/zip`: the module archive); `@v/<escaped
version>.prov` (`application/vnd.pb.provenance.v1+json`: the provenance
envelope); and `@latest` (`application/json`: the info object of the
highest tagged release, or of a recent commit's pseudo-version when no
tag exists).

**REQ-proxy-not-found** (wire): A proxy MUST answer 404 or 410 for a
module, version, or artifact it does not have — including `.mod` for a
synthesized module and `.prov` for a version with no provenance evidence
— and these are the only statuses a client treats as "not here".

**REQ-proxy-immutable** (invariant): A response for a version-addressed
artifact (`.info`, `.mod`, `.zip`, `.prov`) MUST be immutable: once a
proxy has served content for a (module path, version, artifact) triple,
it never serves different content for that triple. `@v/list` and
`@latest` are the only endpoints whose responses may change over time.

**REQ-proxy-list-advisory** (invariant): `@v/list` is advisory: version
selection over declared requirements MUST NOT depend on list completeness
— an omitted version affects only discovery of newer versions, never the
correctness of resolving declared ones.

## Provenance envelope

**REQ-proxy-prov-envelope** (wire): A `git-signed-tag` evidence object
MUST carry: `type` (`"git-signed-tag"`), `objectFormat` (`"sha1"` or
`"sha256"`), `tag` (base64 of the raw annotated tag object), `commit`
(base64 of the raw commit object), and `treePath` (list, possibly empty,
of base64 raw tree objects ordered from the commit's root tree toward the
module root). Each base64 value is canonical — standard alphabet with
padding, no embedded whitespace, no nonzero spare trailing bits — so a
given object has exactly one wire spelling; and a raw git object is
never empty.

**REQ-proxy-prov-unknown** (behavior): A consumer MUST ignore evidence
objects whose `type` it does not recognize, failing instead only on an
envelope whose `formatVersion` it does not support.

## Client behavior

**REQ-proxy-fallthrough** (behavior): A client MUST try sources in source-
list order, moving to the next source only on 404 or 410 — any other
failure (transport error, 5xx, malformed response) aborts the fetch
without consulting later sources. `direct` fetches from the origin; `off`
fails when reached. A proxy's redirects are followed only to HTTPS URLs
and boundedly — proxies legitimately redirect artifacts to blob storage
— with the status classification applying to the final response; a
cleartext or excessive redirect aborts like any transport failure, and
no cookie state crosses a fetch in either direction.

**REQ-proxy-config** (behavior): The source list MUST come from `PBPROXY`
(comma-separated entries, default `direct`), with `PBNOPROXY`
(comma-separated glob patterns in path-glob syntax: `*` and `?`
wildcards, character classes, backslash escapes) routing matching
modules to `direct` regardless of `PBPROXY` — a pattern matches a module
when it globs the module path or any leading segment prefix of it, `*`
never crossing a segment boundary, so `corp.example.com` covers every
module on that host. A malformed entry or pattern is a configuration
error, never a silent non-match. There is no default proxy: with no configuration,
every fetch goes to the origin, and no party beyond the origin host is
trusted for a first fetch.

**REQ-proxy-client-verification** (invariant): A client MUST NOT accept
any artifact on transport trust: archives and module files are verified
against their digests and pins (recomputed per `module-archive.md`,
matched per `module-lockfile.md`), and provenance evidence is verified
against the origin identity before use. The acceptance policy — which
identities are required, whether absence is tolerated — is configuration,
outside this document's scope; the protocol's obligation is that every
byte a proxy serves is verifiable without trusting the proxy. The no-
trust posture extends to resources: a client reads no more of a response
than its artifact-size bound — a response beyond `REQ-archive-size-limit`
can never verify, so reading it would trust the proxy with unbounded
memory.

**REQ-proxy-direct-equivalence** (behavior): The `direct` source MUST
yield artifacts indistinguishable from a well-behaved proxy's: the same
canonical archives, module file bytes, info objects, and verification
packs, constructed from the origin repository itself.
