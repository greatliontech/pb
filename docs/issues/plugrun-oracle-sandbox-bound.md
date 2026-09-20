# Runner mutation evidence is machine-local

Lands: gomutant's observation bracket admits a runner's kernel inputs:
a SandboxRunner.Run mutant classified other than unstable-oracle

the runner suite (internal/plugin/runner) executes a real sandbox per
test — fresh namespaces, a pivoted read-only root, cgroup or rlimit
bounds — so its runtime inputs (kernel state, mount tables, cgroup
filesystems) fall outside gomutant's observation bracket and every
SandboxRunner.Run mutant is classified unstable-oracle. The suite is
genuinely adversarial (isolation, network absence, exit codes, wall
clock, memory bounds, and read-only root are each asserted against a
live sandbox) and hand probes through `gomutant ephemeral` demonstrate
kills, but the records stay machine-local — the same class as the
archive-extraction and plugin-acquisition suites. Pure logic reachable
without the sandbox (response authority, bound folding, output
truncation) is measured normally through its own unit tests.
