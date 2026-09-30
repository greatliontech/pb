# pb — generation configuration

Generation compiles the workspace's modules and invokes plugins
(`plugin-execution.md`) over the resulting descriptors. Its configuration
file declares which plugins run, where output lands, and which file
options are overridden — declaratively, with no heuristics.

**generation file** (term): The file `pb.gen.yaml` at the resolution
root, declaring generation configuration.

**option override** (term): A declared assignment of a protobuf file
option (such as `go_package`) applied to matching files' descriptors
before plugins run: a value written out, or a derivation — a prefix
or suffix declared, the value spelled from it and the file's own
facts by a rule this document states, never inferred.

**generation target** (term): A file an entry's plugin generates for
— named in the request's `file_to_generate`: the workspace files the
entry's `files` patterns select, and, where the entry includes
imports, the files those reach through imports.

**REQ-gen-schema** (wire): The generation file MUST contain `plugins`,
a non-empty list of entries carrying exactly one identity-scheme key —
`ref` (a plugin reference, the `oci` scheme) or `local` (a host
command per `plugin-execution.md`: one scalar, the command alone, or a
list of one or more scalars, the command then its arguments, each
handed to the process verbatim) — plus `out`, an output directory,
optional `opt`, the plugin parameter string handed to the plugin
verbatim, optional `files`, a non-empty list of glob patterns (the
`/`-separated component semantics `provenance.md`
REQ-prov-trust-schema defines) over the workspace's module-relative
proto file paths selecting the entry's generation targets, every
workspace file where absent, and optional `include_imports`, `true` or
`false` spelled so, `false` where absent; optionally `clean`, `true`
or `false` spelled so, `false` where absent (REQ-gen-clean); and
optionally `overrides`, a list of entries `{files, option, value}`
or `{files, option, prefix, suffix}` where `files` is one such glob
pattern over module-relative proto file paths within the workspace
and its dependencies, `option` a protobuf option name in its
navigable forms — a built-in option's dotted field name, or a
parenthesized fully-qualified extension name with at most one field
selector (`(pkg.ext)`, `(pkg.ext).field`) — and `value` the option
value's spelling, or, for a derived override
(REQ-gen-overrides-derived), `prefix` and `suffix` as the option
admits them, each written non-empty, at least one written, and no
`value` key. No other top-level keys and no other entry
keys exist: there is no bare plugin-name key, and an entry with zero
or several scheme keys is a schema violation — which scheme an entry
lives in is always written, never inferred. A `ref` value is
`<registry>/<repository>:<tag>` in full: the registry is a lowercase
DNS host (no IPv6 literals, no uppercase — spellings pb declines to
normalize) naming itself unambiguously (containing a dot or a port, or
`localhost`), the repository follows the OCI distribution grammar, the
tag is written (no implicit `latest`), and no `@digest` appears — the
lockfile pins the digest. An `out` value is a clean relative path
written with forward slashes, never absolute and never escaping the
resolution root through `..`. Every scalar is recorded with its
written spelling — a value that looks numeric or boolean is still the
text the author wrote; `ref`, `local` (each element of its list form),
`out`, `files` (each pattern), `option`, `include_imports` and `clean`
are one line of text, a spelling holding a line break refused, while
`opt` and `value` are text as written, a block scalar included. Runner
selection, trust posture, and resource limits are not generation
configuration and have no keys here (`plugin-execution.md`,
`provenance.md`).

**REQ-gen-emission** (behavior): Tooling that writes a generation file
MUST emit it canonically: UTF-8, LF line endings, two-space
indentation, `clean` (absent where false) then `plugins` then
`overrides`, the last absent where it holds nothing, entries in the
order given, a `plugins` entry's keys in the order `ref` or `local`,
`out`, `opt` (absent where empty), `files` (absent where every file
is a target, as a block sequence of the patterns), `include_imports`
(absent where false) and an `overrides` entry's in the order `files`, `option`, `value` — or
`prefix` then `suffix`, each absent where empty — each
scalar spelled as `check-rules.md`
REQ-lint-emission spells a scalar — a `local` with arguments a block
sequence of them under the key, one without the scalar form, as its
list of one reads — and never a rendering the file's reader rejects
or reads as a different file.

**REQ-gen-compile** (behavior): Generation MUST compile every protobuf
file of every workspace module — a module's files being those under
its directory outside any nested module, with the module directory as
include root — resolving each import first against the well-known
imports — a module shipping a well-known path is ignored in favor of
the toolchain's copy, never an ambiguity — and then against the build
list's modules at their selected versions, each external module's
archive root (a synthesized module's subtree root,
`REQ-resolve-synthesis`; a directory replacement's directory,
`workspace.md` REQ-work-replace-dir) serving as its include root. An
import satisfied by no module fails per
`REQ-resolve-unsatisfied-imports`; an import path that more than one
module provides fails naming the path and every provider — pb never
picks a provider by heuristic. Compilation output is ordered by
workspace module in use order, then by file path, independent of
filesystem iteration.

**REQ-gen-overrides-declarative** (behavior): Option overrides MUST be
applied exactly as declared to the descriptors of matching files —
workspace and dependency files alike, never a well-known import (the
toolchain's, its options its own), matched by include-root-relative
path — before plugin invocation: entries apply in declaration order,
later entries winning on overlap; a built-in option resolves by field
name on the file options, a custom option through the compiled set's
extension declarations; scalar-kind values parse by the field's kind
(strings verbatim, `true`/`false`, enum value names, Go integer and
float syntax); a name resolving to nothing, a non-scalar target, or a
value outside the kind fails — no option value is ever synthesized
from a heuristic.

**REQ-gen-overrides-derived** (behavior): A derived override MUST
assign each matching file the value its rule spells from the
declared prefix or suffix and the file's own facts — its
module-relative path and the package it declares — and assign a
file declaring no package nothing, where the rule reads the
package; the options and their rules are exactly these, an option
outside them, or a prefix or suffix its rule does not read, being a
schema violation: `go_package` from a prefix, the prefix and the
file's directory joined as path components and cleaned (the prefix
alone, cleaned, for a file at the module root), followed by `;` and
the package name where the package's last component is a version
preceded by another component, the name being that component and
the version joined as written — a version being `v` and a number,
then nothing, `test` and any text, or an optional `p` and a number
followed by `alpha` or `beta` and an optional number, a number being
decimal digits worth at least one and at most 2147483647; `java_package` from a prefix
and/or a suffix, the package with the prefix before it and the
suffix after, `.`-joined; `csharp_namespace` from a prefix, the
prefix, `.`, and the package's components in PascalCase, `.`-joined;
`php_metadata_namespace` from a suffix, the package's components in
PascalCase, one that lower-cased is a PHP reserved word or predefined
class name (abstract, and, arithmeticerror, array, as, assertionerror,
bool, break, callable, case, catch, class, clone, closure, const,
continue, declare, default, die, directory, divisionbyzeroerror, do,
echo, else, elseif, empty, enddeclare, endfor, endforeach, endif,
endswitch, endwhile, error, errorexception, eval, exception, exit,
extends, false, final, finally, float, fn, for, foreach, function,
generator, global, goto, if, implements, include, include_once,
instanceof, insteadof, int, interface, isset, iterable, list, match,
namespace, new, null, or, parseerror, print, private, protected,
public, require, require_once, return, static, string, switch, throw,
throwable, trait, true, try, typeerror, unset, use, var, void, while,
xor, yield) getting `_` appended, `\`-joined, then `\` and the
suffix; `ruby_package` from a suffix, the components in PascalCase,
`::`-joined, then `::` and the suffix — a component's PascalCase
dropping its underscores and upper-casing its first letter and each
letter that followed an underscore, the rest as written.

**REQ-gen-request** (wire): The `CodeGeneratorRequest` delivered to a
plugin MUST carry: `file_to_generate` — the entry's generation
targets: the workspace modules' files the entry's `files` patterns
select, in compile order, matched by module-relative path against
every pattern, any one matching — every workspace file where the entry
names no pattern, and a pattern selecting no file failing generation
naming it, whatever the others select, a dead pattern being a mistake
the run must not pass over — then, where the entry includes imports,
every file the selected reach through imports, transitively, that is
no well-known import (the toolchain's, never generated for) and not
selected already, in `proto_file`'s order; `proto_file` — every
reachable file in topological order, dependencies before importers,
each file's imports visited in declaration order;
`source_file_descriptors` — the targets' descriptors, in
`file_to_generate`'s order; the entry's `opt` string as `parameter`
verbatim; and nothing else — no compiler version, no timestamp, no
environment. Descriptors carry full options in both descriptor fields:
pb does not strip source-retention options — a deliberate divergence
from protoc that only ever hands plugins more information.

**REQ-gen-request-determinism** (invariant): The `CodeGeneratorRequest`
delivered to a plugin MUST be a pure function of the compiled descriptor
set, the applied overrides, and the entry's declared parameters — files
in deterministic order, independent of filesystem iteration, cache
state, and prior runs. One entry's applied overrides never leak into
another entry's request: each request's descriptors are built fresh
from the compiled set.

**REQ-gen-out-containment** (invariant): Generated files MUST land only
under the entry's declared output directory; a response naming a file
that escapes it (absolute, unclean, or traversing above via `..`)
fails generation. Insertion points are unsupported: a response file
carrying one fails generation rather than patching content pb never
verified it against.

**REQ-gen-clean** (behavior): With `clean` true, `generate` MUST empty
every output directory an entry names before any entry's plugin runs —
every file and directory under it removed, the directory itself kept
where it exists and not created where it does not — once the build
compiled, every plugin was acquired and every entry's request was
built, so a refusal before that point — a dead pattern, an override
naming nothing — removes nothing, and refusing before removing
anything, naming the entry, where an output directory is the
resolution root, or is or holds the directory of a module read from
the working tree (a workspace module's, a directory replacement's), or
holds any file of such a module — a protobuf source, a rule file — or
holds a module file at any depth, whether or not the build reads that
module, or is reached through a symbolic link, any component of its
path from the root down being one, or names something other than a
directory, since what generation never wrote is not its to remove and
a link's target is never what its name says. Without `clean`,
generation writes over what an output directory holds and removes
nothing.

**REQ-gen-verb** (behavior): `generate` MUST run generation at the
working directory's resolution root: parse the generation file, compile
the build (`REQ-gen-compile`), acquire every entry's plugin — verified
and pinned, with the pins first use records persisted before any
plugin executes and whatever follows, per `REQ-lock-first-use` —
build every entry's request, empty the output directories where
`clean` asks (REQ-gen-clean), and then for each entry in declaration
order execute
its plugin under the trust policy's execution posture
(`plugin-execution.md`, `provenance.md`), and land the response's
files (`REQ-gen-out-containment`) — reporting each completed entry on
standard output with its plugin reference, file count, output
directory, the runner that ran it, reported sandbox tier, and the
bound-enforcing mechanism in
effect. An entry's failure fails the verb naming the entry; later
entries do not run.
