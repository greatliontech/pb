# The cgroups bound accounting runs live only under delegation

Lands: the runner suite runs in continuous integration inside a
delegated cgroup subtree with `PB_TEST_REQUIRE_CGROUPS=1`

The runner suite exercises the bounds accounting the host affords:
the sandbox places the run in a cgroup only where a delegated subtree
accepts one, and POSIX rlimits otherwise. On a plain interactive
session the session scope sits beside `user@.service` and is not
delegated, so the live cgroups arms — the memory-kill attribution,
the refused-fork counter — run through the attribution's unit tests
only. The suite says so in its log rather than skipping silently, and
fails outright when `PB_TEST_REQUIRE_CGROUPS=1` is set and the run was
accounted by rlimits, so a CI job can demand the coverage.

Locally the live arms run under a delegated scope:

    systemd-run --user --scope -p Delegate=yes env PB_TEST_REQUIRE_CGROUPS=1 go test ./internal/plugrun/

The docker runner's live arms — and the runner-independence
comparison between the two runners — run only where a daemon
answers, logged as skipped otherwise; `PB_TEST_REQUIRE_DOCKER=1`
demands them the same way.
