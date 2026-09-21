# The migration's no-heuristic invariant wants a property witness

Lands: the migrate plan's chunk 11

`migrate.md` REQ-migrate-no-heuristic says the verb synthesizes no
value it was not given: every module path, version, option value and
plugin reference comes from the command line, the buf file, the
tables or resolution, and where none supplies one the fact is
unmapped. The steps are pure functions over the configuration read
(`internal/migrate`: Modules, Deps, Rules, Gen), each lookup's two
outcomes pinned by example tests, and the invariant holds by that
shape; stipulator admits no attestation for an invariant, wanting a
property witness or an analyzer proof, so the requirement stands
uncovered.

The witness the shape affords: a property over generated buf
configurations and replacement sets whose scalars are drawn from a
distinguishable alphabet — marker tokens no table holds — asserting
that every scalar in every emitted pb file and in every mapped fact's
text is one of the input's tokens, a `--dep` or `--plugin` value, a
dependency or plugin table constant, the ruleset's path, or a
discovery answer, nothing else admitted. Set containment, no oracle:
the one form that catches a value synthesized from nowhere.
