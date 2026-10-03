# Go parallels

pb's dependency model is Go's module system carried to protobuf, so
Go is the reference for every mechanism it shares: where pb does the
same, the row says so and why that is right here too; where pb
diverges, the row says why, and the divergence is a decision, never
an accident. A row states no contract — the spec it points at holds
the contract — and a row with no spec pointer is a gap to fill. This
note is a tracking artifact, extended as mechanisms land and audited
under the identity-audit issue; nothing kept-current cites it.

| Area | Go | pb | Same or diverges | Why | Spec |
| --- | --- | --- | --- | --- | --- |
| Module identity | `path@version`, the path a hostname-led import path | `path@version`, the path hostname-led | same | one identity for a module's bytes, spelled as its origin spells it | `module-resolution.md` REQ-resolve-path-syntax |
| Version selection | minimal version selection over the requirement graph | MVS over the requirement graph (`internal/mvs`) | same | reproducible, no solver, the highest of every requirement wins and nothing newer | `module-resolution.md` REQ-resolve-mvs |
| Workspace | `go.work` lists modules developed together | the workspace file lists modules under one root | same | one tree, one build of several modules | `workspace.md` |
| Replacement | `replace old => new v` in `go.mod`: the graph keeps `old`, the cache stores `new@v` under its own path, gopls addresses files there; a replacement may name one version of `old` (`replace old v1 => …`) or every version | `replace X: Y@v` makes Y@v stand for every version of X throughout the graph; addresses and the source store named by the replacement's pair, the pair whose bytes they are | same on the address; diverges on the version-specific form | a content name must name one content; the requirement's name belongs to the graph; one stand-in per path keeps the workspace file one line per replacement | `workspace.md` REQ-work-replace; `lsp.md` REQ-lsp-dependency-files |
| Where a replacement may be declared | `replace` in any `go.mod`, honored only in the main module's | `replace` in the workspace file alone; a module file carries none | same in effect | a replacement is the build's decision, never a dependency's | `workspace.md` |
| Excluding and retracting versions | `exclude` in `go.mod`, `retract` by the module's author | neither | diverges | no need has arisen; a bad version is not selected by pinning what is required, and a retraction waits on a demonstrated need | `module-resolution.md` REQ-resolve-mvs |
| Directory replacement | `replace old => ./dir` | `replace X: ./dir` | same | a local checkout stands in for a pair, read from the tree | `workspace.md` REQ-work-replace-dir |
| Lockfile | `go.sum`: hashes per `module@version`, no record of use; `-mod=readonly` refuses to change `go.mod` | the lockfile pins (module, version) to the archive digest, the module-file hash and the provenance record, written on first use by a verb; the language server never writes a pin and resolves nothing unpinned | diverges | pb's lockfile is also the provenance record and the first-use consent; an editor must not grant consent | `module-lockfile.md` REQ-lock-first-use, REQ-lock-digest-enforcement; `lsp.md` REQ-lsp-unpinned |
| Integrity | `go.sum` + the checksum database, a transparency log queried at verify time | the archive digest pinned at first use, provenance by sigstore evidence (signed tags with their Rekor inclusion proof embedded, image signatures) verified offline against a pinned trusted root | diverges | nothing is queried at verify time: the evidence travels with the artifact and trust is per origin | `provenance.md` REQ-prov-offline, REQ-prov-signed-tag |
| Private and untrusted origins | `GOPRIVATE`, `GONOSUMDB`, `GONOSUMCHECK` exempt paths from the checksum database | the trust policy names, per origin prefix, the identity or keys that must have signed, and what an unsigned subject is recorded as | diverges | trust is stated as a policy per origin, not as an exemption list | `provenance.md` REQ-prov-trust-schema, REQ-prov-unsigned-recorded |
| Proxy | `GOPROXY`: `@v/list`, `@latest`, `.info`, `.mod`, `.zip` per version | a dumb proxy serving the same shapes and `.prov` beside them, every version-addressed artifact immutable once served | same, plus provenance | a module is fetched by path and version from a cache that needs no VCS; its evidence travels with it | `module-proxy.md` |
| Path escaping | upper-case letters escaped with `!` in cache and proxy paths | the same escaping | same | one case-insensitive-safe spelling for every filesystem and proxy | `module-proxy.md` |
| Standard files | a module path that clashes with the standard library is refused | the toolchain answers for the well-known imports; a copy shipped by any module is ignored in the toolchain's favor | diverges | one definition of `google/protobuf/*` per toolchain, and a module shipping a copy is common upstream, so it is tolerated rather than refused | `generation.md` REQ-gen-compile; `module-resolution.md` well-known imports |
| Tidy | `go mod tidy` adds missing and drops unused requirements | `pb dep tidy` | same | the module file follows the imports | `dep-verbs.md` REQ-dep-tidy |
| Readiness of this note | | | | rows for the module cache layout, plugin references against image digests, vanity paths and VCS suffixes, and the source store wait on the identity-audit issue | |
