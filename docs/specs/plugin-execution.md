# pb — plugin execution

Code generation runs protoc plugins locally, from plain OCI images, inside
an OS-enforced sandbox. There is no pb-specific plugin format: any image
whose entrypoint speaks the protoc plugin protocol is a plugin, and
everything load-bearing rides standard OCI machinery — identity is the
digest, platform support is the manifest list, provenance is a signature
over the digest (`provenance.md`). Plugin pins live in the lockfile
(`module-lockfile.md`).

**plugin image** (term): An OCI image whose entrypoint reads a serialized
`CodeGeneratorRequest` from standard input and writes a serialized
`CodeGeneratorResponse` to standard output — the protoc plugin protocol.

**plugin reference** (term): A full OCI reference (registry, repository,
tag) naming a plugin image in generation configuration.

**sandbox tier** (term): The isolation level a sandbox run actually
achieved, as reported by the sandbox: `Strong` (kernel-enforced),
`OS` (capability/policy boundary), `Minimal` (resource limits only), or
`None`.

## Image handling

**REQ-plugin-plain-oci** (structural): Plugin acquisition and execution
MUST require nothing beyond standard OCI image content: no pb-specific
metadata, labels, or annotations are required, and any that are present
serve only optional convenience.

**REQ-plugin-no-privileged-source** (structural): Plugin references MUST
be full OCI references: no default registry, no privileged namespace, no
short-name expansion.

**REQ-plugin-digest-pin** (invariant): A plugin MUST execute only at the
manifest-list digest its reference is pinned to in the lockfile; a tag is
resolved to a digest when the pin is created, and never re-resolved
implicitly.

**REQ-plugin-platform-strict** (invariant): A plugin whose manifest list
contains no entry matching the host platform MUST be refused with an
error attributing the gap to the image; no emulation, substitution, or
fallback exists.

**REQ-plugin-verify-before-run** (behavior): A plugin image MUST pass
trust-policy evaluation (`provenance.md`) before any process from it is
executed.

## Execution

**REQ-plugin-sandboxed** (behavior): A plugin MUST run as a single
process execed into a freshly created isolated environment: its root
filesystem is the image's, read-only; it has no network access; its only
communication channels are standard input, standard output, and standard
error.

**REQ-plugin-min-tier** (behavior): Execution MUST require sandbox tier
`Strong` unless configuration explicitly lowers the requirement; an
environment that cannot deliver the required tier fails with an error
stating the achievable tier and how to lower the requirement explicitly.

**REQ-plugin-reported-tier** (invariant): The tier enforced against the
requirement MUST be the tier the sandbox reports for the actual run —
never an assumed or configured value.

**REQ-plugin-response-authority** (behavior): Generated output MUST come
exclusively from the plugin's `CodeGeneratorResponse`; a plugin exiting
non-zero, writing a malformed response, or declaring an error in the
response fails generation with the plugin's error surfaced verbatim.
