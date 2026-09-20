# The contract-file parsers repeat one skeleton

Five YAML contract files are parsed by five hand-walked readers over
`contractfile.Doc`: the lockfile (`internal/module/lockfile`), the
generate file's plugin entries and overrides (`internal/plugin/genfile`),
the trust policy's rules and execution block (`internal/provenance/trust`),
and the rule file (`internal/check/rules`). Each repeats the same
skeleton: a known-key switch refusing `unknown key %q`, a sequence of
mappings with the index carried into every message, a `seen` map, and a
required-key sweep. The lint file (check-rules.md §Lint) will be the
sixth.

The collapse: one mapping walker at `internal/contractfile` taking a
key table — each key's reader (line, scalar, text, list, mapping) and
whether it is required — returning the values by key with the
positional message prefix supplied by the caller; the per-file parser
keeps only its field grammar and its cross-field rules. The family's
scalar readers (`Key`, `String`, `Scalar`, `Line`) already live there;
the user configuration file (`internal/userconfig`) and the module file
(`internal/module/modfile`) read string nodes directly under their own
specs (user-config.md: non-empty string scalars; module-file.md
REQ-modfile-acceptance), and join the walker where their spec's
spelling rule is the family's.

Invariants preserved: every message names the key and, in a list, the
index it reached; unknown keys and duplicate keys are refused before
any value is read — an ordering two parsers have each got wrong once
by reading the value first, so the walker holds it for all of them;
a file's spelling rule stays its spec's.

Lands: the lint file is built, the sixth parser being the one that
makes the walker pay for itself — the check-rules plan's lint-file
chunk.
