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
list of entries carrying exactly one identity-scheme key — `ref` (a
plugin reference, the `oci` scheme) or `local` (a host binary per
`plugin-execution.md`) — plus `out`, an output directory relative to the
resolution root, and optional `opt`, plugin parameters; and optionally
`overrides`, a list of entries `{files, option, value}` where `files` is
a glob pattern over module-relative proto file paths within the
workspace and its dependencies. No other top-level keys and no other
entry keys exist: there is no bare plugin-name key, and an entry with
zero or several scheme keys is a schema violation — which scheme an
entry lives in is always written, never inferred. Runner selection,
trust posture, and resource limits are not generation configuration and
have no keys here (`plugin-execution.md`, `provenance.md`).

**REQ-gen-overrides-declarative** (behavior): Option overrides MUST be
applied exactly as declared to the descriptors of matching files before
plugin invocation — later entries win on overlap, and no option value is
ever synthesized from a heuristic.

**REQ-gen-request-determinism** (invariant): The `CodeGeneratorRequest`
delivered to a plugin MUST be a pure function of the compiled descriptor
set, the applied overrides, and the entry's declared parameters — files
in deterministic order, independent of filesystem iteration, cache
state, and prior runs.

**REQ-gen-out-containment** (invariant): Generated files MUST land only
under the entry's declared output directory; a response naming a file
that escapes it (absolute, or traversing above via `..`) fails
generation.
