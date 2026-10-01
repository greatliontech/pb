# The daemon loses a memory kill's record under load

Lands: the feature-full plan's chunk 22
(docs/plans/feature-full.md): the kill read from the
kernel's counter at a cgroup parent the daemon does not release

The docker runner attributes a memory kill to the daemon's event
log for the container, read around the container's finish
(REQ-plugin-resource-bounds); the record's own flag is set from the
same event. On Docker 29.7.2 over cgroup v2 with the systemd driver,
twenty-two loaded runs of a plugin killed at its memory bound
recorded the kill nineteen times — flag and event together — and
three times not at all: no flag, no event, over any window. The
loss is upstream of pb: the daemon's handler that sets both saw no
event. pb then reports the death as one the record cannot tell
apart — the CPU-time bound, an external kill, or the plugin's own
exit 137 — which is the truth of the record, and the live memory
case retries a run so reported, counting the loss. No record on the
daemon's side survives such a loss: the container's cgroup, whose
memory events the kernel counts, is released by the daemon at the
exit, before pb reads anything.

Spike over the plan's chunk 1, on Docker 29.8.1, cgroup v2, the
systemd driver: a container created with `--cgroup-parent=<name>.slice`
runs under a transient slice the daemon does not release — after the
container's exit and removal the slice stands at
`/sys/fs/cgroup/<name>.slice`, active with zero tasks, its
`memory.events` readable by pb's user and counting the kill
(`oom_kill 1` for a container killed at its memory bound). The
lifetime fails the plan's question: the slice is a root-owned system
unit, systemd never collects an active slice, and pb's user cannot
stop it (`systemctl stop` refuses without authentication), so a slice
per run leaks one unit per run. Two mechanisms remain sound, a fork
the user weighs: a per-user pool of reusable slices
(`pb-u<uid>-<n>.slice`), a slot claimed per run under a machine-wide
flock and the kill read as the slot's `memory.events` delta — the
residue a bounded set of permanent root-owned empty slices per user,
growing only with peak concurrency, the daemon's record the named
fallback for a remote or VM daemon; or the in-container static shim
the plan names, cross-built per linux architecture and embedded in
pb, copied into the created container and run as its first process,
relaying the plugin's streams and reporting the container's own
counter after the death — no host residue and a remote daemon
served, at the cost of a generate step, embedded binaries and a
pb-owned process in every plugin container. pb's own binary cannot
serve as the shim: a default `go install` links it dynamically.
