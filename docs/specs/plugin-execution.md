# pb — plugin execution

Code generation runs protoc plugins locally, from plain OCI images, inside
an OS-enforced sandbox. There is no pb-specific plugin format: any image
whose entrypoint speaks the protoc plugin protocol is a plugin, and
everything load-bearing rides standard OCI machinery — identity is the
digest, platform support is the manifest list, provenance is a signature
over the digest (`provenance.md`). Plugin pins live in the lockfile
(`module-lockfile.md`); which guarantee degradations a resolution root
tolerates is governed by the trust policy (`provenance.md`).

A generation entry names its plugin in exactly one identity scheme, and a
machine-scoped runner executes it. The scheme decides what the plugin
*is* (and which guarantees can hold for it); the runner decides only how
this host executes it, and is never observable in generated output.

**plugin image** (term): An OCI image whose entrypoint reads a serialized
`CodeGeneratorRequest` from standard input and writes a serialized
`CodeGeneratorResponse` to standard output — the protoc plugin protocol.

**plugin reference** (term): A full OCI reference (registry, repository,
tag) naming a plugin image in generation configuration.

**identity scheme** (term): The namespace a generation entry's plugin
identity lives in: `oci` (a plugin reference, the default path with the
full guarantee set) or `local` (a host binary, an explicit downgrade).
`remote` (execution by an external service) is a reserved scheme name
this document does not define; any future definition must model it as
its own trust category — remote execution ships the compiled descriptors
to the service, a disclosure no sandbox tier expresses — and extends the
pin and policy vocabularies under its own scheme value.

**runner** (term): The execution substrate for `oci`-scheme plugins:
`native` (pb's own sandbox) or `docker` (a Docker daemon). Runner
selection is machine-scoped configuration, never part of a generation
entry and never committed: flag over environment over user configuration
over the platform default. The platform default is chosen by capability
exhaustion, not heuristics: `native` where pb's sandbox is available
(Linux), otherwise `docker` where it is the only viable runner. A
selected runner that is unavailable fails with an error naming it;
no run ever falls back to another runner silently.

**sandbox tier** (term): The isolation level a sandbox run actually
achieved, as reported by the sandbox: `Strong` (kernel-enforced),
`OS` (capability/policy boundary), `Minimal` (resource limits only), or
`None`.

**plugin override** (term): An invocation-scoped substitution of the
content behind an `oci` generation entry, keyed by the entry's declared
plugin reference. Overrides are a development affordance outside the
supported identity schemes: they exist so a plugin under development can
run without entering any committed artifact.

## Platform scope

pb supports Linux (`native` and `docker` runners) and macOS (`docker`).
Windows is a named non-goal: no pb behavior is defined on it, and WSL2 —
a Linux environment where this document applies as written — is the
sanctioned route. Paths in committed configuration are always written
with forward slashes regardless of host convention.

## Image handling (`oci` scheme)

**REQ-plugin-plain-oci** (structural): Plugin acquisition and execution
MUST require nothing beyond standard OCI image content: no pb-specific
metadata, labels, or annotations are required, and any that are present
serve only optional convenience.

**REQ-plugin-no-privileged-source** (structural): Plugin references MUST
be full OCI references: no default registry, no privileged namespace, no
short-name expansion — a reference is fetched at the repository path
exactly as written (`docker.io/ubuntu` is `/v2/ubuntu`, never rewritten
to `library/ubuntu`).

**REQ-plugin-digest-pin** (invariant): A plugin MUST execute only at the
manifest-list digest its reference is pinned to in the lockfile; a tag is
resolved to a digest when the pin is created, and never re-resolved
implicitly.

**REQ-plugin-platform-strict** (invariant): A plugin whose manifest list
contains no entry matching the host platform MUST be refused with an
error attributing the gap to the image — an artifact that is not a
manifest list is refused the same way, since the manifest list is the
author's platform declaration (the plugin image term) — and no
emulation, substitution, or fallback exists. Matching granularity is
`<os>/<arch>`, deliberately: the platform variant is not consulted, and
variant-aware selection is a future amendment, not implied behavior.

**REQ-plugin-verify-before-run** (behavior): A plugin image MUST pass
trust-policy evaluation (`provenance.md`) before any process from it is
executed — on every acquisition, cached content included, so a
tightened policy gates immediately. The module pipeline's
pin-is-the-record posture deliberately does not apply here: the
stricter per-run posture sits on the side that executes code.

**REQ-plugin-core-verifies** (invariant): Pin resolution, trust-policy
evaluation, and the platform check MUST be performed by pb against the
manifest list it fetched itself, before any runner is involved; runners
execute already-verified content and never perform or substitute for
verification. Image content reaches the runner by any byte path — pb's
own store by default, or, as machine-scoped opt-in configuration, a
Docker daemon pulling by digest — because content addressing makes the
byte path irrelevant to identity: the digest verified by the core is
the digest the substrate enforces.

## Local binaries (`local` scheme)

The `local` scheme runs a host binary as the plugin process. It is an
explicit downgrade: the binary has no manifest, no provenance, and no
image root filesystem, so REQ-plugin-digest-pin, verify-before-run,
platform-strict, and the sandbox contract cannot hold for it. A `local`
entry executes only when the trust policy permits the scheme
(`provenance.md`); that permission is the root's explicit acceptance of
unsandboxed execution, and the run reports sandbox tier `None`.

**REQ-plugin-local-resolution** (behavior): A `local` value containing no
path separator MUST resolve through the `PATH` environment variable
exactly as written — no name is ever synthesized (no `protoc-gen-`
prefixing, no extension expansion) — while a value containing a path
separator resolves relative to the resolution root (absolute paths
allowed), written with forward slashes. Resolution failure fails
generation with an error naming the search performed; it never falls
back to another scheme or source.

**REQ-plugin-local-pin** (behavior): Unless the trust policy disables
local pinning, first use of a `local` plugin MUST record the resolved
binary's content hash in the lockfile per `REQ-lock-first-use`, keyed by
host platform, with later runs failing on a hash mismatch and naming
both hashes. Identity is the bytes, not the location: a moved or
re-resolved binary with identical content is a non-event.

## Execution

**REQ-plugin-sandboxed** (behavior): An `oci` plugin MUST run as a single
process execed into a freshly created isolated environment: its root
filesystem is the image's, read-only; it has no network access; its only
communication channels are standard input, standard output, and standard
error.

**REQ-plugin-resource-bounds** (behavior): Every plugin process — every
scheme, every tier — MUST run under bounded memory, CPU, process count,
and wall-clock time: implementation-declared defaults, overridable
through the trust policy. A plugin exceeding a bound is terminated and
reported as a plugin failure naming the bound exceeded.

**REQ-plugin-min-tier** (behavior): `oci` execution MUST require sandbox
tier `Strong` unless the trust policy explicitly lowers the requirement;
an environment that cannot deliver the required tier fails with an error
stating the achievable tier and how to lower the requirement explicitly.

**REQ-plugin-reported-tier** (invariant): The tier enforced against the
requirement MUST be the tier the sandbox reports for the actual run —
never an assumed or configured value.

**REQ-plugin-runner-independence** (invariant): Generated output MUST be
a pure function of the pinned plugin content and the
`CodeGeneratorRequest`: the same digest and request yield the same
response under every runner. No runner-specific fact (mount paths,
container names, substrate environment) may be observable in a response.

**REQ-plugin-response-authority** (behavior): Generated output MUST come
exclusively from the plugin's `CodeGeneratorResponse`; a plugin exiting
non-zero, writing a malformed response, or declaring an error in the
response fails generation with the plugin's error surfaced verbatim.

## Plugin overrides

**REQ-plugin-override** (behavior): An override, given on the generation
invocation and keyed by an entry's declared plugin reference, MUST
substitute only the content that executes — the entry's output
directory, parameters, and request are unchanged — and leave the
lockfile untouched in both directions: no pin is written for the
override, and the entry's pin is not checked against it. Each overridden
entry is reported on standard error for that run. Override sources are
content a runner already consumes: an OCI layout or archive (loaded into
pb's store, any runner) or a daemon-local image (`docker` runner). An
override is refused when the trust policy forbids overrides.
