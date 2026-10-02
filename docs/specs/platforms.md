# pb — platforms

pb runs on more than one operating system, and what it can promise
differs by what each affords: a sandbox row, a daemon, an image entry
for the host, a filesystem's case rule, a way to replace an open
file. This document names the
platforms pb builds for and states, for each contract the other
documents write in Unix terms, what holds everywhere and what a
platform changes. Every contract it touches is defined where its
subject is (`plugin-execution.md`, `module-archive.md`, `export.md`,
`dep-verbs.md`, `user-config.md`); this document adds the platform's
reading and nothing else.

**platform** (term): An operating system and architecture pair pb is
built for, spelled as Go spells them: `linux/amd64`, `linux/arm64`,
`darwin/arm64`, `darwin/amd64`, `windows/amd64`, `windows/arm64`. The
platform of a run is the host's; a plugin image's platform is the
substrate's that runs it (`plugin-execution.md`
REQ-plugin-platform-strict).

**sandbox row** (term): The mechanism set the sandbox selects for a
host and the tier it reports — `Strong`, `OS` or `Minimal` — as the
sandbox's own contract defines them (its mechanism ladder). A host
reaches one row; the row is a fact read before a run and reported
after it (`plugin-execution.md` REQ-plugin-reported-tier).

## Scope

**REQ-plat-scope** (behavior): pb MUST build for every platform the
term names with no C toolchain — every mechanism a platform's
backend uses is reached through system calls or by executing a
platform binary, never through cgo — with its suite running on each
operating system in continuous integration, so a contract this
document states for a platform is one the suite witnessed there; a
behavior only real hardware or a daemon can witness (a sandbox row
on a host, a Docker Desktop daemon) is stated here as the contract
and tracked to its witness as any deferral is.

## Plugin execution

**REQ-plat-oci-substrate** (behavior): An `oci` plugin MUST run on
the substrate whose platform its image serves, on every platform the
same way: the native runner exports the image's entry for the host's
platform and executes its entrypoint under the sandbox row the host
reaches, where that row meets the tier the policy requires
(`plugin-execution.md` REQ-plugin-min-tier — `Strong` unless lowered,
so the `OS` rows of `darwin` and `windows` run an `oci` plugin only
under the policy's explicit lowering); the `docker` runner hands the
image's entry for the daemon's platform to a daemon running Linux
containers — Docker Desktop's on `darwin` and `windows`, whose
platform is `linux` at the daemon's architecture (`linux/arm64` on an
Apple-silicon host, `linux/amd64` under WSL2). The entry sought is
the substrate's platform and exactly one must match
(REQ-plugin-platform-strict): an image serving no entry for the host
is refused under the native runner naming the platforms it serves,
and runner selection's default chooses per entry
(REQ-plugin-runner-selection): `native` where the image serves the
host and the row meets the floor, `docker` for the rest where a
daemon is reachable, so one run mixes what runs natively with what
a daemon must run and the report says which.
On an `OS` row the plugin's world is the export and the platform's
own system libraries, which every `darwin` and `windows` binary
links (`libSystem`, the system DLLs), and nothing else; the Linux
`OS` row's static-entrypoint rule is that platform's alone. A daemon
in a virtual machine leaves the host's cgroup tree unreadable to pb:
a memory kill there is attributed as the daemon's record affords it
and the record says which source spoke (REQ-plugin-resource-bounds);
pb hands the daemon the export as a stream and binds no host
directory, so the daemon's file sharing is never consulted.

**REQ-plat-local-runner** (behavior): A `local` plugin, a host command,
MUST run on the native runner on every platform, under the sandbox
row the host reaches and with no tier floor of its own — listing the
scheme is the root's acceptance of host execution
(`provenance.md` REQ-prov-exec-policy) — the row reported for the run
as for any: `Strong` or `OS` on `linux` by the host's facts, `OS` on
`darwin` where Seatbelt is available and on `windows` where an
AppContainer is, `Minimal` where a platform's security boundary is
absent and only bounds apply; a platform matching no row refuses the
run naming the platform, never a bare exec. The `docker` runner
never runs a `local` plugin, on any platform. On `windows` a `local`
command with no path separator resolves to the name with `.exe`
appended, unless it already ends in `.exe` in any case, and to
nothing else — the platform's executable suffix, never `PATHEXT`'s
scripts, which the sandbox would not run as one process, and never
a file found by any other spelling (a trailing dot the platform
drops, a search of the working directory or of a relative `PATH`
entry, which the pinned hash and the run would read differently)
— case being the filesystem's, a case-insensitive one answering for
a case variant with its one file as it does for every open — the one
extension REQ-plugin-local-resolution's no-expansion rule admits; a
value holding a backslash is neither a name nor a path on any
platform and is refused naming it, paths being spelled with forward
slashes; the content pin is keyed by host platform
as REQ-plugin-local-pin has it, so one lockfile pins a plugin's
binary per platform.

## Files pb reads and writes

**REQ-plat-files** (invariant): Every file pb writes MUST read the
same on every platform, every rule the file contracts state in Unix
terms holding on each: paths inside files, and the tree-relative
paths pb prints — a report's, a fact's, a refusal's — are spelled
with forward slashes, the files emitted with LF (`module-file.md`
REQ-modfile-emission, `module-lockfile.md`
REQ-lock-canonical-emission, `workspace.md` REQ-work-emission,
`check-rules.md` REQ-lint-emission, `generation.md`
REQ-gen-emission); a file set or an export tree two of whose paths
fold equal is refused before anything touches a disk
(`module-archive.md` REQ-archive-case-collision, `export.md`
REQ-export-layout), so a case-insensitive default filesystem
(`darwin`'s, `windows`'s) holds every file a case-sensitive one
holds; no export writes a link, a submodule entry or an executable
mode (REQ-export-materialization), so a platform's link privilege
and mode model are never consulted; the module cache and the plugin
store are addressed by escaped paths that share the proxy's
case-insensitivity rule (`dep-verbs.md` REQ-dep-cache-layout). A
file replaced whole by rename (an atomic write, a lockfile
rewritten, an export moved into place) is replaced atomically on
every platform; on `windows` a target another process holds open
without delete sharing cannot be replaced, and the write fails
loudly naming the file — a rerun by the user succeeding once it is
released — never a partial file, since the rename is the only step
that touches the target. A
lock file the vcs store leaves in place (REQ-dep-clean) is left on
every platform: a process waiting on it holds it open, and a file
recreated under its name would grant a second holder over a live
claim.

**REQ-plat-user-dirs** (behavior): The user configuration file and
the default cache and store directories MUST live under the
platform's own user directories as Go's `os.UserConfigDir` and
`os.UserCacheDir` name them — `$XDG_CONFIG_HOME` and
`$XDG_CACHE_HOME` on `linux`, `~/Library/Application Support` and
`~/Library/Caches` on `darwin`, `%AppData%` and `%LocalAppData%` on
`windows` — under the same `pb` subdirectory and file names on each
(`user-config.md`), the environment and the file's own settings
overriding exactly as there; a stated location the platform cannot
use — a relative path in the variable, which Go checks on some
platforms alone — is refused naming the variable on every platform;
a path setting is absolute as the platform spells one (a volume on
`windows`), any other spelling relative to the file's directory as
`user-config.md` has it; pb consults no platform registry or
preference store.
