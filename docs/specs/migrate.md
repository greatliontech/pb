# pb — migrate

`pb migrate` is a one-shot import of a buf configuration into pb's:
it reads buf's files as they lie, writes pb's beside them, and prints
what it mapped and what it could not, with the reason. It is a table,
not a compatibility layer: pb never reads a buf file at any other
time, never emulates buf at run time, and forgets where a migrated
configuration came from once the verb ends. Every file it writes is
one another document defines — the module file (`module-file.md`),
the workspace file (`workspace.md`), the lint file (`check-rules.md`),
the generation file (`generation.md`) — and every obligation those
documents place on such a file holds for a migrated one unchanged.

**buf configuration** (term): The files buf reads at a directory:
`buf.yaml` (version `v1` or `v2`), `buf.work.yaml` (`v1`),
`buf.gen.yaml` (`v1` or `v2`) and `buf.lock` (`v1` or `v2`). A
`buf.yaml` of `v2` declares its modules under `modules`; one of `v1`
declares one module at its directory, a `buf.work.yaml` beside it
naming several. A key with no value is a key absent, as buf's decoder
reads a null, a required key excepted.

**mapping table** (term): A fixed list pb ships, keyed by a name buf's
configuration uses, giving the pb form of that name and, where pb
carries less than buf did, what is lost. Two tables exist: the
dependency table, from a BSR module name to a module path, and the
plugin table, from a BSR plugin name to a plugin reference. A table
grows by pb's own releases, never at run time.

**migration report** (term): What the verb prints: every buf fact
mapped, the pb form it took; every fact it could not map, with its
reason; every file written. The report is the whole of what the verb
says, on standard output, one line per fact.

**unmapped fact** (term): A buf setting the verb carries over to no
pb form: a name absent from a table, a lint option that reshapes a
rule's meaning, a breaking option pb has no counterpart for, a plugin
form pb does not run. An unmapped fact is reported, never silently
dropped and never contorted into a pb form that means something else.

## Invocation

**replacement** (term): A mapping given on the command line for one
run: `--dep <BSR module name>=<module path>[@<version>]` or `--plugin
<BSR plugin name>=<plugin reference>`, each repeatable, a dependency's
version, where given, declared as written with nothing discovered,
naming what the tables lack or what the user wants elsewhere; a
replacement naming what a table holds wins over the table. A
replacement lives in the invocation alone — nothing of it is written,
since a migration runs once — and one naming what the configuration
never declares fails the verb naming it.

**REQ-migrate-verb** (behavior): `pb migrate` MUST run at a directory
holding a buf configuration — a `buf.yaml`, or a `buf.work.yaml`
naming directories — taking `--module <path>`, the pb path of the
directory the configuration lies at (REQ-migrate-modules), and any
replacements, and write pb's files beside it: the workspace file where
the configuration names several modules, a module file at each
module's root, the lint file where buf's `lint` or `breaking` sections
carry anything, the generation file where a `buf.gen.yaml` lies at the
directory; then run `pb dep tidy` over the result, so the written
workspace is tidy and its lockfile pinned. It fails, writing nothing,
where any pb file it would write already exists, where the directory
holds no buf configuration, or where a buf file does not parse under
its own version — a key the verb does not model is no parse failure
but an unmapped fact naming it; and it fails after writing, the files
kept, where tidy fails, naming tidy's cause. It deletes no buf file:
the buf configuration stays as it was, the user's to remove.

**REQ-migrate-report** (behavior): The verb MUST print the migration
report — every replacement it applied among the facts — and exit 0
when every fact mapped, 1 when any fact went unmapped, the files
written in either case, so a script can tell a complete migration
from one needing a hand and a second run with replacements can
finish it.

## Modules and the workspace

**REQ-migrate-modules** (behavior): Each module buf's configuration
declares MUST become a module file at that module's root declaring the
module's pb path: the pb path of the configuration's directory joined
with the module's directory relative to it, cleaned, the directory
itself joining nothing. The configuration's path is what `--module`
gives or, absent the flag, the git origin of the repository the
directory lies in — found as `check-rules.md`
REQ-break-base-materialized finds it — spelled as a module path
(`module-resolution.md` REQ-resolve-path-syntax) and joined with the
directory's path within the repository. The spelling: the `origin`
remote's first URL, its host in lower case and its path, the scheme,
the user, a leading or trailing slash and a trailing `.git` dropped,
`https://github.com/o/r.git`, `ssh://git@github.com/o/r` and git's
`git@github.com:o/r.git` each spelling `github.com/o/r`; a URL with a
port, a query or a fragment, or one spelling no module path, spells
none. A directory in no repository, a repository with no `origin`, or
a URL spelling none, each with no path given, fails naming the flag.
buf's `name` for the module (a BSR name) is reported, never used as
the path: a BSR name is no place pb fetches from. A `v2`
configuration's `modules` and a `v1` workspace's `directories` become
the workspace file's `use` entries, each module's directory cleaned as
`workspace.md` REQ-work-emission has it; a lone module writes no
workspace file. A module whose directory contains another's fails the
verb naming both: the verb authors no module file the archive refuses,
and a module file beneath a module's root makes that module
unpublishable (`module-archive.md` REQ-archive-nested-module),
whatever a hand-written workspace may declare (`workspace.md`, the
workspace module term). A `v1` workspace's directories each carry
their own `buf.yaml`, the module's name, dependencies and sections
read from it, a directory holding none a module under buf's default
`v1` configuration with nothing to report; a directory whose
`buf.yaml` is not `v1` does not parse, as buf refuses it. buf's
`excludes` and `includes` under a module, and `v1`'s `build.excludes`,
are unmapped facts: a pb module's file set is every regular file under
its root (`module-archive.md` REQ-archive-file-set). A `buf.yaml` or
`buf.work.yaml` buf itself refuses — a module directory, module name,
dependency (its `:ref` aside) or workspace directory listed twice, a
workspace directory that is the configuration's own or contains
another — does not parse under its version. A `buf.work.yaml` beside a
`v2` `buf.yaml` is two workspaces at once, which buf refuses: the verb
fails naming both files.

## Dependencies

**REQ-migrate-deps** (behavior): Each entry of buf's `deps` MUST be
looked up among the replacements and then in the dependency table by
its BSR name, the `:ref` suffix aside: a name either holds becomes a
declared dependency of every module the configuration declares, at the
replacement's version where it gives one, else at the version
discovery names for the path — through a proxy its `@latest`
(`module-proxy.md`), through the origin the highest release tag in the
module's namespace or, none existing, the pseudo-version of its
default-branch head (`module-resolution.md`
REQ-resolve-synthesized-tags) — the tidy that ends the verb then
keeping or moving it as the graph selects; a name neither holds, or
one whose discovery fails, is an unmapped fact naming the BSR module
and the flag's form that supplies what is missing, a failure's reason
on the fact's one line. A module path is declared at one version: a
replacement's version applies to every name reaching its path, and two
replacements reaching one path at different versions fail the verb
naming both. The `buf.lock` entry for a dependency, its BSR commit and
digest, is reported as unmapped: a BSR commit names no git commit, and
pb's pin is the lockfile's own, made by the tidy. The dependency table
names, for each entry, the module path, whose resolution splits the
repository from the subtree that is the BSR module's root, and holds:

| BSR name | module path |
|---|---|
| `buf.build/opentelemetry/opentelemetry` | `github.com/open-telemetry/opentelemetry-proto` |
| `buf.build/prometheus/client-model` | `github.com/prometheus/client_model` |

An entry enters the table with its layout verified: the module's own
files compile from the named root, at the import paths the BSR served
them at, the layout test the migrate package carries
(TestDependencyLayouts) run against the origins at entry.

`buf.build/gogo/protobuf` has no entry: its repository root holds
generator test protos that compile from no root, and
`gogoproto/gogo.proto` is imported by a path that pins the module root
there, so no layout pb can name compiles as a set.

## Lint and breaking

**REQ-migrate-rules** (behavior): buf's `lint` and `breaking`
sections MUST become one lint file importing the ruleset
`github.com/greatliontech/buf-rules` — declared as a dependency of
every module the configuration declares, tidy keeping it
(`dep-verbs.md` REQ-dep-tidy-rulesets) — with: `use` of either
section joined into `enable`, each entry a buf category or rule id
spelled as the ruleset's qualified tag or rule name, `DEFAULT` read
as `STANDARD`; `except` of either section joined into `exclude` the
same way; each `ignore` path, a file or a directory, an `ignore`
entry over the glob `<path>` or `<path>/**` naming no rule; each
`ignore_only` entry an `ignore` entry over its paths naming its rule.
A buf id neither section's category nor the ruleset declares —
`PROTOVALIDATE`, `FILE_SAME_PHP_GENERIC_SERVICES` — is an unmapped
fact naming it. A `v2` configuration's top-level sections become the
lint file's root selection and a module's own sections its entry in
the lint file's `modules` map (`check-rules.md`
REQ-lint-config-schema), `enable`, `exclude` and `severity` per
module; `ignore` and
`ignore_only` of a module join the root's ignores as written, their
paths module-relative as pb's finding paths are.

**REQ-migrate-rule-options** (behavior): buf's rule-shaping options
MUST be unmapped facts where set to anything but their default —
`enum_zero_value_suffix`, `service_suffix`,
`rpc_allow_same_request_response`,
`rpc_allow_google_protobuf_empty_requests`,
`rpc_allow_google_protobuf_empty_responses`,
`ignore_unstable_packages`, `disable_builtin` and buf's own check
plugins — each naming the option and the rule it would have
reshaped: a pb rule has no parameters, so an option's meaning lives
in a rule of one's own, which the report says. `disallow_comment_ignores`
and `v1`'s `allow_comment_ignores` are unmapped naming the pb comment
form, `pb:ignore`, which is always honored.

**REQ-migrate-comments** (behavior): Every buf suppression comment in
a checked file — `// buf:lint:ignore <ID>` and
`// buf:breaking:ignore <ID>`, on the flagged line or the line before
as buf reads them — MUST be rewritten in place to `// pb:ignore <ID>`,
any trailing text kept as the reason, the file otherwise byte-for-byte
as it was; the rewrite is reported per file with its count. A bare id
suffices, the migrated lint file importing one ruleset
(`check-rules.md` REQ-lint-suppression).

## Generation

**REQ-migrate-gen** (behavior): A `buf.gen.yaml` MUST become the
generation file: each plugin entry naming a BSR plugin (`remote` in
`v2`; in `v1`, `plugin` spelled as buf's plugin reference,
`remote/owner/plugin` with a `:version` or none) looked up among the
replacements and then in the plugin table, a name either holds
becoming a `ref` entry at the reference given for that name and
version, a name neither holds an unmapped fact naming the plugin; a
`local` entry naming one executable becoming a `local` entry, one
naming a command with arguments an unmapped fact — a `v1` entry's
`path` its command, and its bare `plugin` or `name` the executable
`protoc-gen-<name>`, as buf ran them; a `protoc_builtin` entry — in
`v1`, a name among protoc's builtins or any entry with `protoc_path` —
an unmapped fact, pb running no protoc; `out` as written; `opt`, a
string or a list, joined with commas as buf hands it to the plugin.
`strategy`, `revision`, `protoc_path`, `include_imports`,
`include_wkt`, `types`, `exclude_types` and `inputs` are unmapped
facts naming the key: pb generates over the workspace's own files
under one strategy (`generation.md`). An entry buf itself refuses does
not parse under its version: no naming form, two, or an empty one; no
`out`; a `v1` `name` spelled as a reference; a key its form takes no
meaning from — `strategy` or `protoc_path` beside a remote plugin and,
in `v1`, `path` there; in `v2`, `revision` or `protoc_path` beside a
local plugin and `revision` beside a builtin. buf's `managed` mode
becomes `overrides` where an entry is declarative — a `file_option`
with a `value` and, in `v2`, a `path` or `module` scope, over the
files that scope names; in `v1`, a boolean option, the `override` map
of option to file to value, and `optimize_for`'s default for every
file and its override per module — and an unmapped fact where it is
buf's own heuristic: `enabled` with no explicit override, a
`field_option`, a `disable` entry, or a form pb would have to compute
a value from — a prefix or suffix (`go_package_prefix`,
`java_package_prefix`, `java_package_suffix`), `v1`'s per-package
forms of default, except and override (`objc_class_prefix`,
`csharp_namespace`, `ruby_package`) and `optimize_for`'s `except`. A
`v2` override naming no option or both, an option buf does not know,
no `value`, or scoping a `file_option` to a `field`, a `disable` entry
naming nothing, both options, an option buf does not know, or a
`file_option` with a `field`, and a `v1` `override` map keyed by an
option buf does not know, are buf's own refusals and do not parse.
`v1`'s alpha `remote` plugin key is one the verb does not model, an
unmapped fact naming it. The plugin table's entries in pb's first
release of the verb are the plugins greatliontech's fork of buf's
plugin repository builds; a plugin an author publishes under their own
registry enters the table by a pull request naming the reference.

## What the verb never does

**REQ-migrate-no-network-but-tidy** (invariant): The verb MUST reach
no network but through the discovery of its declarations' versions
(REQ-migrate-deps) and the tidy that ends it: the tables are pb's own,
a BSR is never consulted, and no buf file is fetched.

**REQ-migrate-no-heuristic** (invariant): The verb MUST synthesize no
value it was not given: a module path, a version, an option value or
a plugin reference comes from the command line, the buf file, the
tables or resolution, never from a guess; where none supplies it, the
fact is unmapped. The tables and the replacements are the whole of
the verb's knowledge of buf's names: pb reads no buf file after the
verb ends and keeps no record of the migration but the report.
