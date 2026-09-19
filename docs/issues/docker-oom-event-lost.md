# The daemon loses a memory kill's record under load

Lands: a Docker release on which the live memory case records its
kill in fifty consecutive loaded runs (the daemon's oom event
delivered every time), pinned by the runner suite

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
