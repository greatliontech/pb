# Ecosystem: sibling repos and their roles

pb composes existing greatliontech repos rather than building isolation,
filesystems, or provenance from scratch. Maturity notes are as of 2026-08
and go stale fast — verify against the repos before relying on them.

## Roles

| Repo | Role for pb |
| --- | --- |
| [protocompile] (bufbuild) | Compiler foundation: parse, resolve, descriptors, position info. Apache-licensed and stable; the irony of building on bufbuild's compiler is accepted — forking concerns wait for an actual provocation. |
| `../skillset` | Sigstore provenance machinery (cosign/fulcio/rekor, trusted-root handling) proven in another tool; reuse for module and plugin verification. |
| `../sandbox` | **The trajectory-setter, and pb's day-one runner dependency.** Cross-platform, create-only, pure-Go sandbox: one process execed into a fresh isolated environment. Tier model (`Strong`/`OS`/`Minimal`/`None`) with honest tier *reporting* — pb consumes the reported tier, never assumes. Its Linux backend arrives by lifting container's proven machinery ([sandbox-consolidation.md](./sandbox-consolidation.md)). Built for `ociplug` (upcoming, unfleshed). |
| `../ocifs` | Linux: read-only FUSE union FS over OCI images — the plugin rootfs on Linux. |
| `../projfs-go` | Windows: ProjFS binding — the rootfs-projection equivalent. Most proven of the three FS repos (extensive test surface, spec'd callback contract). Requires the ProjFS optional Windows feature enabled. |
| `../fskit-go` | macOS: FSKit binding. Portable core done; Tier-2 open question — whether a *third-party* signed appex actually gets dispatched. macOS 15.4+, signed/notarized appex, user enablement: real distribution friction. |
| `../container` | Full OCI runtime. Its create-path machinery (rootfs pivot, mounts, seccomp, caps, cgroups) is being lifted into `sandbox` per the adopted consolidation ([sandbox-consolidation.md](./sandbox-consolidation.md)); container re-bases on sandbox and keeps join/exec/lifecycle. Fallback if sequencing changes: pb uses container's non-cgo build directly as the first Linux backend (create-only subset; exec-into-running never called) — pb's runner contract is identical either way. |
| `../pbr` | Today: a buf-compatible registry (workaround for BSR restrictions). In pb's model the registry concept dissolves; pbr plausibly becomes the proxy implementation plus a buf-compat facade for migration. Its long-term shape is an open question owned by that repo. |

## Platform trajectory

Two distinct problems, only one of which the sandbox stack solves:

1. **Isolation off-Linux — solved in trajectory.** Linux namespaces
   (`Strong`), Windows AppContainer / macOS Seatbelt (`OS`). Real OS
   boundaries; `OS` tier is a fine default for plugin execution.
2. **Executing the plugin binary off-Linux — not a sandbox problem.** The
   existing plugin image ecosystem is overwhelmingly linux/amd64 ELF; no
   sandbox tier makes a Linux binary run on macOS. Resolved by the strict
   platform rule (see [plugin-execution.md](./plugin-execution.md)): authors
   ship multi-platform manifests or their image refuses to run off-Linux.
   Go-based plugins cross-compile near-free, so multi-platform coverage of
   the common ecosystem is cheap.

Consequence: **pb v1 ships Linux-first** (`Strong` tier reference behavior);
Windows/macOS arrive as sandbox backends and FS projections mature behind the
same interface. Off-Linux onboarding friction (enable ProjFS feature, approve
FSKit extension) is real and belongs in the platform docs when those land.

## Maturity snapshot (2026-08)

- `sandbox`: Linux backend is a spike (namespace core proven; rootfs/seccomp/
  caps/cgroups pending); Windows/darwin are skeletons.
- `projfs-go`: most mature; v0 but heavily tested with a documented contract.
- `fskit-go`: portable core complete; darwin binding registrable on CI; the
  signed-appex mount (the thing that proves dispatch) not yet demonstrated.
- `ociplug`: named in sandbox's README, not yet fleshed out.

[protocompile]: https://github.com/bufbuild/protocompile
