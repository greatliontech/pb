# Two BSR module roots hold symbolic links

Lands: user decision

The dependency table (`migrate.md` REQ-migrate-deps) admits an entry
with its layout verified: the module's own files compile from the
named root. The layout test run against the origins refuses two
entries before any file compiles, each origin's module root holding
a symbolic link, which a module's file set admits none of
(`module-archive.md` REQ-archive-file-set; the direct construction's
forbidden entries):

- `buf.build/bufbuild/protovalidate` at
  `github.com/bufbuild/protovalidate/proto/protovalidate`: `LICENSE`
  is a link.
- `buf.build/envoyproxy/protoc-gen-validate` at
  `github.com/bufbuild/protoc-gen-validate`: `example-workspace/.bazelrc`
  is a link.

Both are out of the table until the fork below is decided; a
migration naming either reports it unmapped, and `--dep` names a
mirror without the links. buf's own file set is the `.proto` files
alone, so buf never met the links.

The fork, the user's to weigh:

- A module's file set stays every regular file under its root, links
  refused: the archive is the tree as git holds it, its hash the
  origin's, and a link — a path git records as pointing elsewhere —
  is a file the module cannot carry. The two entries wait for their
  origins to drop the links, or for mirrors.
- The file set skips symbolic links (and submodule entries), as buf
  skips every non-proto file: the archive's tree hash then differs
  from the origin commit's for a tree holding one, so the invariant
  that refuses the entries (`module-archive.md`
  REQ-archive-forbidden-entries, stating that very reason) and the
  binding of the recomputed tree hash to the commit's
  (REQ-archive-tree-binding) must be restated over the file set
  rather than the tree, a soundness-sensitive change.

The tradeoff visible outside: the first keeps the two most common
validation modules out of the table until their origins change; the
second admits them and every origin like them, at the cost of an
archive whose hash is no longer the commit's tree.
