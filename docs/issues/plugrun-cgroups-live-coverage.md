# The cgroups bound mechanism runs live only under delegation

Lands: the runner suite runs in continuous integration inside a
delegated cgroup subtree with `PB_TEST_REQUIRE_CGROUPS=1`

The runner suite exercises every bound mechanism the host affords:
POSIX rlimits always, cgroups only where `container.CgroupsAvailable`
finds a writable subtree. On a plain interactive session the session
scope sits beside `user@.service` and is not delegated, so the live
cgroups arms — locating the run's cgroup, reading its event counters,
the memory-kill attribution, the cgroup's removal — run through their
pure helpers' unit tests only. The suite says so in its log rather
than skipping silently, and fails outright when
`PB_TEST_REQUIRE_CGROUPS=1` is set and cgroups are unavailable, so a
CI job can demand the coverage.

Locally the live arms run under a delegated scope:

    systemd-run --user --scope -p Delegate=yes go test ./internal/plugrun/
