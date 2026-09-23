# pb — module lockfile

The lockfile pins the content identity and provenance facts of every module
a resolution has consulted. It is a pin store, not a resolution store:
version selection is recomputed from module requirement declarations, and
the lockfile's job is to guarantee that the bytes and provenance behind
every (module path, version) pair never change silently. Module digests and
manifest recomputation are defined in `module-archive.md`.

**lockfile** (term): The file `pb.lock`, sitting next to the module file of
the resolution root (the module or workspace whose dependencies are being
resolved).

**pin** (term): A lockfile entry's recorded facts for one (module path,
version) pair: the module digest, the module-file hash, and the provenance
record.

**module-file hash** (term): `sha256:` followed by 64 lowercase hex digits
of the SHA-256 of a module file's exact bytes as they appear in the
module's file set.

**provenance record** (term): The lockfile's statement of what provenance
evidence was verified for a pinned version: either the literal `none`, or a
record naming the evidence type and the verified identity.

## Format

**REQ-lock-format** (wire): The lockfile MUST be UTF-8 YAML with LF line
endings containing, in order: `version`, whose value is the integer `1`;
`modules`, a block-style list of module entries; and, only when plugin
pins exist, `plugins`, a block-style list of plugin entries. No other
top-level keys exist.

**REQ-lock-entry** (wire): Each module entry MUST carry, in order: `path`
(the module path), `version` (the version string), `digest` (the module
digest, present when the module's archive has been fetched), `modfile`
(the module-file hash, present when the module declares a module file),
and `provenance` (the provenance record). No other keys exist.

**REQ-lock-provenance-record** (wire): A provenance record other than
`none` MUST carry `type`, the evidence type, then the type's own
fields, then `identity`, with `san` and `issuer` strings naming the
verified Fulcio identity — a pinned-key record `key` in its place. A `git-signed-tag` record carries
`objectFormat` (`sha1` or `sha256`) and `object`, the hex git hash of
the signed object. An `image-signature` record — a sigstore signature
over the plugin entry's digest (`provenance.md`) — carries no field of
its own: the entry's `digest` is what was signed. A `git-signed-tag`
record belongs to a module entry and an `image-signature` record to a
plugin entry; a record of the other type under an entry, and a record
naming a type's field under another type, are invalid. No other
evidence types are defined by this document but the pinned-key record
of REQ-lock-pinned-key-record.

**REQ-lock-pinned-key-record** (wire): A `git-pinned-key` record — a
module entry's evidence accepted under a trust-policy rule naming
pinned keys (`provenance.md`, REQ-prov-pinned-key-eval) — MUST carry
`objectFormat` and `object` as a `git-signed-tag` record does, then
`key`, with `kind` (`openpgp` or `ssh`) and `fingerprint` naming the
pinned key that verified the signature, in place of `identity` —
the fingerprint spelled as the kind spells it (`provenance.md`,
REQ-prov-pinned-keys-schema: an OpenPGP key's uppercase hex, forty
digits for a version 4 key and sixty-four for a version 6 one; an
SSH key's `SHA256:` and forty-three base64 digits). It belongs to a
module entry alone.

**REQ-lock-plugin-entry** (wire): Each plugin entry MUST carry, in order:
`ref` (the plugin identity as written in generation configuration, without
a digest), `scheme` (its identity scheme, `oci` or `local` —
`plugin-execution.md`), then the scheme's own facts and no others. An
`oci` entry carries `digest` (the manifest-list digest the reference
resolved to) and `provenance` (a provenance record for the image
signature, `none` when unsigned). A `local` entry carries `binary`, a
mapping from host platform (`<os>/<arch>`) to the content hash
(`sha256:` + 64 lowercase hex digits) of the resolved binary on that
platform, and no provenance key — a host binary has no evidence to
record, and its absence is not spelled `none`. The scheme is stated,
never inferred from which fields are present; a pin satisfies only
lookups in its own scheme, so an entry migrated between schemes takes a
fresh first-use pin. Plugin entries are sorted by `ref` then `scheme` in
raw-byte order.

**REQ-lock-scalar-values** (wire): Every free-string fact — `version`,
`san`, `issuer`, and `ref` — MUST be printable non-space ASCII, start
with an alphanumeric character — a `ref` alone may also start with
`/` or `./`, the starts a `local` plugin's path is written with; `.`
alone leads YAML's float spellings and is excluded — not end with
`:`, and not be a YAML null
spelling (`null`, `Null`, `NULL`, `~`). This bound is exactly what lets
canonical emission write every value as an unquoted plain scalar that
re-parses to the same bytes under the lockfile's own reader, which
takes every value as the text written and types none — a spelling a
generic YAML reader would type, `y`, `True`, `0x1f`, is read as its
text here, the lockfile being read by pb alone; values outside the
bound are rejected on parse and on emission alike. Paths, digests,
hashes, and object fields are bounded by their own grammars.

**REQ-lock-acceptance** (wire): Parsing MUST accept key-order, comment,
line-ending, and quoting variants that yield the recorded facts
unambiguously — normalized by canonical re-emission — and reject merge
keys, anchors, aliases, tags, non-string mapping keys, empty or
degenerate provenance record mappings, and, for the free-string facts of
REQ-lock-scalar-values, YAML null spellings and quoted spellings
containing escapes. A parse-and-re-emit cycle never alters a recorded
fact.

**REQ-lock-canonical-emission** (invariant): Lockfile emission MUST be a
pure function of the recorded facts: entries sorted by path then version
(raw-byte order), keys in the specified order, two-space indentation,
block style throughout, no comments, no anchors, no tags, every value an
unquoted plain scalar — each fact's own grammar, REQ-lock-scalar-values
for the free strings, admitting no spelling the lockfile's reader
retypes — never a rendering the lockfile's reader rejects or reads as
a different file.
Regenerating a lockfile from the same facts yields byte-identical
output.

## Semantics

**REQ-lock-pins-only** (structural): The lockfile MUST NOT record version
selection, build lists, or any fact derivable by re-running resolution
over the pinned module files; it records only pins.

**REQ-lock-digest-enforcement** (invariant): A fetched artifact whose
recomputed digest or module-file hash differs from its pin MUST fail the
operation with an error naming the module path, version, expected hash,
and computed hash. No operation updates a pin as a side effect of a
mismatch. Module-file-hash enforcement applies when the verifying
operation computed a module-file hash from fetched content; an operation
that verified the digest has already verified the declared module file's
bytes, which the file set contains (`module-archive.md`).

**REQ-lock-first-use** (behavior): Resolving a (module path, version) with
no existing pin MUST compute its digest and module-file hash from fetched
content, evaluate provenance evidence, and record the pin. Pins are only
added or explicitly updated, never silently rewritten.

**REQ-lock-modfile-consistency** (invariant): When both a standalone
module file and the module archive have been fetched for the same pinned
version, the standalone bytes MUST agree with both the pinned module-file
hash and the archive's module file bytes; any disagreement fails the
operation.

**REQ-lock-no-silent-downgrade** (invariant): A pin whose provenance
record names verified evidence MUST NOT transition to `none`, to a
different evidence type, to a different identity, or to a different
signed object except by an explicit user-invoked update of that pin;
re-resolution that can no longer verify previously recorded provenance
fails rather than rewriting the record. A changed signed object for the
same (module path, version) means the origin tag moved — a rewrite, not
a refresh: new releases are new versions with their own pins.
