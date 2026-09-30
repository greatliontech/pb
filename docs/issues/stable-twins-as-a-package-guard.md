# The `_STABLE` twins as one package guard

## What stands

The buf-rules ruleset declares every breaking rule twice: the rule,
and a `<ID>_STABLE` twin whose expression is the rule's call guarded
by `unstablePackage(...)` over the pair's files, tagged `<TAG>_STABLE`
for each of the rule's tags. The twins are how buf's
`ignore_unstable_packages` maps to a pb selection
(`docs/specs/migrate.md` REQ-migrate-rule-options): every breaking
name a migrated selection enables, excludes or ignores is read as its
`_STABLE` twin. Sixty-eight rules carry a twin; the twin's body is the
rule's, one guard longer.

## The collapse

One mechanism could replace the twins: a lint-file selection guarding
a kind by package, so that a breaking finding whose file, on either
side of the pair, lies in a package with an unstable version suffix is
dropped, as buf drops it — the guard a fact of the selection, not of
each rule. The ruleset would then carry one rule per check, the
migration would map `ignore_unstable_packages` to the guard, and the
`_STABLE` names would go.

What merges: the sixty-eight twins into their base rules. What is
deleted: the twin rules, the `_STABLE` tags, the `stable` reading in
`internal/migrate/rules.go`. Invariants preserved: a migrated
selection under `ignore_unstable_packages` reports exactly the
findings buf reported (`internal/check/catalog`'s
`TestUnstablePackage` pins the guard's grammar and either-side
reading; it pins the guard wherever it lives).

## The fork

The two shapes differ in what a lint file can say, which is the
user's call:

- **The twins stay.** The lint file's grammar is unchanged: a
  selection names rules and tags, nothing else; a ruleset expresses
  every shaping in rule bodies, as `check-rules.md` has it (a rule has
  no parameters). The cost is the ruleset's size and a name convention
  (`_STABLE`) the migration and its readers must know.
- **A package guard in the selection.** The lint file gains a concept
  — a per-selection predicate over the finding's package, on either
  side of a pair — that `check-rules.md` REQ-lint-config-schema does
  not have today and every ruleset could then rely on. The cost is a
  new lint-file surface, its own spec section, and a second way to
  shape a rule beside its body.

Lands: user decision — the fork above: a rule's shaping expressed in
rule bodies alone (twins stay), or a selection-level package guard
added to the lint file (twins collapse).
