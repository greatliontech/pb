# pb — generation configuration

Generation compiles the workspace's modules and invokes plugins
(`plugin-execution.md`) over the resulting descriptors. Its configuration
file declares which plugins run, where output lands, and which file
options are overridden — declaratively, with no heuristics.

**generation file** (term): The file `pb.gen.yaml` at the resolution
root, declaring generation configuration.

**option override** (term): A declared assignment of a protobuf file
option (such as `go_package`) applied to matching files' descriptors
before plugins run.

**REQ-gen-schema** (wire): The generation file MUST contain `plugins`, a
non-empty list of entries carrying exactly one identity-scheme key —
`ref` (a plugin reference, the `oci` scheme) or `local` (a host binary
per `plugin-execution.md`) — plus `out`, an output directory, and
optional `opt`, the plugin parameter string handed to the plugin
verbatim; and optionally `overrides`, a list of entries `{files,
option, value}` where `files` is a glob pattern (the `/`-separated
component semantics `provenance.md` REQ-prov-trust-schema defines)
over module-relative proto file paths within the workspace and its
dependencies, `option` a protobuf option name in its navigable forms —
a built-in option's dotted field name, or a parenthesized
fully-qualified extension name with at most one field selector
(`(pkg.ext)`, `(pkg.ext).field`) — and `value` the option value's
spelling. No other top-level
keys and no other entry keys exist: there is no bare plugin-name key,
and an entry with zero or several scheme keys is a schema violation —
which scheme an entry lives in is always written, never inferred. A
`ref` value is `<registry>/<repository>:<tag>` in full: the registry
is a lowercase DNS host (no IPv6 literals, no uppercase — spellings pb
declines to normalize) naming itself unambiguously (containing a dot
or a port, or `localhost`), the repository follows the OCI
distribution grammar, the tag is written (no implicit `latest`), and
no `@digest` appears — the lockfile pins the digest. An `out` value is a
clean relative path written with forward slashes, never absolute and
never escaping the resolution root through `..`. Every scalar is
recorded with its written spelling — a value that looks numeric or
boolean is still the text the author wrote; `ref`, `local`, `out`,
`files` and `option` are one line of text, a spelling holding a line
break refused, while `opt` and `value` are text as written, a block
scalar included. Runner selection, trust
posture, and resource limits are not generation configuration and
have no keys here (`plugin-execution.md`, `provenance.md`).

**REQ-gen-emission** (behavior): Tooling that writes a generation file
MUST emit it canonically: UTF-8, LF line endings, two-space
indentation, `plugins` then `overrides`, the latter absent where it
holds nothing, entries in the order given, a `plugins` entry's keys in
the order `ref` or `local`, `out`, `opt` (absent where empty) and an
`overrides` entry's in the order `files`, `option`, `value`, each
scalar spelled as `check-rules.md`
REQ-lint-emission spells a scalar, and never a rendering the file's
reader rejects or reads as a different file.

**REQ-gen-compile** (behavior): Generation MUST compile every protobuf
file of every workspace module — a module's files being those under
its directory outside any nested module, with the module directory as
include root — resolving each import first against the well-known
imports — a module shipping a well-known path is ignored in favor of
the toolchain's copy, never an ambiguity — and then against the build
list's modules at their selected versions, each external module's archive root (a synthesized module's
subtree root, `REQ-resolve-synthesis`) serving as its include root. An
import satisfied by no module fails per
`REQ-resolve-unsatisfied-imports`; an import path that more than one
module provides fails naming the path and every provider — pb never
picks a provider by heuristic. Compilation output is ordered by
workspace module in use order, then by file path, independent of
filesystem iteration.

**REQ-gen-overrides-declarative** (behavior): Option overrides MUST be
applied exactly as declared to the descriptors of matching files —
workspace and dependency files alike, matched by include-root-relative
path — before plugin invocation: entries apply in declaration order,
later entries winning on overlap; a built-in option resolves by field
name on the file options, a custom option through the compiled set's
extension declarations; scalar-kind values parse by the field's kind
(strings verbatim, `true`/`false`, enum value names, Go integer and
float syntax); a name resolving to nothing, a non-scalar target, or a
value outside the kind fails — no option value is ever synthesized
from a heuristic.

**REQ-gen-request** (wire): The `CodeGeneratorRequest` delivered to a
plugin MUST carry: `file_to_generate` — the workspace modules' files in
compile order; `proto_file` — every reachable file in topological order,
dependencies before importers, each file's imports visited in
declaration order; `source_file_descriptors` — the generated files'
descriptors; the entry's `opt` string as `parameter` verbatim; and
nothing else — no compiler version, no timestamp, no environment.
Descriptors carry full options in both descriptor fields: pb does not
strip source-retention options — a deliberate divergence from protoc
that only ever hands plugins more information.

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

**REQ-gen-verb** (behavior): `generate` MUST run generation at the
working directory's resolution root: parse the generation file, compile
the build (`REQ-gen-compile`), acquire every entry's plugin — verified
and pinned, with the pins first use records persisted before any
plugin executes and whatever follows, per `REQ-lock-first-use` — and
then for each entry in declaration order build its request, execute
its plugin under the trust policy's execution posture
(`plugin-execution.md`, `provenance.md`), and land the response's
files (`REQ-gen-out-containment`) — reporting each completed entry on
standard output with its plugin reference, file count, output
directory, reported sandbox tier, and the bound-enforcing mechanism in
effect. An entry's failure fails the verb naming the entry; later
entries do not run.
