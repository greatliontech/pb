# pb — lint and breaking-change rules

pb ships a rule engine and no rules. Every rule is a CEL expression over
standard protobuf descriptors; rulesets are ordinary modules acquired,
pinned, and verified exactly like any dependency. Lint rules judge one
schema; breaking rules judge an aligned pair of schema versions, with the
alignment computed by the engine, never by rules.

**rule** (term): A declared check: an identifier, a target, a severity, a
CEL expression evaluating to a boolean (true meaning the check passes),
and a message.

**ruleset** (term): A module containing rule files; consumed through the
ordinary module machinery — resolution, pinning, provenance.

**rule file** (term): A YAML file within a ruleset declaring rules and
the CEL environment version they target.

**CEL environment** (term): The versioned contract a rule evaluates in:
the bound variables, the descriptor types, and pb's standard library of
CEL functions (navigation, naming, comments, source info). Identified by
an integer version; additions bump the version.

**target** (term): The entity kind a rule binds: `file`, `package`,
`message`, `field`, `enum`, `enum-value`, `service`, `method`, or `set`
(the entire descriptor set, for global properties).

**lint file** (term): The file `pb.lint.yaml` at the resolution root:
ruleset imports, rule selection, severity overrides, per-path
exclusions, and the breaking-change comparison base.

## Rules

**REQ-rules-file-schema** (wire): A rule file MUST contain `celEnv`, the
integer CEL environment version its rules target, and `rules`, a list of
entries `{id, kind, target, severity, tags, cel, message}` where `kind`
is `lint` or `breaking`, `severity` is `error` or `warning`, and `tags`
is an optional string list. No other keys exist.

**REQ-rules-env-versioned** (invariant): The engine MUST refuse a rule
file whose `celEnv` names an environment version it does not provide —
never evaluating a rule against a different environment than it targets.

**REQ-rules-bounded** (invariant): Rule evaluation MUST be bounded: CEL
programs run with a cost limit, no I/O capability, and no host access —
a rule from any source is safe to evaluate by construction.

**REQ-rules-no-defaults** (behavior): With no rules configured, a check
run MUST report that zero rules are configured and produce no findings —
no built-in rules exist at any severity.

**REQ-rules-verdict** (behavior): A rule evaluating to false MUST yield
exactly one finding carrying the rule id, severity, message, and the
source location of the bound entity — locations come from the engine via
`SourceCodeInfo`, never from the rule.

## Breaking-change alignment

**REQ-break-pairing** (behavior): Breaking rules MUST evaluate over
entity pairs aligned by the engine: files, packages, messages, enums,
services, and methods pair by fully-qualified name; fields and enum
values pair by number within their paired parent; an entity present on
one side only forms a pair with an absent side. Rules see the pair;
rules never walk the diff.

**REQ-break-base** (behavior): The comparison base MUST come from the
lint file: a git reference, a tagged version, or the lockfile-pinned
version of the module under check.

## Configuration and suppression

**REQ-lint-config-schema** (wire): The lint file MUST contain, each
optional: `rulesets`, a list of module paths to import rules from;
`enable` and `exclude`, lists of rule ids or tags; `severity`, a map
from rule id to override; `ignore`, a list of `{paths, rules}` entries
excluding rule ids under path globs; and `breaking`, with the comparison
base. No other top-level keys exist.

**REQ-lint-suppression** (behavior): A finding MUST be suppressed by a
comment `// pb:ignore <rule-id>` (optionally followed by a reason) on
the flagged line or the line immediately preceding it — the rule id is
mandatory, and no comment form suppresses more than the named rule.
