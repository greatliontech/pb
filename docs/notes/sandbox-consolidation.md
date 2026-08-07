# Sandbox consolidation: container's guts become sandbox's Linux backend

Adopted direction (2026-08). Execution lives in `../sandbox` and
`../container`, scheduled by their owner; this note tracks it from pb's side
because it sets pb's dependency path.

## The insight

sandbox's pending Linux roadmap — rootfs pivot + bind mounts, seccomp,
capability drop, cgroup v2 limits — is nearly a checklist of what container
already implements and tests. Building sandbox's Linux backend from scratch
while container sits next door would create two parallel implementations of
the same mechanism, owned by the same person. Instead: **lift container's
create-path machinery into sandbox**, making sandbox the shared create-only
core.

## The seam is the cgo boundary

sandbox's README already identifies the natural split: the single-threaded
`setns` constraint that forces cgo applies only to *joining* running
namespaces, never to *creating* them at clone time. That constraint line
becomes the repo boundary:

- **sandbox** owns everything pure-Go-able: create a fresh isolated
  environment (namespaces + uid/gid maps, rootfs pivot, mounts, seccomp,
  caps, cgroups, rlimits) and exec one process into it. Per-platform
  backends, tier reporting.
- **container** re-bases its create path on sandbox and keeps what makes it
  a full runtime: exec-into-running (the cgo/nsenter part), lifecycle, OCI
  compliance, console handling.

One mechanism, two ambitions layered instead of duplicated.

## What each repo gains

- **sandbox**: Linux backend goes from spike to proven by lift-and-refactor;
  container's existing tests are the source of truth for the lifted code.
- **container**: sheds its duplicated create path; becomes sandbox + join.
- **pb**: depends on sandbox from day one — contract and implementation
  aligned, no backend swap later. The container-as-first-backend fallback
  (recorded in [ecosystem.md](./ecosystem.md)) stays available if sequencing
  changes, since pb's runner contract is identical either way.
- **ociplug**: gets the foundation it was named for.

## Layering consequence for pb

sandbox should stay ignorant of OCI images — it consumes a prepared rootfs
path and a Spec. Combining "mount this image" (ocifs / projfs-go / fskit-go)
with "sandbox this process" (sandbox) is ociplug's job:

```
pb ──► ociplug ──► (ocifs | projfs-go | fskit-go)  +  sandbox
```

Whether pb targets ociplug or composes the two itself until ociplug exists
is open — but the sandbox/image seam should be respected either way, so
ociplug can slot in without reshaping pb.

## Costs, honestly

- Work lands before pb ships. Bounded — lift and reshape, not invent — but
  container's internals were not designed as a library boundary for
  sandbox's `Spec`/`Tier` contract, so this is a refactor, not a move.
- container's own API changes (pre-v1, no installed base: clean break).

## Open questions

- Rootfs/mount grants in sandbox's `Spec`: what does "prepared rootfs +
  path grants" look like in the contract so all three FS projections can
  satisfy it?
- Does the lifted seccomp/caps/cgroups code keep container's defaults, or
  does sandbox define its own (plugin-appropriate: no network, read-only
  rootfs) with container overriding?
- ociplug's actual shape — still unfleshed; this note feeds it.
