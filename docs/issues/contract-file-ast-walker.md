# Contract-file AST walking is triplicated

`internal/modfile`, `internal/lockfile`, and `internal/trust` each
hand-walk the goccy YAML AST with the same discipline: exactly one
document, `yamlshape.Check`, top-level `MappingNode` assertion, string
keys dispatched with unknown-key rejection, and typed scalar extraction
with per-field errors. Three parallel mechanisms enforce one concept —
the strict contract-file surface.

Sketch of the collapse: a shared walker in a package such as
`internal/contractfile` — `Doc(data) (*ast.MappingNode, error)` for the
one-document/shape/top-level prologue, plus helpers for key dispatch
(`Keys(mapping, map[string]func(ast.Node) error)` with unknown-key
rejection) and typed extraction (`String(node, field)`,
`Sequence(node, field)`). The three parsers keep their schemas and error
vocabularies; the walking mechanics merge into the shared home. Deletes
the triplicated prologue and the per-package `keyString` helpers;
preserves each file's exact error-message surface (the tests pin those).

Lands: user decision (three-package refactor; touches modfile and
lockfile, whose emission/round-trip tests are byte-contractual).
