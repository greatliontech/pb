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
`message`, `field`, `oneof`, `enum`, `enum-value`, `service`, `method`,
`extension`, or `set` (the entire descriptor set, for global
properties).

**finding** (term): One failed check, as REQ-rules-verdict composes
it.

**checked modules** (term): The workspace's modules: what a check run
judges. Their dependencies are compiled with them and reachable to
rules through the environment's lookup functions, and are never judged
themselves.

**lint file** (term): The file `pb.lint.yaml` at the resolution root:
ruleset imports, rule selection, severity overrides, per-path
exclusions, and the breaking-change comparison base.

## Rules

**REQ-rules-file-schema** (wire): A rule file MUST contain `celEnv`, the
CEL environment version its rules target written as unquoted decimal
digits with no sign and no leading zero, and `rules`, a list — possibly empty — of entries
`{id, kind, target, severity, tags, cel, message}` where every value
is read as the text written in any YAML scalar spelling, `id` and
`message` and each entry of the optional list `tags` are one non-empty
line of text — a spelling holding a line break refused, since an id is
written in a suppression comment and a message on a finding line —
`cel` is non-empty text, a block scalar included, `kind` is `lint` or
`breaking`, `severity` is `error` or `warning`, `target` is a target,
and ids are unique within the file. No other keys exist.

**REQ-rules-file-discovery** (behavior): A ruleset's rule files MUST be
every file named `*.rules.yaml` under the module root, at any depth,
read in path order — the byte order of their module-relative paths; a ruleset
with none contributes no rules, and a file that fails
REQ-rules-file-schema fails the check run naming the ruleset and the
file.

**REQ-rules-env-versioned** (invariant): The engine MUST refuse a rule
file whose `celEnv` names an environment version it does not provide —
a number beyond any it could provide included — never evaluating a
rule against a different environment than it targets.

**REQ-rules-bounded** (invariant): Rule evaluation MUST be bounded: CEL
programs run with a cost limit — each library function charged at least
the size of its result and of the collection it searches, so a call over
the whole set costs what the set costs — no I/O capability, and no host
access; a rule from any source is safe to evaluate by construction.

**REQ-rules-no-defaults** (behavior): With no rules enabled, a check run
MUST report that zero rules are enabled and produce no findings — no
built-in rules exist at any severity.

**REQ-rules-verdict** (behavior): A rule evaluating to false MUST yield
exactly one finding carrying the rule id, severity, message, and the
location REQ-rules-finding-location assigns — positions come from the
engine via `SourceCodeInfo`, never from the rule.

**REQ-rules-finding-location** (behavior): A finding's location MUST be
the bound entity's declaration in the checked schema — its file, and
the declaration's first line and column — a file's its first lexical
element's, a leading comment passed over — 1-based, the column
counted in Unicode code points — each byte starting a UTF-8
sequence, as the compiler counts; for a pair whose new side is
absent, the old
side's declaration in the base, marked as the base's; for a `package`
rule, the first of the package's checked files in path order, without
a position; for a `set` rule, no location.

## CEL environment 1

The first environment. Every rule file targeting it evaluates over
these bindings and this library and nothing else; a rule that needs
more is the reason for environment 2, never for an addition under this
number.

**word segmentation** (term): The split of a name into words the
naming functions share. Every character that is neither a letter nor
a digit, the underscore included, separates and belongs to no word; a
boundary also falls at a lower-case letter followed by an upper-case
one (`fooBar` is `foo`, `Bar`), within a run of upper-case letters
before the last one when a lower-case letter follows it
(`HTTPServer` is `HTTP`, `Server`), and at a digit followed by an
upper-case letter in a run that also holds a lower-case letter
(`Foo2Bar` is `Foo2`, `Bar`; `V2X` is one word, so an upper-snake
name is its own words); no boundary falls between a letter and a
following digit or a digit and a following lower-case letter
(`v1beta1` is one word). A name's words are its
non-empty runs between boundaries, so a qualified name's dots yield
no words and a rule judging its segments splits it first.

**acronym** (term): A word with at least two letters whose letters
are all upper-case (`HTTP`, `ID2`).

**REQ-env1-population** (behavior): For a lint rule a target MUST
bind, in a checked file, every entity of its kind and no other — for a
breaking rule the population is the pairs REQ-break-pairing aligns,
each side's entities drawn from that side's schema by the same rule:
`file` each checked file;
`package` each package a checked file declares, once — a file
declaring none binds no package; `message` each
message declared in a checked file, nested ones included and the
map-entry messages the compiler synthesizes excluded; `field` each
field of a bound message, oneof members included; `oneof` each oneof
declared, the synthetic oneofs of proto3 optional fields excluded;
`enum` and `enum-value` each enum and each of its values; `service`
and `method` each service and each of its methods; `extension` each
extension wherever declared; `set` once, over the checked modules'
files.

**REQ-env1-bindings** (wire): Environment 1 MUST bind, for a lint rule,
the entity under its target's name — `enum-value` as `enumValue`, a
binding being an identifier — as its standard descriptor proto
(`google.protobuf.FileDescriptorProto` for `file`, `DescriptorProto` for
`message`, `FieldDescriptorProto` for `field` and `extension`,
`OneofDescriptorProto` for `oneof`, `EnumDescriptorProto` for `enum`,
`EnumValueDescriptorProto` for `enum-value`, `ServiceDescriptorProto`
for `service`, `MethodDescriptorProto` for `method`), `file` as the
containing file for every target but `package` and `set` (a `file`
rule's `file` being its entity), for `package` the name as `pkg` —
`package` being a reserved word of CEL — and the package's checked
files as `files`, and for `set` the checked
modules' files as `files`; a breaking run binds, on each side, only the
files of the module under check, for `package` and `set` alike; for a
breaking rule each binding in two forms in place of the one — `old` and
`new` for an entity target, `oldFile` and `newFile` beside them,
`oldPackage`, `newPackage`, `oldFiles` and `newFiles` for `package`,
`oldFiles` and `newFiles` for `set` — the absent side `null`.

**REQ-env1-library** (wire): Environment 1 MUST provide CEL's standard
functions, the `strings` and `lists` extension libraries, and exactly
these functions of pb's, each pure and each accepting `null` where a
descriptor is named by returning `null`: `comments(entity)`, a map
with `leading` and `trailing` strings and `detached` a list of
strings; `parent(entity)`, the enclosing declaration — the message for
a nested message, field, oneof, enum or extension declared in one,
the enum for a value, the service for a method, the file for a
top-level declaration, `null` for a file; `file(entity)`, the
containing file; `fullName(entity)`, the fully qualified name without
a leading dot, a file's its path; `messages(x)`, `enums(x)`, `extensions(x)` and
`services(x)`, the declarations of that kind under a file, a list of
files or a message — the receiver excluded, nested ones included,
map-entry messages excluded, in declaration order, an empty list
where the kind cannot occur; `resolve(name)`, the declaration a fully
qualified name names, with or without a leading dot — a message (a
map-entry message, the compiler's, excepted), enum, enum value,
oneof, service, method, field or extension — `null` for none; `fileByName(path)`, the file of that import path, `null` for
none; `imports(file)`, the files the file imports directly, in
declaration order; `visible(file)`, the file itself, the files it
imports, and, through public imports, transitively, in that order;
`references(file)`, the fully qualified names the file references
through field and extension types — a map field's its value's type —
method request and response types, extendees and custom option
extensions, each once in first-use order over the file's options,
then its messages (each its options, fields, oneofs, enums,
extensions, nested messages), enums, extensions and services with
their methods, each list in declaration order and a declaration's
type before its options;
`features(entity)`, the entity's resolved `google.protobuf.FeatureSet`
with the message's own fields and no extension, editions inheritance
applied and, in a proto2 or proto3 file, the syntax and a field's
modifiers expressed as the features they imply (`optional` and
oneof membership in proto3 explicit presence, `required` legacy
required, a group delimited, `packed` as written and otherwise the
syntax's default), for any entity but a package or the set; a
message-typed field reports the resolved value in every syntax, its
presence being its kind's to read; `options(entity)`,
a map from option name to value — a built-in option under its field
name, a custom option under its extension's fully qualified name in
parentheses, as the language writes it, so the two never collide; a scalar option's value the scalar, an enum
option's its name, a repeated option's a list, a message-valued
option's a map by field name — holding exactly the options set,
custom options included; `words(name)`, the identifier's
words by word segmentation; `case(name, style)`, the identifier
rebuilt from its words in `pascal`, `camel`, `snake` or `upper-snake`
— `snake` the words lower-cased and joined by `_`, `upper-snake`
upper-cased and so joined, `pascal` each word with its first letter
upper-cased and the rest lower-cased except an acronym kept whole,
`camel` as `pascal` with the first word lower-cased — so that a name
is in a style exactly when `case(name, style)` equals it; and
`packageCycles(files)`, the strongly connected components of two or
more packages in the files' package import graph — a file's import of
a file of its own package is no edge — each a list of package names
in sorted order, the components sorted by their first name.

**REQ-env1-lookup-scope** (invariant): The lookup functions MUST search
the whole compiled set, dependencies included — in a breaking run the
new side's, the old side's entities being at hand in the pair — while
`files` names the checked modules' files alone, so a rule judges the
workspace and can see through its imports.

## Breaking-change alignment

**REQ-break-pairing** (behavior): Breaking rules MUST evaluate over
entity pairs aligned by the engine: files pair by import path;
packages, messages, enums, services, methods and extensions by
fully-qualified name; fields and enum values by number within their
paired parent; oneofs by name within their paired message; an entity
present on one side only forms a pair with an absent side, and a
`set` rule evaluates once over the two sides. Rules see the pair;
rules never walk the diff.

**module under check** (term): One workspace module: `pb breaking`
runs per workspace module, each compared against a base of its own
made from the one `breaking.base` form.

**REQ-break-base** (behavior): The comparison base MUST come from the
lint file's `breaking.base`, one form of the three: a git reference,
a tagged version, or the version the lockfile pins for the module
under check.

**REQ-break-base-materialized** (behavior): The base MUST be
materialized as a module and compiled by the same compiler as the
checked schema before pairing: a git reference from the git repository
the workspace root lies in, at that reference, at the module's
directory relative to that repository's root as it lies now; a tagged
version and the pinned version as the module's own path at that
version, acquired, verified and pinned as any dependency
(`module-resolution.md`, `module-lockfile.md`). A base that cannot be
materialized — a workspace root in no git repository, a reference or
directory the repository lacks, a version the module's origin does not
serve, a module the lockfile does not pin — fails `pb breaking` naming
the form and the cause; nothing degrades to an empty base.

## Configuration and suppression

**REQ-lint-config-schema** (wire): The lint file MUST contain, each
optional: `rulesets`, a list of module paths to import rules from;
`enable` and `exclude`, lists of rule ids or tags; `severity`, a map
from rule id to override; `ignore`, a list of `{paths, rules}` entries
excluding rule ids under path globs; and `breaking`, a mapping whose
`base` is a mapping with exactly one of `ref` (a git reference),
`version` (a tagged version) and `pinned` (`true`; any other value is
a schema violation). No other keys exist at any level.

**REQ-lint-selection** (behavior): The enabled rules MUST be every
rule of every imported ruleset when `enable` is absent, or the rules
`enable` names by id or tag, less those `exclude` names, each with the
severity `severity` overrides for it; an id or tag `enable`,
`exclude` or `severity` names that no imported rule declares, and a
rule id two rule files of the imported rulesets both declare, fail
the check run naming it, since ids drive every selection, override
and suppression.

**REQ-lint-suppression** (behavior): A finding MUST be suppressed by a
line comment whose text opens with the word `pb:ignore` followed by
the rule id as its next word, anything after being the reason, on
the flagged line, or alone on the line immediately preceding it — a
trailing comment on the preceding declaration's line suppresses that
declaration's findings, never the next one's; a `//` inside a string
literal or a block comment opens no line comment, and a block comment
suppresses nothing; the rule id is mandatory, and no comment form
suppresses more than the named rule;
a finding without a position has no line to carry the comment, and
is suppressed by configuration alone.

**REQ-lint-rulesets-declared** (behavior): Each module path the lint
file's `rulesets` names MUST be a module of the build — a dependency
some workspace module declares, or a workspace module itself, whose
rule files are read from the working tree — and a path naming
neither fails the check run; a ruleset is acquired, pinned and
verified exactly as any dependency (`module-lockfile.md`,
`provenance.md`).

## Verbs

**REQ-check-lint-verb** (behavior): `pb lint`, taking no arguments,
MUST evaluate every enabled lint rule over the checked modules and
report the findings as REQ-check-findings-output says; with zero lint
rules enabled it reports that on standard error.

**REQ-check-breaking-verb** (behavior): `pb breaking`, taking no
arguments, MUST materialize each module under check's base, pair it
with that module's checked schema, evaluate every enabled breaking
rule over the pairs, and report the findings of every module as one
stream, ordered as REQ-check-findings-output says, with one exit
status; with zero breaking rules enabled it reports that on standard
error.

**REQ-check-findings-output** (wire): A check verb MUST print each
finding as one line on standard output, `path:line:column: severity
rule-id: message`, a finding in the base carrying ` [base]` after the
message, a finding without a position omitting `:line:column`, one
without a location omitting the path and its colon, in the order path,
then line and column, then rule id, then message, findings without a
location last in the same order less the path.

**REQ-check-exit-status** (behavior): A check verb MUST exit with
status 1 when any finding of severity `error` remains after
suppression, and 0 otherwise — warnings never fail the run, and zero
rules enabled is status 0.
