# The daemon's record of a memory kill is sometimes absent

Lands: when the misreport is reproduced with the daemon's event log
for the container captured alongside its record (a `docker events
--filter event=oom` read before the container's release), which is
the diagnosis's anchor and the candidate second record

The docker runner attributes a memory-bound death to the daemon's
record: `State.OOMKilled` in `docker inspect` after the run
(REQ-plugin-resource-bounds, "a bound-exceeded death is
attributable"). On Docker 29 over cgroup v2 with the systemd driver
the live suite's memory case (`TestDockerLive`, the `hog` run under a
64 MiB bound) has ended with status 137 and `OOMKilled` false — the
same run reported as "the CPU-time bound, an external kill, or the
plugin's own exit 137" — while the next runs of the same case set
the flag. The daemon sets the flag from its OOM event stream, and
the container's exit can outrun it. Under `stipulator check` that
run turns REQ-plugin-sandboxed red for the host it happened on; it
is not deterministic here, so no diagnosis is anchored yet.

Candidate resolutions, once anchored: read the container's OOM
event from `docker events` (the daemon's log of the same fact, not
subject to the race) before the container is released and attribute
the death on either record; or wait on the record with a bounded
retry where the exit status is 137 and neither record yet says why.
