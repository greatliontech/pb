# Audit: every name a layer hands out names the bytes it serves

The language server addressed a pinned replacement's files by the
pair the module file requires while the bytes were the replacement's
(now the pair whose bytes they are: `docs/specs/lsp.md`
REQ-lsp-dependency-files, `modfiles.Module`'s source beside its
requirement): the resolver filed the replacement under the name
callers look it up by, and a layer below derived file addresses and
a store layout from that name. One instance found by use; the audit
looks for the others before use finds them.

The rule. Names come in two kinds. A content name names bytes that
never change under it: a module archive by `path@version`, a
`pb-module://` address, a source store or module cache path, a
lockfile entry, a provenance evidence key, a plugin by its image
digest. A place name names a location whose content may change: a
working-tree file by its `file://` URI, a proxy's `@v/list` or
`@latest`, a tag before it is pinned. The fault is a content name
whose bytes can differ across time or configuration while the name
stays (a replacement's archive filed under the replaced pair), or a
place name used where a content name is due (a tag where a digest
belongs). A name for a requirement, alias, or rendering (what the
user asked for) lives in the graph, never in a content name. Go's
module cache, `go.sum` and gopls are the reference: a replacement
is stored and addressed under its own path, and only `go.mod` and
`go list` know it stands in for another.

Lenses, each walked over the scope below and its findings filed as
issues of their own (defects fixed in the audit's chunk where they
fit, the rest slotted):

1. Two identities in one field: a struct carrying one name for a
   thing that has two (what it stands for, what it is):
   `modfiles.Module` and `modfiles.Load` (the replacement filed
   under the replaced pair, chunk 29's instance), `dep.Pair`, the
   language server's `origin` (`internal/lsp/build.go`, built from
   a module's pair), the lockfile's module and plugin entries
   (`internal/module/lockfile`), a synthesized module file against
   the archive it was synthesized for, a workspace copy of a
   well-known file against the toolchain's, vanity and VCS-suffixed
   paths against the repository they resolve to
   (`internal/source/origin`), a plugin reference against the image
   digest it resolves to (`internal/plugin/genfile`,
   `internal/plugin/oci`), a catalog name against the registry
   reference it maps to (`internal/migrate`).
2. Convenience names: a field filled with what a caller looks a thing
   up by rather than what the thing is; every constructor of a
   module, origin, location or key from a build-list pair
   (`modfiles.Load`, `dep.Session`'s tables, `lsp.Server.at` and
   `moduleURI`, the source store's `copyDir`).
3. Resolver knowledge reconstructed downstream: `workspace.Root.Source`
   is the one place a replacement is resolved; any layer below it
   recomputing a replacement, alias or redirect (`internal/resolve`,
   `internal/dep`, `internal/lsp`) is a second source of truth.
4. Persisted and wire keys: every `pb-module://` address, source
   store path, module cache path, lockfile entry key, provenance
   evidence key and plugin store key, listed and classified as a
   content name, a place name or a graph name, each checked against
   its spec clause
   (`module-lockfile.md`, `provenance.md`, `plugin-publish.md`,
   `lsp.md` REQ-lsp-dependency-files).
5. Stability across reload: for each content name, what changes it
   and whether every holder of the old name is invalidated: the
   language server's navigation index against its judgement's table
   (the first instance), the plugin store's acquired images against
   the lockfile's pins, the module cache against a lockfile
   re-pinned by `pb dep` verbs.
6. The Go parallel: for each identity, what Go does
   (`docs/notes/go-parallels.md`), and where pb diverges, whether the
   divergence is written down with its reason.

Scope: internal/module (workspace, the resolver; lockfile; mvs;
archive), internal/resolve, internal/proto/modfiles, internal/dep
(judge, tidy, export), internal/source (origin, proxy, fetch,
direct), internal/provenance (the evidence keys), internal/lsp
(sources, build, positions), internal/plugin (oci, the store,
genfile references), internal/migrate (catalog names against
registry references), and the specs the keys live in
(`module-lockfile.md`, `provenance.md`, `plugin-publish.md`,
`module-proxy.md`, `workspace.md`, `lsp.md`).

Lands: 30.
