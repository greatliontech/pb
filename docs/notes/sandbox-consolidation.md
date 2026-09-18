# Sandbox consolidation: sandbox is pb's native backend

Direction revised again (2026-09-19). The August reversal parked
sandbox and lifted its tier contract into container; on 2026-09-01
sandbox was un-parked with a policy-level contract
(`../sandbox/docs/specs/sandbox.md`) and its own Linux mechanism
layer seeded from container's create path, maintained as an
independent replica (twin records: sandbox's
`docs/issues/linux-mechanism-replica.md`, container's
`docs/issues/sandbox-create-path-replica.md`; any kernel-rule fix
lands in both copies).

Read from first principles, sandbox's contract is pb's runner
contract, and container's is not:

- intent, never mechanism, in the request (Root as world-restriction,
  path grants, limits, `MinTier`); pb has nothing mechanism-shaped to
  say;
- tier derived from the row that fully applied, never asserted —
  REQ-plugin-reported-tier without pb deriving anything;
- the bounds accounting reported as a fact of the run — the mechanism
  clause of REQ-plugin-resource-bounds, owned by the backend;
- `MinTier` failing closed before exec; no timeout of its own (the
  wall clock is pb's); cancellation kills the whole tree by the
  strongest tie the host affords (pid namespace, `cgroup.kill`);
- a shared, read-only tree is a valid Root and is never written.

Everything pb had to build around container — deriving Strong,
parsing wait-error text, locating the run's cgroup, the pivot scratch
written into the shared export, the delegated-cgroup placement — is
either sandbox's stated obligation or its API. So the consolidation
runs the original way after all: **pb's native runner is sandbox**;
container stays the mechanism-level, Linux-only runtime it is,
independent by design, and leaves pb's dependency graph once the
runner moves.

## Layering for pb

```
pb (fetch manifest-list + verify + platform-strict)
  ──► ocifs (acquire by digest, Export → prepared rootfs dir)
  ──► sandbox (Root = the export, no grants, stdio, Limits, MinTier)
```

## Sandbox-side work pb's runner needs

Tracked in `../sandbox/docs/plans/strong-backend.md`: the Strong world
(landed), hardening (capability drop, seccomp, no_new_privs, network
denial), bounds with reported accounting — folding in the
delegated-cgroup placement rule container landed (vacate the
delegated cgroup into a leaf, enable controllers in one write) and
the honest CPU attribution (a namespace init never receives SIGXCPU)
— and row selection with derived tiers. pb's runner moves once the
Strong row is delivered whole.
