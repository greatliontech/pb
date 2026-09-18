# The native runner's pivot writes scratch into the shared export

Lands: pb's native runner runs on sandbox, whose pivot writes nothing
into the new root

container's create path pivots by creating `.pivot_root` under the new
root, pivoting onto it, and removing it. pb bind-mounts the ocifs
export-cache entry as the new root, so that directory is created and
removed in the store's shared, content-addressed export — a directory
plugoci hands over as read-only. Two consequences:

- Two pb processes running the same image race: one removes the
  scratch directory while the other's pivot still needs it, and the
  loser's run fails to start.
- The export entry is mutated, transiently, by every run.

The read-only remount lands after the pivot, so pb cannot bind the
root read-only first without breaking the pivot. The fix is
container's: pivot with new root and put-old the same directory and
detach the old root from the current directory, which needs no
scratch path; pb then binds the export read-only from the start and
the store's entry is never written.

Retargeted 2026-09-19: the runner moves from container to sandbox
(docs/notes/sandbox-consolidation.md); sandbox's `PivotRoot` already
uses the scratch-free form, so the issue resolves with the move.
