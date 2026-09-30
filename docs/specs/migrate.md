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
`buf.gen.yaml` (`v1` or `v2`), every generation template beside the
configuration — a file named `buf.gen.<name>.yaml`, the convention
under which a `buf generate --template` names one; a template named
otherwise is a file the verb does not know — and `buf.lock` (`v1` or
`v2`), a `v1` workspace member's beside its own `buf.yaml`. A
`buf.yaml` of `v2` declares its modules under `modules`; one of `v1`
declares one module at its directory, a `buf.work.yaml` beside it
naming several. A key with no value is a key absent, as buf's decoder
reads a null, a required key excepted.

**mapping table** (term): A fixed list pb ships, keyed by a name buf's
configuration uses, giving the pb form of that name and, where pb
carries less than buf did, what is lost. Two tables exist: the
dependency table, from a BSR module name to a module path, and the
plugin catalog, the BSR plugin names pb's catalog publishes, each
mapping to its repository under the catalog's registry by one rename
rule. A table grows by pb's own releases, never at run time.

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
module's root, the lint file always — buf checks every module under a
selection, explicit or its default, and the file spells the one buf
applied (REQ-migrate-rules) — the generation file where a
`buf.gen.yaml` lies at the directory; then run `pb dep tidy` over the result, so the written
workspace is tidy and its lockfile pinned, the configuration's
directory the resolution root. It fails, writing nothing,
where any pb file it would write already exists — the lockfile the
tidy writes at the configuration's directory among them, and one in
a module's directory below, which no workspace admits — where the directory
holds no buf configuration, or where a buf file does not parse under
its own version — a key the verb does not model is no parse failure
but an unmapped fact naming it; and it fails after writing, the files
kept and the report printed, where the comments' rewriting or the
tidy fails, naming the cause. It deletes no buf file: the buf
configuration stays as it was, the user's to remove.

**REQ-migrate-report** (behavior): The verb MUST print the migration
report — every replacement it applied among the facts — on standard
output, one line per fact: a mapped fact as `<buf key> -> <pb form>`,
an unmapped one as `<buf key> !! <reason>`, a file written as
`<path> -> written`, the lockfile the tidy wrote last, the facts in
the order of the steps (modules, dependencies, rules, generation, the
keys no step models, the files written, the comments rewritten); and
exit 0 when every fact mapped,
1 when any fact went unmapped, the files written in either case, so a
script can tell a complete migration from one needing a hand and a
second run with replacements can finish it.

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
`workspace.md` REQ-work-emission has it; a lone module at the
directory itself writes no workspace file, and one below it a
workspace of one, the configuration's directory staying the
resolution root the lint and generation files and the output
directories are relative to, as they are to buf's. A module whose directory contains another's fails the
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
keeping or moving it as the graph selects — and so does each BSR
module the table records the name's files importing, to closure,
since an entry's origin is a synthesized module declaring nothing of
its own (`module-resolution.md` REQ-resolve-synthesis) and the
migration's declaration is what carries its needs, as the tidy then
keeps them (`dep-verbs.md` REQ-dep-tidy); a name neither holds, or
one whose discovery fails, is an unmapped fact naming the BSR module
and the flag's form that supplies what is missing, a failure's reason
on the fact's one line. A module path is declared at one version: a
replacement's version applies to every name reaching its path, and two
replacements reaching one path at different versions fail the verb
naming both. The `buf.lock` entry for a dependency, its BSR commit and
digest, is reported as unmapped: a BSR commit names no git commit, and
pb's pin is the lockfile's own, made by the tidy. The dependency table
names, for each entry, the module path, whose resolution splits the
repository from the subtree that is the BSR module's root, and the
BSR modules the entry's files import, and holds:

| BSR name | module path | imports |
|---|---|---|
| `buf.build/bufbuild/protovalidate` | `github.com/bufbuild/protovalidate/proto/protovalidate` | |
| `buf.build/cncf/xds` | `github.com/cncf/xds` | `buf.build/envoyproxy/protoc-gen-validate` `buf.build/google/cel-spec` `buf.build/googleapis/googleapis` |
| `buf.build/envoyproxy/envoy` | `github.com/envoyproxy/envoy/api` | `buf.build/cncf/xds` `buf.build/envoyproxy/protoc-gen-validate` `buf.build/googleapis/googleapis` `buf.build/opencensus/opencensus` `buf.build/opentelemetry/opentelemetry` `buf.build/prometheus/client-model` |
| `buf.build/envoyproxy/protoc-gen-validate` | `github.com/bufbuild/protoc-gen-validate` | |
| `buf.build/google/cel-spec` | `github.com/google/cel-spec/proto` | `buf.build/googleapis/googleapis` |
| `buf.build/googleapis/googleapis` | `github.com/googleapis/googleapis` | |
| `buf.build/grpc-ecosystem/grpc-gateway` | `github.com/grpc-ecosystem/grpc-gateway` | `buf.build/googleapis/googleapis` |
| `buf.build/grpc/grpc` | `github.com/grpc/grpc-proto` | `buf.build/googleapis/googleapis` |
| `buf.build/opencensus/opencensus` | `github.com/census-instrumentation/opencensus-proto/src` | |
| `buf.build/opentelemetry/opentelemetry` | `github.com/open-telemetry/opentelemetry-proto` | |
| `buf.build/prometheus/client-model` | `github.com/prometheus/client_model` | |

An entry enters the table with its layout verified: every file of
the module resolves its imports from the named root — within the
module, the entries it imports mapped through the table, or the
well-known imports — and a file buf's users import, at the path they
import it by, compiles; the layout test the migrate package carries
(TestDependencyLayouts) is run against the origins at entry. Every
file must resolve its imports because a build checks every file of
every module in its build list for them
(`module-resolution.md` REQ-resolve-unsatisfied-imports); the
module's files are not required to compile as one set because a
build compiles what a user's files import, never a dependency whole,
and a repository may hold files no one compiles together, as
googleapis holds a `preview` tree redeclaring its packages.

`buf.build/gogo/protobuf` has no entry: its repository root holds
generator test protos whose imports resolve from no root, and
`gogoproto/gogo.proto` is imported by a path that pins the module root
there, so no layout pb can name has every file resolving its imports.

## Lint and breaking

**REQ-migrate-rules** (behavior): buf's `lint` and `breaking`
sections MUST become one lint file importing the ruleset
`github.com/greatliontech/buf-rules` — declared as a dependency of
every module the configuration declares, tidy keeping it
(`dep-verbs.md` REQ-dep-tidy-rulesets) — spelling the selection buf
applies to each module: `use` of either section joined into
`enable`, each entry a buf category or rule id spelled as the
ruleset's qualified tag or rule name, `DEFAULT` read as `STANDARD`,
and a section absent or its `use` empty buf's default for the kind —
`DEFAULT` for `v1` lint, `STANDARD` for `v2`, `FILE` for breaking — a
mapped fact naming it; `except` of either section joined into
`exclude` the same way; each `ignore` path, a file or a directory, an
`ignore` entry over the glob `<path>/**`, which matches the path and
everything under it, module-relative, naming no rule and the
section's kind, as buf's ignore excludes that kind alone; each
`ignore_only` entry an `ignore` entry over its paths naming its rule,
or every rule a category tags — reaching what the lint file's
location rule places under its paths (`check-rules.md`
REQ-rules-finding-location): a `set` rule's finding, having no path
under the root selection, or a `set` or `package` rule's, located at
a module's directory under its entry, is reached by an `ignore_only`
over the module's directory alone, and one over any other path
naming such a rule is an unmapped fact naming it; an `ignore` path equal to a module's
directory, which buf reads as disabling the kind for that module, no
rule of the kind enabled for it, a mapped fact saying so — a module
every kind is disabled for enabling nothing, its `enable` spelled
`[]` — and such a path under `ignore_only` every file of the module
for the rule. A buf id neither section's category nor the
ruleset declares — `PROTOVALIDATE`, `FILE_SAME_PHP_GENERIC_SERVICES`,
a deprecated `v1` category — is an unmapped fact naming it. The lint
file's root selection is what buf applies to a module declaring no
section of its own: a `v1` or `v2` file's top-level sections, buf's
defaults for a `v1` workspace's directories; where the configuration
declares several modules, a module whose own sections — a `v2`
module's, a `v1` directory's file's — or the top-level ignores lying
within it give it a selection or ignores of its own has an entry in
the lint file's `modules` map (`check-rules.md` REQ-lint-config-schema)
carrying its whole selection of both kinds and its ignores, and a
module whose selection is the root's with no ignores has none; a lone
module's selection and ignores are the root's. A `v2` file's paths
are relative to the file, a module's own section's required within
the module — one outside does not parse, as buf refuses it — and the
top-level section's assigned to the module holding each, one in no
module an unmapped fact, as buf skips it; a `v1` file's paths are
relative to its module.

**REQ-migrate-rule-options** (behavior): buf's rule-shaping options
MUST be unmapped facts where set to anything but their default —
`enum_zero_value_suffix` (`_UNSPECIFIED`), `service_suffix`
(`Service`), `rpc_allow_same_request_response`,
`rpc_allow_google_protobuf_empty_requests`,
`rpc_allow_google_protobuf_empty_responses`,
`ignore_unstable_packages` (each `false`) and buf's own check
plugins — each naming the option and the rule it
would have reshaped: a pb rule has no parameters, so an option's
meaning lives in a rule of one's own, which the report says; a key
spelled at its default is no fact. `disallow_comment_ignores` and
`v1`'s `allow_comment_ignores` are mapped facts deciding whether a
module's suppression comments are rewritten (REQ-migrate-comments),
`v1`'s absent switch, under which buf honored none, a fact all the
same.

**REQ-migrate-comments** (behavior): buf's suppression comments MUST
be rewritten in place as buf read them. buf honors
`buf:lint:ignore <ID>` on any line of the comment block leading an
element — the comment-only lines directly above it, block comments
among them, each line's text trimmed — for a module whose lint
section honors comment ignores (`v1` with `allow_comment_ignores`
true, `v2` without `disallow_comment_ignores` true), and no other
directive: none for breaking, none trailing a line of code, none
parted from the element by a blank line. In every regular proto file
under the root of such a module — the module's file set being every
regular file, a symbolic link carried as one and never followed —
each `//` comment on a leading block's line whose text opens
with `buf:lint:ignore` and an id is rewritten to `// pb:ignore <ID>`,
the id and any trailing text kept, the file otherwise byte-for-byte
as it was, and the rewrite is reported per file with its count; a
rewritten directive on a line pb does not read — a block inside a
declaration continued from the line before, leading a statement
declaring no entity but the file's — an `option`, `reserved`,
`extensions`, `import`, `package`, `syntax` or `edition` — or a
body's closing brace, on whose line no finding sits, or a `file`
rule's anywhere but the block leading the file's first line of
code, pb reading the comment block leading the flagged line, a
finding's line being its declaration's first and a file's its first
lexical element's (`check-rules.md` REQ-lint-suppression,
REQ-rules-finding-location) — a rewritten
directive naming a rule whose finding carries no position, a
`package` or `set` rule, which no comment suppresses wherever it
stands, and a directive in a block comment, which pb reads not, are
each an unmapped fact naming the file and the line, one fact per
directive; every other directive is left as it
was, buf having honored none — one parted from its id by anything
but one space among them, as buf reads the form. A module whose lint section honors none keeps
its comments as they are, a mapped fact saying so
(REQ-migrate-rule-options). A bare id suffices, the migrated lint
file importing one ruleset.

## Generation

**REQ-migrate-gen** (behavior): A `buf.gen.yaml` MUST become the
generation file: each plugin entry naming a BSR plugin (`remote` in
`v2`; in `v1`, `plugin` spelled as buf's plugin reference,
`remote/owner/plugin` with a `:version` or none) looked up among the
replacements and then in the plugin catalog, a name either holds
becoming a `ref` entry at the reference given for that name and
version — the replacement's as given; the catalog's repository for
the name, `ghcr.io/greatliontech/pb-plugins/<owner>/<plugin>` for
`buf.build/<owner>/<plugin>`, tagged with the version exactly as buf
spells it, the catalog publishing buf's versions under buf's tags (a
version the catalog has not published surfaces at the first
generate, the tag absent, never at the migration, which carries no
versions; a version no tag can spell — buf admits a build suffix's
`+`, which a tag cannot carry — an unmapped fact naming the flag's
form), or, where none is named, with the highest version tag the
registry lists for the repository — a tag of `v` and dot-separated
decimal numbers with no leading zero, so one number has one
spelling, the highest by number component by component, a tag equal
so far but shorter the lesser — listed once
per name, a mapped fact naming the choice, a listing that fails or
holds no version tag an unmapped fact naming the flag's form — a
name neither holds an unmapped fact naming the plugin and the flag's
form; a
`local` entry becoming a `local` entry of its command and arguments,
the scalar form where it has none, where pb's schema takes the
command (`generation.md` REQ-gen-schema), an unmapped fact naming it
where not, as buf takes any text — a `v1` entry's `path` its
command, and its bare `plugin` or `name` the executable
`protoc-gen-<name>`, as buf ran them; a `protoc_builtin` entry — in
`v1`, a name among protoc's builtins or any entry with `protoc_path` —
an unmapped fact, pb running no protoc; `out` as written where it is
a clean relative path within the resolution root (`generation.md`
REQ-gen-schema), the entry an unmapped fact naming it where not, as
buf accepts what pb writes nowhere; `opt`, a string or a list, joined
with commas as buf hands it to the plugin; `include_imports` as
itself (`generation.md` REQ-gen-schema); `clean` as itself, one for
the generation file. `v2`'s `inputs` become the file's entries'
`files` patterns: each `directory` input's `paths`, root-relative
and within the input's directory as buf requires, a path under one
workspace module becoming that module-relative path's pattern — the
file's own, a directory's with everything under it — a mapped fact
naming it, every entry of the file carrying the patterns; a path
under no module, one naming a whole module among several (a
module-relative pattern names every module), one that exists
nowhere, or one whose module-relative path exists under another
module too, is an unmapped fact naming it; a directory input naming
no paths is read as a path naming its directory, the root leaving
every workspace file a target; an `exclude_paths` entry, an input of
any other kind, and a directory outside the root, an unmapped fact.
`strategy`, `revision`, `protoc_path`, `include_wkt` — pb generates
for no well-known file — and `types` and `exclude_types`, an input's
or `v1`'s, are unmapped facts naming the key: pb generates over the
workspace's own files under one strategy. A generation template
beside the configuration is read as the generation file is, whether
or not a `buf.gen.yaml` lies there — one that does not parse fails
the verb naming it — its entries following the file's in the
template files' name order, its inputs its own entries' patterns,
its facts keyed by its name; pb's overrides being one set over every entry and its `clean` one
for every output directory, a template's managed mode is read as
the file's is, its entries the template's own facts, and is a
mapped fact where it gives the overrides the first file's gives —
none where the first gives none, its entries then running under the
first file's — and an unmapped fact where not, a template without
one among them where the first file's gives any; its `clean` is an
unmapped fact where it differs from the first file's, the first
file being `buf.gen.yaml` where present. An entry buf itself refuses does
not parse under its version: no naming form, two, or an empty one; no
`out`; a `v1` `name` spelled as a reference; a key its form takes no
meaning from — `strategy` or `protoc_path` beside a remote plugin and,
in `v1`, `path` there; in `v2`, `revision` or `protoc_path` beside a
local plugin and `revision` beside a builtin. buf's `managed` mode
becomes `overrides` where an entry is declarative — a `file_option`
with a `value` over every file or, in `v2`, over the files a `path`
scope names: buf matches the path against a file's path relative to
its module, the file's own or a dependency's alike, so the glob is
the path and everything under it, over module-relative paths as
pb's globs are (`generation.md` REQ-gen-schema); in `v1`, a boolean
option, the `override` map of option to file to value, the file's
path read the same way, and the `default` of `optimize_for`,
`objc_class_prefix` and `swift_prefix` for every file — and an unmapped
fact where it is
buf's own heuristic or names a module, which a module-relative glob
cannot: `enabled`, for buf's defaults (the values it computes per
file for every option no rule of the file names), a `field_option`, a
`disable` entry, a `module` scope, a form's `except` and its
override per module, or a form pb would have to compute a value from
per package with no declared rule (`v1`'s `csharp_namespace` and
`ruby_package`). A prefix or suffix option — `v2`'s
`go_package_prefix`, `java_package_prefix`, `java_package_suffix`,
`csharp_namespace_prefix`, `php_metadata_namespace_suffix` and
`ruby_package_suffix`, and `v1`'s `go_package_prefix` and
`java_package_prefix` defaults, over every file — becomes derived
overrides (`generation.md` REQ-gen-overrides-derived) of the option
it prefixes or suffixes reproducing buf's reading, which keeps per
file and option the prefix and the suffix the rules matching the
file set in order, a prefix rule keeping the suffix and a suffix
rule the prefix, a value rule clearing both, and `java_package`
starting from the prefix `com`: the rule's overrides carry its
prefix or suffix and the other axis of the state the files had — one
over the rule's scope from buf's starting state, where no earlier
override of the option covers the scope whole, then one over the
intersection with each earlier override of the option, in their
order, carrying that override's other axis — and an override a later
one of the option covers whole is dropped; a rule with an empty value,
clearing what earlier rules set, is unmapped. In `v1` the rules hold buf's order, whatever the
document's: the booleans, then the forms, then the per-file
`override` map by option key as written and then by file path, in
byte order — the map having the last word. `managed` with `enabled` false
is read for nothing, a mapped fact saying so, as buf reads it. Where
no plugin entry maps, no generation file is written and the managed
mode is read for nothing, an unmapped fact for each. A
`v2` override naming no option or both, an option buf does not know,
no `value`, or scoping a `file_option` to a `field`, a `disable` entry
naming nothing, both options, an option buf does not know, or a
`file_option` with a `field`, and a `v1` `override` map keyed by an
option buf does not know, are buf's own refusals and do not parse.
`v1`'s alpha `remote` plugin key is one the verb does not model, an
unmapped fact naming it. The plugin catalog is the list of names pb
carries, a copy of the names the catalog repository
(greatliontech/pb-plugins, its `catalog.yaml`) publishes under
`ghcr.io/greatliontech/pb-plugins`:

- `buf.build/bufbuild/es`
- `buf.build/connectrpc/es`
- `buf.build/connectrpc/go`
- `buf.build/grpc/csharp`
- `buf.build/grpc/go`
- `buf.build/grpc/web`
- `buf.build/protocolbuffers/csharp`
- `buf.build/protocolbuffers/go`
- `buf.build/protocolbuffers/js`

The copy is held to the repository's catalog and the registry by the
migrate package's live test (TestPluginCatalog), run at each entry's
addition: the names equal the catalog's at the commit the test pins,
and each name's highest published tag names a list the registry
answers, signed under the catalog's identity. The catalog signs every
list and image keyless under its publish workflow's identity, the
subject `https://github.com/greatliontech/pb-plugins/.github/workflows/publish.yaml@refs/heads/main`
issued by `https://token.actions.githubusercontent.com`; a trust
policy accepts the catalog's images with a `plugins` rule of prefix
`ghcr.io/greatliontech/pb-plugins/` naming that `san` and `issuer`
(`provenance.md` REQ-prov-trust-schema, REQ-prov-plugin-identity: a
plugin image has no default identity, so without the rule its
evidence is never judged).

## What the verb never does

**REQ-migrate-no-network-but-tidy** (invariant): The verb MUST reach
no network but through the discovery of its declarations' versions
(REQ-migrate-deps), the listing of a versionless plugin's tags
(REQ-migrate-gen) and the tidy that ends it: the tables are pb's
own, a BSR is never consulted, and no buf file is fetched.

**REQ-migrate-no-heuristic** (invariant): The verb MUST synthesize no
value it was not given: a module path, a version, an option value or
a plugin reference comes from the command line, the buf file, the
repository's `origin` remote, the tables, the registry's tag listing,
the ruleset or resolution, never from a guess — buf's defaults and pb's own file schemas being
this document's constants, given by it; where none supplies it, the
fact is unmapped. The tables and the replacements are the whole of
the verb's knowledge of buf's names: pb reads no buf file after the
verb ends and keeps no record of the migration but the report.
