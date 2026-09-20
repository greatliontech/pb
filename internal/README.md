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
| shared rules | the contract-file prologue, atomic writes, the root-contained path rule, each a rule several specs share | `contractfile`, `atomicfile`, `rootpath` |
| plugin vocabulary | plugin-execution.md | `plugexec` |
| module | module-file.md, module-archive.md, module-resolution.md, workspace.md, module-lockfile.md | `archive`, `modpath`, `version`, `modfile`, `mvs`, `workspace`, `lockfile` |
| provenance | provenance.md | `provenance`, `imagesig`, `imagesig/discover`, `imagesig/evidence`, `trust` |
| source | module-proxy.md | `proxy`, `origin`, `direct`, `modfetch`, `httpspolicy` |
| proto | generation.md, the compile half | `modfiles`, `protocomp`, `protoimport` |
| user configuration | user-config.md | `userconfig` |
| plugin | plugin-execution.md, generation.md | `pluglocal`, `plugoci`, `plugrun`, `genfile`, `genrequest` |
| driver | module-resolution.md | `resolve` |
| verbs | dep-verbs.md | `dep`, `cmd/pb` |

The rows are the order, top to bottom. Source, proto and plugin stand
level: none imports another, in either direction. The plugin
vocabulary sits above module because the lockfile and the trust
policy spell its schemes and tiers; user configuration sits just
above plugin because the runner is its one reader. A package name is
under `internal/` unless it names a command. check-rules.md has no
packages yet; its domain enters the table between plugin and the
driver with its first package. Test support lives under `testing/`
(its own README) and is imported from test files only.
