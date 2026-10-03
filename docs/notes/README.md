# Notes

Working documents of two kinds. A pre-spec note tracks a concept area while
it is still being shaped — settled direction, rationale, and open questions
live together here; when an area is specced, the spec (`docs/specs/`)
becomes authoritative and the note is deleted or reduced to whatever remains
unsettled. A reference note keeps a comparison or a reading across specced
areas (the Go parallels), persists while it is kept current, and states no
contract: each row points at the spec that holds one. Notes of both kinds
are tracking artifacts: they may cite anything, and nothing kept-current may
cite them.

| Note | Concept area |
| --- | --- |
| [vision.md](./vision.md) | What pb is, what it replaces, and what it deliberately dissolves |
| [dependency-management.md](./dependency-management.md) | Go-module-style dependency management: git origin, proxy, archive format, provenance |
| [plugin-execution.md](./plugin-execution.md) | Local plugin execution: plain OCI images, sandbox tiers, strict platform rule |
| [ecosystem.md](./ecosystem.md) | Sibling repos pb builds on, their roles and maturity, and pbr's future |
| [sandbox-consolidation.md](./sandbox-consolidation.md) | Adopted cross-repo direction: sandbox is pb's native runner backend; container stays an independent mechanism-level runtime |
| [lint-breaking.md](./lint-breaking.md) | Lint & breaking-change engine: no built-in rules, all rules are CEL, rulesets are ordinary modules |
| [feature-set.md](./feature-set.md) | The command surface: core (Linux-only) / deferred / hard no, with reasons |
| [go-parallels.md](./go-parallels.md) | Where pb's mechanisms follow Go's module system and where they diverge, with the reason, row by row against the spec |
