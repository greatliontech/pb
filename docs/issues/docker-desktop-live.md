# The docker runner's live arms on darwin and windows

The `docker` runner on darwin and windows hands an image's entry for
the daemon's platform to Docker Desktop's Linux daemon
(`docs/specs/platforms.md` REQ-plat-oci-substrate): `linux/arm64` on
an Apple-silicon host, `linux/amd64` under WSL2. The runner suite's
live docker arms (`PB_TEST_REQUIRE_DOCKER=1`) witness that contract
on Linux in continuous integration, where the daemon is the host's
own; the macOS runner there has no daemon and the windows runner's
daemon runs Windows containers, which the runner refuses, so on
those platforms the arms skip and the contract is stated, not
witnessed (REQ-plat-scope's reading: what only hardware or a daemon
can witness is tracked to its witness).

Resolution: run the runner suite with the docker arms demanded on a
darwin host and on a windows host with Docker Desktop — on windows
the daemon byte path's arms, the stream refused there
(docker-store-path-windows.md) — and bank each run's log beside this
issue; a failure there is a finding against the runner,
dispositioned like any.

Lands: a log of `PB_TEST_REQUIRE_DOCKER=1 go test
./internal/plugin/runner/` green on a darwin host and on a windows
host with Docker Desktop, banked beside this issue.
