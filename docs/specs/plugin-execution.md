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
entry and never committed (`REQ-plugin-runner-selection`).

**REQ-plugin-runner-selection** (behavior): `generate` MUST select
the runner of each `oci` entry by layer and never substitute one: the
`--runner` flag where given, over the `PBRUNNER` environment variable
where set to a non-empty value, over the user configuration file's
`runner` key where present (`user-config.md`), over the default —
each layer naming a runner by these exact names (`native`, `docker`)
for every entry of the run, an empty environment value being an
absent layer as for pb's other environment settings and a flag given
empty naming no runner; the default choosing per entry by capability,
not heuristics — `native` where the entry's image serves the host's
platform and the sandbox's row for the entry meets the tier the
policy requires (`platforms.md` REQ-plat-oci-substrate,
REQ-plugin-min-tier), else `docker` where a daemon is reachable, else
the entry refused naming the platforms its image serves, the row the
host reaches and the floor — every input a stated fact, so the choice
is the same on every run; and a layer naming no runner, or a named
runner that is unavailable or cannot run an entry (`native` for an
image serving no entry for the host), failing with an error that
names the layer and, for a runner, the runner and the entry — no
entry ever falls back to another runner than the one its layer
named, and the default names none. A `local` entry runs on the native
runner on every platform, whatever the layers name
(`platforms.md` REQ-plat-local-runner). The run's report names each
entry's runner (`generation.md` REQ-gen-verb).

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
implicitly — only by the explicit update of the pin (`dep-verbs.md`
REQ-dep-update).

**REQ-plugin-platform-strict** (invariant): A plugin whose manifest list
contains no entry matching the host platform MUST be refused with an
error attributing the gap to the image — an artifact that is not a
manifest list is refused the same way, since the manifest list is the
author's platform declaration (the plugin image term) — and no
emulation, substitution, or fallback exists. Matching granularity is
`<os>/<arch>`, deliberately: the platform variant is not consulted, and
variant-aware selection is a future amendment, not implied behavior.
Exactly one entry matches, or none does: a manifest list carrying
several entries for the host — variants of one architecture — is
refused naming them, since choosing among them would be a fallback,
as the store's own rule holds — so an image published for the host's
architecture in several variants is refused on it. The one entry
admitted, its variant included, is the child of the verified index
the run uses, on every byte path.

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
the digest the substrate enforces. The byte path is the `plugin-pull`
setting (`PBPLUGINPULL`, or the user configuration file's
`plugin-pull` key; `user-config.md`): `store` (the default) exports
the verified image from pb's store into the runner; `docker` has the
daemon pull the verified digest itself — pb resolves and verifies
without materializing, pins as ever, and the runner pulls the
repository at that digest with the daemon's own credentials, runs the
container under the image's own configuration as it runs a
daemon-local image, and leaves the pulled image in the daemon, which
now holds it as its own; the runner's record is judged exactly as for
any run. The runner names to the daemon's pull and create the very
entry the seam admitted (`REQ-plugin-platform-strict`), its variant
included, so the daemon pulls and runs that child of the verified
index and no other — its own default and its own variant matching
(under which a bare `linux/arm` is `v7`, and a `v6`-only image would
be refused) never choosing; on the store path the import stamps the
daemon's platform, which the create names back. A daemon-local
override, which pb selects nothing of, is created as it is. A runner
that runs no daemon images (`native`) refuses `docker` before
anything runs, and a value naming neither byte path is refused —
each naming the layer the value came from.

## Local binaries (`local` scheme)

The `local` scheme runs a host binary as the plugin process. It is an
explicit downgrade: the binary has no manifest, no provenance, and no
image root filesystem, so REQ-plugin-digest-pin, verify-before-run,
platform-strict, and the sandbox contract cannot hold for it. A `local`
entry executes only when the trust policy permits the scheme
(`provenance.md`); that permission is the root's explicit acceptance of
an execution none of the sandbox's guarantees hold for. The run
reports the tier the sandbox reports
for the row that ran it (`REQ-plugin-reported-tier`), and that tier
grades nothing of the world: the binary runs in the host's, with the
host's environment and network, under the resource bounds alone —
no floor is required of it.

**REQ-plugin-local-resolution** (behavior): A `local` value containing no
path separator MUST resolve through the `PATH` environment variable
exactly as written — no name is ever synthesized (no `protoc-gen-`
prefixing, no extension expansion but the platform's executable
suffix where the platform has one, `platforms.md`
REQ-plat-local-runner) — while a value containing a path
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

**REQ-plugin-sandboxed** (behavior): An `oci` plugin MUST run as a
single process execed into a freshly created isolated environment: its
root filesystem is the image's, read-only; it has no network access;
its only communication channels are standard input, standard output,
and standard error. The `docker` runner's known deviations, which pb
cannot switch off through the daemon's API, are these classes and no
others: the runtime filesystems an OCI runtime mounts over the image's
root — `/proc`, `/sys` and `/dev`, with everything the runtime places
beneath them (its masks, `/dev/pts`, `/dev/mqueue`, a writable
`/dev/shm`, `/dev` itself writable) — the daemon's three name files
`/etc/hosts`, `/etc/hostname` and `/etc/resolv.conf` bound over the
image's, and the variables the daemon injects into a container
created, as pb creates every one, without a terminal: `PATH` where the
image states none, `HOSTNAME`, `HOME`; the daemon's confinement of the
process — under a daemon naming AppArmor among its security options,
its built-in default profile `docker-default` in enforce mode (moby's
`profiles/apparmor` template), which denies a write to a file directly
in `/proc`, under its non-numeric subdirectories and under `/proc/sys`
outside `/proc/sys/kernel/shm*` whatever the runtime's mask admits, a
mount, a write under `/sys` outside the container's own cgroup, and a
ptrace of a process outside the profile; under a daemon naming
SELinux, the container type the policy's container contexts name, with
the per-container categories the daemon allocates; under a daemon
naming neither, no confinement, the kernel reporting none or
`unconfined` — and a daemon image — a daemon-local override, or the
docker byte path (`REQ-plugin-core-verifies`) — runs under the image's
whole configuration as the daemon applies it, since running an image
other than as it declares is not what a daemon image means: a declared
`VOLUME` is a writable anonymous volume over the read-only root,
released with the container; `USER` sets the process's uid; a
`HEALTHCHECK` runs. The native runner presents none of these: its
world is the image's alone, as `Root` bounds it on the sandbox row
that ran (sandbox's "Root is world-restriction"): the export at `/` on
the `Strong` row, with a fixed hostname; on the Linux `OS` row —
reached where the host refuses unprivileged user namespaces but has
Landlock and seccomp (sandbox's ladder), and admitted only by an
explicit lowering (`REQ-plugin-min-tier`) — the export at its host
path, read-only, with no network, where only a static entrypoint
loads: a plugin whose entrypoint is a script, dynamically linked, or
unreadable is refused before it runs, the refusal naming the sandbox's
reason. That row's exposures are the lowering's to accept: a plugin
that dereferences image-absolute paths at runtime observes the host's
resolution, the row presents no hostname so the plugin observes the
host's, and the IPC the row leaves open to the same user — unix
sockets by path, and abstract sockets and signals where the kernel
does not scope them — is reachable by a plugin that goes for it, its
own doing as the `docker` deviations are; pb hands it the standard
streams alone on every row. INV-docker-deviations: every mount over
the image's root is a filesystem the runtime created under one of the
three roots — mounted whole, or re-bound from one of those very
filesystems as the runtime's masks are, never a host directory bound
there — or a name file, every variable beyond the image's is an
injected one, `/dev` is writable while `/proc` itself and the root are
not, the runtime's masks standing over the paths it masks — what a
mask admits being the runtime's and the daemon's confinement's own —
the process's confinement is the daemon's default profile enforced
where the daemon names AppArmor, the container type where it names
SELinux and none, the kernel reporting none or `unconfined`, where it
names neither — all as the running plugin sees them on a daemon whose
account of itself is true (`REQ-plugin-reported-tier`: pb audits no
daemon at a run; the invariant holds the list complete against a
conforming one) — and the native runner's world is the image's alone,
no confinement reported, as the running plugin sees it; enforced by
`TestDockerDeviations` against a live daemon and the native runner.

**REQ-plugin-resource-bounds** (behavior): Every plugin process — every
scheme, every tier — MUST run under bounded memory, CPU, process count,
and wall-clock time: implementation-declared defaults, overridable
through the trust policy. A plugin exceeding a bound is terminated and
reported as a plugin failure naming the bound exceeded wherever the
enforcing mechanism attributes the termination: the wall clock always,
and otherwise as the mechanism's own accounting affords — under
cgroups, memory kills and refused forks from the kernel's event
counters where the runner reads them (the native runner) — a memory
kill attributed when the plugin died by a kill and one was counted
over the run, the counter placing no kill in time; a refused fork
when the plugin then failed — and the memory kill alone where the
runner reads a daemon's record of the container (the `docker`
runner): a plugin that died by a kill is read against the daemon's
event log for the container around its finish, on the daemon's own
clock, before the container's release, an oom event there being the
memory bound — the record's own flag is set from that same event
and places it nowhere in time, and a kill the daemon recorded
nowhere is a death the record cannot tell apart; a kill or a
refusal the plugin outlived terminated nothing of it, and its
response, or its own failure, stands; a refused fork under the
daemon being the
plugin's own failure surfaced verbatim and an exit status of 137
there being the CPU-time bound, an external kill, or the plugin's own
exit 137, which that record cannot tell apart; CPU time, a POSIX
rlimit under every accounting, whose
exhaustion arrives as an unlabeled kill and is reported as either the
CPU-time bound or an external kill — never claimed as one; and under
POSIX rlimits a refused allocation or fork is not a termination and
the plugin's own resulting failure is surfaced verbatim. The
mechanism enforcing the memory and process bounds is machine-scoped,
not mandated; a run's report names the mechanism actually in effect,
so every failure is read against a named enforcement.

**REQ-plugin-min-tier** (behavior): `oci` execution MUST require sandbox
tier `Strong` unless the trust policy explicitly lowers the requirement;
an environment that cannot deliver the required tier fails with an error
stating the achievable tier and how to lower the requirement explicitly.
The lowering path ends at `OS`: a host reaching only the `Minimal`
row runs no `oci` plugin at any floor, that row being unable to deny
the network or to restrict the world to the export
(`REQ-plugin-sandboxed`), and the error says so rather than offering
a lowering that cannot help.

A daemon that records the container unconfined by AppArmor is no
`Strong` daemon: `apparmor unconfined` is among what the record may
lack, the record being the daemon's own account
(`REQ-plugin-reported-tier`).

**REQ-plugin-reported-tier** (invariant): The tier enforced against
the requirement MUST be the tier the sandbox reports for the actual
run — never an assumed or configured value. A runner's report is the
substrate's own account of what it applied — the kernel's answers to
the native runner's calls; for the `docker` runner the daemon's record
of the container and of itself, the profile it names for its
containers among the latter — and pb audits neither substrate: it
requests the isolation the policy needs, confirms the request was
honored as the substrate reports it, and refuses where the report says
less.

**REQ-plugin-runner-independence** (invariant): Generated output MUST be
a pure function of the pinned plugin content and the
`CodeGeneratorRequest`: the same digest and request yield the same
response under every runner. No runner-specific fact (mount paths,
container names, substrate environment) may be observable in a
response through the plugin protocol — the request in, the response
out. The `docker` runner's named deviations (`REQ-plugin-sandboxed`)
are observable to a plugin that goes and reads them — its uid, the
daemon's `/etc` files, a declared volume — and such a plugin's output
is its own doing, not the runner's: the invariant binds what pb
hands a plugin and takes from it, and pb passes no runner-specific
fact by either. The native runner's `OS` row leaves the host's
hostname and paths observable the same way (`REQ-plugin-sandboxed`).

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
override is refused when the trust policy forbids overrides. On the
invocation an override is spelled `--override REF=SOURCE`, repeatable,
where REF is the declared `oci` reference of an entry — a key naming
no such entry is an error — and SOURCE is a directory holding an OCI
layout, a file holding an OCI layout archive or a docker-save tarball,
or `docker://IMAGE` naming a daemon-local image; a layout or archive
passes the platform check and trust-policy evaluation like any
acquisition (`REQ-plugin-verify-before-run`) — its evidence sought
where it was staged, which holds none, so under `require-provenance`
an override is refused as unsigned, the `plugin-overrides` key
admitting overrides and the posture judging them — while a daemon-local
image is the daemon's content, never acquired by pb and never
evaluated by it — the permission that admits overrides admits that;
the `docker` runner's record check holds the boundary and the bounds,
not the content's provenance — and runs only under the `docker`
runner, which fetches nothing for it: an image the daemon does not
hold is an error.
