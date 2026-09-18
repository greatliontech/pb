# Cgroup bounds fail to start wherever pb is a direct member of the delegated cgroup

Lands: pb's native runner runs on sandbox with cgroup accounting
available inside a delegated scope

Where `container.CgroupsAvailable` is true because pb's own cgroup is
writable — a `systemd-run --scope -p Delegate=yes` session, or a CI
container whose namespace root is the container's cgroup — pb is a
direct member of the cgroup the run's cgroup is created under. cgroup
v2 refuses to enable controllers for the children of a cgroup with
member processes, container ignores that refusal, and the run fails
at start: `cgroups required but limits not applicable: apply memory:
open .../memory.max: permission denied`. The runner reports the
failure honestly and never falls back, so `pb generate` is unusable
under the cgroups mechanism on such hosts until the placement is
fixed.

The fix is container's: the sanctioned delegation pattern moves the
caller into a leaf child of the delegated cgroup first, then enables
the controllers, then creates the run's cgroup as a sibling — and
reports a controller it cannot enable instead of ignoring it.

The fix has a disclosed consequence: every member process of the
delegated cgroup — in a Delegate=yes session scope that is the user's
shell and jobs, in a container its init — is moved into the leaf
child, permanently for the cgroup's lifetime. Accounting outside the
delegated cgroup is unchanged; a reader of that cgroup's own
`cgroup.procs` sees its processes one level down. Whether the spec
should state this effect is relayed to the user.

Retargeted 2026-09-19: container landed the fix (vacate into a leaf,
one-write controller enable) on its master; the runner moves to
sandbox, whose mechanism layer must carry the same kernel rule (the
replica twin obligation) before this closes.
