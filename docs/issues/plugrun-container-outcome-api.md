# The runner re-derives what container already holds

Lands: pb's native runner runs on sandbox, whose API reports the
exit status (code or signal) and the bounds accounting

Two facts the native runner needs are container's own but reach pb
only indirectly:

- The payload's outcome. `Container.Wait` reaps with wait4 and returns
  the exit code or the killing signal as error text ("container
  exited with status N", "container killed by signal N"), recording
  the code on the container. `plugrun.exitStatus` parses those
  sentences and cross-checks the code; a typed outcome (code, signal)
  would delete the parse and the coupling to the wording.
- The run's cgroup. container creates the cgroup and holds its path
  unexported; `plugrun.cgroupDirOf` re-derives it from the payload's
  procfs entry and the cgroup2 mount in mountinfo. An accessor would
  delete the derivation.

Both are container-side additions; the ecosystem note lists them with
the tier report and the pivot change.

Retargeted 2026-09-19: the runner moves from container to sandbox
(docs/notes/sandbox-consolidation.md); sandbox's `ExitStatus` and
its reported accounting are the typed outcome, so the parse and the
procfs derivation go with the move.
