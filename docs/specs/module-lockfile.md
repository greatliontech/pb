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
`none` MUST carry: `type`, the evidence type (`git-signed-tag`);
`objectFormat` (`sha1` or `sha256`); `object`, the hex git hash of the
signed object; and `identity`, with `san` and `issuer` strings naming the
verified Fulcio identity. No other evidence types are defined by this
document; new types extend this record with their own fields.

**REQ-lock-plugin-entry** (wire): Each plugin entry MUST carry, in order:
`ref` (the OCI reference as written in generation configuration, without a
digest), `digest` (the manifest-list digest the reference resolved to),
and `provenance` (a provenance record for the image signature, `none` when
unsigned). Plugin entries are sorted by `ref` in raw-byte order.

**REQ-lock-scalar-values** (wire): Every free-string fact — `version`,
`san`, `issuer`, and `ref` — MUST be printable non-space ASCII, start
with an alphanumeric character, not end with `:`, and not be a YAML null
spelling (`null`, `Null`, `NULL`, `~`). This bound is exactly what lets
canonical emission write every value as an unquoted plain scalar that
re-parses to the same bytes; values outside it are rejected on parse and
on emission alike. Paths, digests, hashes, and object fields are bounded
by their own grammars.

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
block style throughout, no comments, no anchors, no tags. Regenerating a
lockfile from the same facts yields byte-identical output.

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
