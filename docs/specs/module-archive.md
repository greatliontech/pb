# pb — canonical module archive

The canonical archive is the persisted form of a protobuf module: the file
set a module version contains, its content-addressed digest, and the wire
container that carries it. The digest is the identity every other contract
(lockfile pins, proxy responses, provenance verification) refers to, and the
format binds each archive back to the git tree it was derived from, so that
a signature over the origin's git objects proves the archive's contents.

**module** (term): A directory tree of files, rooted at a module root,
identified by a module path and versioned by git tags or pseudo-versions.

**module path** (term): The identity of a module: a hostname followed by
one or more slash-separated path segments, possibly extending into a
subtree of the origin repository.

**origin** (term): The git repository, and subtree within it, that a module
path designates. The discovery mechanism mapping a module path to a
repository URL is outside this document's scope.

**module root** (term): The directory at which a module is rooted: for a
declared module, the directory containing its module file; for a
synthesized module, the subtree the module path addresses. The module root
is the include root for protobuf imports of the module's files.

**module file** (term): `pb.yaml`, the file declaring a module at its
module root. Its schema is outside this document's scope.

**synthesized module** (term): A module derived from an origin subtree that
contains no module file: its file set and include root are the subtree's,
and it declares no dependencies.

**file set** (term): The complete set of entries under a module root at
a given commit, each carrying a path relative to the module root and a
mode: regular files, with content bytes; symbolic links, with the target
path git stores as the link's content; submodule entries, with the
commit id git records and no content. Directories are represented only
implicitly through paths; empty directories do not exist.

**canonical manifest** (term): The canonical textual listing of a file set
from which the module digest is computed.

**module digest** (term): The content-addressed identity of a module
version: the SHA-256 hash of its canonical manifest, rendered as
`pb1:<64 lowercase hex digits>`.

## File set

**REQ-archive-file-set** (structural): The file set of a module version
MUST contain every entry under the module root at the referenced commit
— every regular file, symbolic link and submodule entry, not a filtered
subset — and nothing else.

**REQ-archive-links-carried** (invariant): A symbolic link and a
submodule entry under the module root MUST be carried as git stores
them and never followed nor fetched: a link as its target path for
content, mode `120000`, no consumer reading the target as a path — a
file reachable only through a link is no file of the module, a link
named like the module file no module file; a submodule entry as the
commit id git records for it, mode `160000`. Neither is materialized
onto a filesystem (REQ-archive-no-exec-materialization); both exist in
the file set for digest and git-tree fidelity, so that the tree hash
of a module root holding either recomputes exactly
(REQ-archive-tree-binding).

**REQ-archive-nested-module** (invariant): A module tree containing a
module file at any path strictly below the module root is invalid: archive
creation and verification MUST fail. Repositories host multiple modules as
disjoint subtrees, never nested.

**REQ-archive-path-rules** (wire): Every path in a file set MUST be a
relative path using `/` as separator, in valid UTF-8, with no empty
segment, no `.` or `..` segment, no leading or trailing `/`, no control
character (C0 or DEL — a `\n` would break manifest framing, a NUL git
tree encoding), none of the characters `:` `<` `>` `"` `|` `?` `*` `\`
(invalid in Windows file names; `\` is a separator there), no segment
ending in a dot or a space (Windows strips them on extraction, colliding
distinct names), and no segment that is a Windows reserved device name —
`CON`, `PRN`, `AUX`, `NUL`, or `COM`/`LPT` followed by a digit `1`–`9`
or a superscript `¹` `²` `³` — case-insensitively, with or without an
extension. Paths are compared and sorted as raw bytes; no Unicode
normalization is applied.

**REQ-archive-case-collision** (invariant): A file set is invalid — and
creation and verification MUST both fail — when two distinct file paths
are equal under Unicode simple case folding, or when a file path's
case-folded form equals the case-folded form of a directory prefix
implied by any path (which includes the byte-identical case: a name
cannot be both a file and a directory). Directory prefixes fold-equal
only to each other do not invalidate a file set: extraction can merge
such directories on a case-insensitive filesystem, but no file entry is
lost — within-directory basenames are fold-distinct by the rule above —
and digest verification reads archive members, never the filesystem.

**REQ-archive-mode-normalization** (wire): Each entry's mode MUST be
exactly `100755` when the entry is an executable file, `100644` for any
other regular file, `120000` for a symbolic link and `160000` for a
submodule entry; no other mode exists in a file set.

**REQ-archive-size-limit** (behavior): A file set whose total content size
— a link's target counted as its content, a submodule entry carrying
none — exceeds 500 MiB MUST be rejected at creation and at verification.

## Manifest and digest

**REQ-archive-manifest** (wire): The canonical manifest of a file set MUST
be exactly: the header line `pb-module-manifest/v1`, followed by one line
per entry in ascending raw-byte order of path, each line being
`<mode> <hash> <path>` — the entry's mode, its hash, and its path,
separated by single spaces — with every line terminated by a single
`\n` and no other bytes present. An entry's hash is the lowercase hex
SHA-256 of its content bytes, a link's target being its content; a
submodule entry's is the recorded commit id in lowercase hex.

**REQ-archive-digest** (wire): The module digest MUST be the SHA-256 hash
of the canonical manifest bytes, rendered as `pb1:` followed by 64
lowercase hex digits.

**REQ-archive-digest-purity** (invariant): The module digest MUST be a pure
function of the file set — paths, modes, content bytes and recorded
commit ids. Two archives carrying the same file set have the same digest
regardless of producer, archive encoding, compression, or transport.

## Wire container

**REQ-archive-zip** (wire): The wire form of a module version MUST be a ZIP
archive whose member names are exactly the file set's paths — verification
rejects an archive with a member outside the file set, a missing member, a
duplicate member name, encryption, a member whose recorded type is none
a file set holds, or a member using a compression method other than
store or deflate, and ignores directory entries.

**REQ-archive-zip-mode** (wire): Producers MUST record each member's mode
in the ZIP Unix external attributes, the member's "version made by"
field naming the Unix host — the attributes are Unix ones under no other
host: a regular file's `100644` or `100755`; a link's `120000`, the
member's bytes being the target path; a submodule entry's `160000`, the
member's bytes being the recorded commit id.

**REQ-archive-no-exec-materialization** (invariant): Tooling materializing
archive contents onto a filesystem (cache extraction, export) MUST
write neither an executable file, nor a symbolic link, nor a submodule
entry — the execute mode, a link's target and a submodule's id exist
in the manifest solely for digest and git-tree fidelity, module
content is never executed, and a link may point anywhere; nothing in
the toolchain runs a file that arrived in a module archive, and
nothing follows a link one carries.

**REQ-archive-zip-verification** (invariant): A consumer MUST accept a ZIP
only after recomputing the canonical manifest from the extracted members —
deriving each member's mode by normalizing the member's recorded
attributes (a member made by any host but Unix records no mode and is a
regular file, `100644`; under the Unix host a link's type means
`120000`, a submodule entry's `160000`, otherwise any execute bit set
means `100755` and else `100644`), a submodule member's bytes read as
the recorded id — and matching the manifest's digest against the
expected module digest; the ZIP's own byte encoding carries no
authority.

## Git tree binding

**REQ-archive-tree-recompute** (behavior): For a git object format (SHA-1
or SHA-256), the git tree hash of a file set MUST be recomputable from the
canonical manifest's inputs alone, as follows: each file's or link's blob
hash is the object-format hash of `blob <decimal content length>\0`
followed by the content bytes, a link's target being its content; a
submodule entry's hash is the recorded commit id itself, so a file set
holding one recomputes in the format the id's length names and in no
other; each directory's tree object lists its immediate entries sorted by
name, where a directory entry sorts as its name with `/` appended, each
entry encoded as the ASCII octal mode without leading zeros (`100644`,
`100755`, `120000`, `160000`, or `40000` for subdirectories), a space,
the entry name, a NUL byte, and the raw hash of the entry's object; the
directory's tree hash is the object-format hash of
`tree <decimal encoded length>\0` followed by the encoded entries.

**REQ-archive-tree-binding** (invariant): The recomputed tree hash of an
archive's file set MUST equal the git tree hash of the module root in the
origin commit it claims to derive from: for a module rooted at the
repository root, the commit object's tree; for a subtree module, the hash
reached by walking the commit's tree through the subtree path. An archive
whose recomputed tree hash does not match is rejected. This binding is what
lets a signature over the origin's git objects prove the archive's
contents without trusting whoever produced the archive.
