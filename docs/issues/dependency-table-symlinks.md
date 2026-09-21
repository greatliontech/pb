# Two BSR module roots hold symbolic links

Lands: the migrate plan's chunk 9

The dependency table (`migrate.md` REQ-migrate-deps) admits an entry
with its layout verified, and the layout test refuses two entries
before any file compiles, each origin's module root holding a
symbolic link, which a module's file set today admits none of
(`module-archive.md` REQ-archive-file-set, every regular file and
nothing else; REQ-archive-forbidden-entries, the tree invalid):

- `buf.build/bufbuild/protovalidate` at
  `github.com/bufbuild/protovalidate/proto/protovalidate`: `LICENSE`
  is a link.
- `buf.build/envoyproxy/protoc-gen-validate` at
  `github.com/bufbuild/protoc-gen-validate`: `example-workspace/.bazelrc`
  is a link.

The rule's ground does not hold: git stores a link as a tree entry
of mode 120000 whose blob is the target path, and a submodule as an
entry of mode 160000 whose hash is the submodule's commit id, no
blob behind it. A file set that records a link as that blob and a
submodule as that recorded id — the manifest carrying the id where
it carries a content hash for a file, the tree recompute taking it
as the entry's hash — recomputes the origin's tree hash exactly
(REQ-archive-tree-binding kept) in the origin's object format: the
id is copied, so the recompute's promise of either format
(REQ-archive-tree-recompute) narrows to the recorded one where a
submodule is present. Chunk 9 carries them — never followed, never
fetched, a proto reachable only through a link no module file, and
extraction onto a filesystem writing neither a link nor a submodule
directory, since protovalidate's own link points above the module
root — repealing REQ-archive-forbidden-entries and amending the file
set, mode normalization, manifest, tree recompute, zip mode and
materialization rules; the two entries then enter the table as the
layout test verifies each, envoy and xds with them, which depend on
protoc-gen-validate. Until then a migration naming either reports it
unmapped, and `--dep` names a fork without the links.
