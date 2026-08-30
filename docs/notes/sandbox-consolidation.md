# Sandbox consolidation: superseded — the tier contract moves to container

Direction revised (2026-08). The original plan lifted container's
create-path machinery into sandbox so sandbox became the shared
create-only core. Reading both repos settled it the other way:

- container's create path is already pure Go (the cgo seam is exactly
  where the original note put it — joining, never creating) and
  carries everything pb's runner contract needs: pivot_root, caps,
  seccomp, minimal /dev, cgroup v2 bounds, masked paths, namespaces.
- sandbox is a spike whose `Spec` is narrower than container's
  `Config` (rlimit-based memory caps, no seccomp/caps surface), so a
  lift would first have to redesign the target contract.
- Neither real consumer points at sandbox: pb and ociplug both run on
  container.

So the consolidation runs in the cheap direction: **sandbox's one
load-bearing idea — honest tier reporting and `MinTier` refusal — is
lifted into container as a small API**, and sandbox is parked until a
consumer with a genuine cross-platform requirement exists. If that day
comes, "container's create path extracted behind sandbox's contract"
remains available, cheaper then, with the tier surface already in
place.

## Layering for pb

```
pb (fetch manifest-list + verify + platform-strict)
  ──► ocifs (acquire by digest, Export → prepared rootfs dir)
  ──► container (namespaces, pivot ro, cgroups, exec with stdio)
```

ociplug is out of pb's path (socket-shaped transport; pb plugins are
stdio). FUSE is off pb's critical path (export, not mount). See
[ecosystem.md](./ecosystem.md) and `docs/specs/plugin-execution.md`.

## Container-side work pb's runner needs

- Applied-mechanism / tier report (`Strong` derived from what actually
  applied, never assumed).
- Defined behavior when cgroup v2 delegation is unavailable — fail
  naming the gap, or a reportable degraded-bounds state; pb's spec
  treats bounds as mandatory, so silence is not an option.
- Drop or update the stale ocifs example pin in container's go.mod.
