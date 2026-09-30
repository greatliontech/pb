# Package layering

pb's packages are grouped by the specs that govern them, into
domains that stand in an order: a package imports only packages of a
domain above its own in the table, or of its own domain. The layering
test beside this file reads the table below and holds the tree to it,
every non-test import edge and every package, so a new package is
placed here before it builds and the table can never say one thing
while the tree does another.

| Domain | Governing specs | Packages |
|---|---|---|
| shared rules | the contract-file prologue, atomic writes, the root-contained path rule, the repository search, each a rule several specs share | `contractfile`, `atomicfile`, `rootpath`, `gitdir` |
| plugin vocabulary | plugin-execution.md | `plugin` |
| module | module-file.md, module-archive.md, module-resolution.md, workspace.md, module-lockfile.md | `module`, `module/archive`, `module/version`, `module/modfile`, `module/mvs`, `module/workspace`, `module/lockfile` |
| provenance | provenance.md | `provenance`, `provenance/image`, `provenance/image/discover`, `provenance/image/evidence`, `provenance/trust` |
| source | module-proxy.md, module-resolution.md (path resolution and private origins) | `source`, `source/netrc`, `source/proxy`, `source/origin`, `source/direct`, `source/fetch` |
| proto | generation.md (the compile half), module-resolution.md (the import satisfaction half), export.md (the import closure), build.md (the descriptor set), check-rules.md (the reading of a source's lines its suppression clause takes) | `proto/modfiles`, `proto/compile`, `proto/importcheck`, `proto/lines` |
| user configuration | user-config.md | `userconfig` |
| plugin | plugin-execution.md, generation.md, plugin-publish.md | `plugin/local`, `plugin/oci`, `plugin/runner`, `plugin/genfile`, `plugin/genrequest`, `plugin/publish` |
| check | check-rules.md | `check`, `check/rules`, `check/env1`, `check/eval`, `check/lintfile`, `check/breaking` |
| migrate | migrate.md | `migrate`, `migrate/bufconfig` |
| driver | module-resolution.md | `resolve` |
| verbs | dep-verbs.md, check-rules.md §Verbs, export.md, build.md | `dep`, `cmd/pb` |

The rows are the order, top to bottom. Source, proto and plugin stand
level: none imports another, in either direction. The plugin
vocabulary sits above module because the lockfile and the trust
policy spell its schemes and tiers; user configuration sits just
above plugin because the runner is its one reader. A package name is
under `internal/` unless it names a command. The check domain sits
between plugin and the driver: it judges the compiled build and
reaches nothing below it in the table. Test support lives under
`testing/` (its own README) and is imported from test files only.
