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

**rule name** (term): The canonical identifier of an imported rule,
`<module path>:<id>` — the ruleset's module path, a colon, the id the
rule file declares — so two rulesets may declare one id and a fork
needs no edit; a tag qualifies the same way. A bare id or tag names
the rule or tag exactly one imported ruleset declares, and is an
error naming the candidates where several do.

**rule file** (term): A YAML file within a ruleset declaring rules and
the CEL environment version they target.

**CEL environment** (term): The contract a rule evaluates in: the
bound variables, the descriptor types, pb's library of CEL functions
(navigation, naming, comments, source info) and the CEL extension
libraries the environment's section names, each at the version named
there. Identified by an integer, its version, which names a meaning:
under one number the environment only grows — a function, an
overload or an extension library may be added when the addition
leaves every expression that compiled before evaluating as it did,
an expression whose argument type is known only at evaluation
included, where a new overload can answer a call that had no overload
to answer it; a rule naming a function the engine lacks fails at
compile time with the function's name (REQ-rules-compile). A change
to what an expression means, a removal, an addition that does not
leave every earlier expression as it was, or an extension library
moved to a version under which an expression that compiled before
evaluates differently, is the next number.

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
ids are unique within the file, and no id or tag holds a colon, the
rule name's separator. No other keys exist.

**REQ-rules-compile** (behavior): A rule whose expression does not
compile under the environment it targets — an unknown function or
variable, a type error, a kind or target the environment refuses, or
a result that is not `bool` — MUST fail the check run, naming the
rule's name and the compiler's cause, an unknown function or variable
named in it.

**REQ-rules-eval** (behavior): A rule whose evaluation fails — a
runtime error, the cost limit exceeded, or a fault the evaluator
cannot attribute, a select of a value the environment puts beyond
reach among its causes — MUST fail the check run naming the rule's
name and the cause, an unattributed fault named as the environment's
own failure to answer, the fault's text following the name.

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
exactly one finding carrying the rule name, severity, message, and the
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
a position; for a `set` rule, no location — unless the rule is
enabled by a module's own selection, when a `set` finding, and a
`package` finding likewise, is located at the module's directory
without a position, so two selections' findings of one rule tell
apart.

## CEL environment 1

The first environment. Every rule file targeting it evaluates over
these bindings and this library alone, an addition under this number
being an edit to this section and to the code in one change.
INV-env1-library-names: pb's function names are the ones this
section lists; enforced by `env1.TestChargedFunctions`.

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
functions, CEL's optional values, cel-go's extension libraries at
these versions — `strings` 5, `lists` 3, `math` 3, `encoders` 1,
`bindings` 1, `sets` 0, `two-variable comprehensions` 0, `protos` 0,
`regex` 0 — and exactly these functions of pb's, each pure and each accepting `null` where a
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
with the message's own fields, editions inheritance applied and, in
a proto2 or proto3 file, the syntax and a field's modifiers
expressed as the features they imply (`optional` and oneof
membership in proto3 explicit presence, `required` legacy required,
a group delimited, `packed` as written and otherwise the syntax's
default), for any entity but a package or the set — a message-typed
field reports the resolved value in every syntax, its presence
being its kind's to read — and every language feature as its
extension of `FeatureSet`, resolved through the same chain and, in
a proto2 or proto3 file, at the edition's default: each extension
the checked schema declares — in a breaking run the new side's, a
feature the old side alone declared being one the schema no longer
has — and the standard ones protoc ships (`(pb.java)`, `(pb.cpp)`,
`(pb.go)`) whether or not a file imports them, read as
`proto.getExt(features(entity), pb.java)` and their enums by name,
as a custom option reads as `proto.getExt(entity.options, pkg.ext)`
and a message-valued option's own fields by name, an extension
within such a value as `proto.getExt(proto.getExt(entity.options,
pkg.ext), pkg.deep)` — every option value the environment's family's
own, on both sides of a breaking run, the family the new side's: an
extension the old side alone declared stays unread, as a feature it
alone declared does, and an old value the new side's declarations
cannot decode keeps the compiler's own family, its extensions within
beyond reach as before; `syntax(file)`, the syntax the file declares — `proto2`, `proto3` or
`editions` — or the empty string where it declares none, since a
descriptor spells no proto2 and a declaration is a source fact: a
declaration the file's source information does not carry counts as
none;
`options(entity)`,
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
`dir(file)`, the directory of the file's path, empty at the root;
`unique(list)`, the list's distinct members in first-seen order, two
members one under CEL's equality — `1`, `1u` and `1.0` one member, a
message one with another of its content, a list or map one with
another of its members — a member of a kind with no equality
refused; and
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
paired parent — where either side holds several values of one enum
at one number, aliases, the values at that number by name among
them; oneofs by name within their paired message; an entity
present on one side only forms a pair with an absent side, and a
`set` rule evaluates once over the two sides. Rules see the pair;
rules never walk the diff.

**module under check** (term): One workspace module: `pb breaking`
runs per workspace module, each compared against a base of its own
made from the one `breaking.base` form; a workspace module holding no
protobuf files has nothing to pair and needs no base.

**REQ-break-base** (behavior): The comparison base MUST come from the
lint file's `breaking.base`, one form of the three: a git reference,
a tagged version, or the version the lockfile pins for the module
under check — the highest, where the pin store holds the module at
more than one.

**REQ-break-base-materialized** (behavior): The base MUST be
materialized as a module and compiled by the same compiler as the
checked schema before pairing — its files in place of the module
under check's, the build's other modules resolving its imports: a
git reference from the git repository the workspace root lies in —
the root or its nearest ancestor holding a `.git` entry, the
filesystem's root searched last, the search never entering a
directory `GIT_CEILING_DIRECTORIES` names, the variable read as git
reads it: absolute entries alone, the filesystem's root among the
dropped, each naming the directory it resolves to until an empty
entry, after which an entry names its spelling alone — at that
reference, at the module's directory relative to
that repository's root as it lies now, a nested module's directory — one
holding a module file at that reference — excluded as the working
tree's walk excludes one; a tagged version and the pinned
version as the module's own path at that version, acquired, verified
and pinned as any dependency (`module-resolution.md`,
`module-lockfile.md`). A base that cannot be
materialized — a workspace root in no git repository, a reference or
directory the repository lacks, a version the module's origin does not
serve, a module the lockfile does not pin — fails `pb breaking` naming
the form and the cause; nothing degrades to an empty base.

## Configuration and suppression

**REQ-lint-config-schema** (wire): The lint file MUST contain, each
optional: `rulesets`, a list of module paths to import rules from;
`enable` and `exclude`, lists of rule names, ids or tags; `severity`,
a map from rule name or id to override; `ignore`, a list of `{paths,
rules, kind}` entries excluding rules under path globs — `paths` one
or more globs over module-relative proto paths (the component
semantics `provenance.md` REQ-prov-trust-schema defines), `rules`
optional and non-empty when present, absent meaning every rule, `kind`
optional, `lint` or `breaking`, the entry excluding findings of that
kind alone and of either kind where absent, a finding without a path
never ignored and one located at a module's directory matched by a
glob matching the directory itself; and `breaking`, a mapping whose
`base` is a mapping with exactly one of `ref` (a git reference),
`version` (a tagged version) and `pinned` (`true`; any other value is
a schema violation); and `modules`, a map from a workspace module's
directory — the cleaned relative directory the workspace file's `use`
entry names, `.` the root itself — to a mapping of `enable`,
`exclude`, `severity` and `ignore` in the forms above, each optional.
No other keys exist at any level.

**REQ-lint-selection** (behavior): The enabled rules MUST be every
rule of every imported ruleset when `enable` is absent, or the rules
`enable` names — by rule name, by qualified tag, or by a bare id or
tag where exactly one imported ruleset declares it — less those
`exclude` names, each with the severity `severity` overrides for it,
`severity` and `ignore` naming rules alone; a `modules` entry is a
selection of its own for the files of the module at its directory —
its `enable`, `exclude` and `severity` read as the root's are, an
absent `enable` every imported rule, and its `ignore` excluding
findings of that module's files beside the root's `ignore`, which
excludes across every module — replacing the root's, which governs
every other module's files, so a rule of the `set` or `package` target
enabled at the root sees the files of the modules the root governs and
one enabled for a module that module's files alone, and a directory
naming no workspace module fails the check run naming it; a spelling
that names no imported rule or tag, a bare spelling several imported
rulesets declare, a ruleset declaring one id in two of its files or as
both an id and a tag — a ruleset's ids and tags one namespace, so a
rule name is one rule and a qualified tag one tag — and two `severity`
spellings of one rule, fail the check run naming them and, for an
ambiguous spelling, the candidates, since names drive every selection,
override and suppression.

**REQ-lint-emission** (behavior): Tooling that writes a lint file MUST
emit it canonically: UTF-8, LF line endings, two-space indentation,
the keys in the order `rulesets`, `enable`, `exclude`, `severity`,
`ignore`, `breaking`, `modules`, each absent where it holds nothing —
save `enable`, whose empty list means what its absence does not and
is spelled `[]`; `rulesets` in the order given, `enable` and `exclude`
sorted in raw-byte order, `severity` by key in raw-byte order,
`ignore` entries by their `paths` sorted in raw-byte order, then by
their `rules` so sorted, then by `kind`, each entry's keys in the
order `paths`, `rules`, `kind`, `pinned` as `true`, `modules` by directory
in raw-byte order with each entry in the same form and `{}` where it
holds nothing, a file holding nothing `{}`; a scalar plain where the
lint file's reader reads its plain spelling back as exactly that text
and no YAML schema of any version reads it as other than text — as a
number, a boolean or null, including a spelling opening with a digit,
a sign before a digit, or a dot before a digit or `inf` or `nan`, one
of the words `y`, `yes`, `n`, `no`, `on`, `off`, `true`, `false`,
`null` in any case, or a tilde; or as a merge key `<<` or a value key
`=` — and double-quoted otherwise, so a file written once reads the
same under every reader; and never a rendering the lint file's reader
rejects or reads as a different file.

**REQ-lint-suppression** (behavior): A finding MUST be suppressed by a
line comment whose text opens with the word `pb:ignore` followed, as
its next word, by the rule's name or by its bare id where one enabled
rule of the run's kind bears it (a bare id several such rules bear
fails the check run naming them and the comment's line), anything
after being the reason, on
the flagged line, or on any line of the comment block leading it —
the lines directly above it holding nothing outside comments, up to
a line of code or a line holding nothing at all, so directives stack
— a trailing comment on the preceding declaration's line suppresses
that declaration's findings, never the next one's; a `//` inside a
string literal or a block comment opens no line comment, and a block
comment suppresses nothing, though its lines keep the block whole, a
blank line inside one being the comment's; the rule's name or id is mandatory, and no comment form
suppresses more than the named rule; a finding without a position has
no line to carry the comment, and a finding in the comparison base no
working-tree line, and each is suppressed by configuration alone.

**REQ-lint-rulesets-declared** (behavior): Each module path the lint
file's `rulesets` names MUST be a module of the build — a dependency
some workspace module declares, or a workspace module itself, whose
rule files are read from the working tree — and a path naming
neither fails the check run; a ruleset is acquired, pinned and
verified exactly as any dependency (`module-lockfile.md`,
`provenance.md`).

## Verbs

**REQ-check-lint-verb** (behavior): `pb lint`, taking no arguments,
MUST evaluate every enabled lint rule over the checked modules, each
module's files under the selection governing it, and report the
findings as REQ-check-findings-output says; with zero lint rules
enabled under every selection it reports that on standard error.

**REQ-check-breaking-verb** (behavior): `pb breaking`, taking no
arguments, MUST materialize each module under check's base, pair it
with that module's checked schema, evaluate every breaking rule
enabled under the selection governing the module over the pairs —
a module with none needing no base — and report the findings of
every module as one stream, ordered as REQ-check-findings-output
says, with one exit status; with zero breaking rules enabled under
every selection it reports that on standard error.

**REQ-check-findings-output** (wire): A check verb MUST print each
finding as one line on standard output, `path:line:column: severity
rule-name: message`, a finding in the base carrying ` [base]` after the
message, a finding without a position omitting `:line:column`, one
without a location omitting the path and its colon, in the order path,
then line and column, then rule name, then message, findings without a
location last in the same order less the path.

**REQ-check-exit-status** (behavior): A check verb MUST exit with
status 1 when any finding of severity `error` remains after
suppression, and 0 otherwise — warnings never fail the run, and zero
rules enabled is status 0; a run that fails to complete exits nonzero
with its cause on standard error.
