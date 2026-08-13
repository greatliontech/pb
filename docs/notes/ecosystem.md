# Ecosystem: sibling repos and their roles

pb composes existing greatliontech repos rather than building isolation,
filesystems, or provenance from scratch. Maturity notes are as of 2026-08
and go stale fast — verify against the repos before relying on them.

## Roles

| Repo | Role for pb |
| --- | --- |
| [protocompile] (bufbuild) | Compiler foundation: parse, resolve, descriptors, position info. Apache-licensed and stable; the irony of building on bufbuild's compiler is accepted — forking concerns wait for an actual provocation. |
| `../skillset` | Sigstore provenance machinery proven in another tool, and the architectural template for pb's provenance: gitsign-signed git objects with embedded Rekor proofs, verified fully offline against a pinned TUF trusted root. Settled: extract its provenance core as a shared library both tools consume; the extraction is the moment to lift its SHA-1-only fail-closed scope (SHA-256 object-format repos). |
| `../sandbox` | **The trajectory-setter — but not a pb blocker.** Cross-platform, create-only, pure-Go sandbox: one process execed into a fresh isolated environment. Tier model (`Strong`/`OS`/`Minimal`/`None`) with honest tier *reporting* — pb consumes the reported tier, never assumes. pb's native runner lands on `container` today; sandbox arrives later behind the same seam once container's machinery is lifted into it ([sandbox-consolidation.md](./sandbox-consolidation.md)). Built for `ociplug` (out of pb's path — see below). |
| `../ocifs` | **The one home of OCI layer semantics, and pb's native-runner rootfs supplier.** Store (pull/cache/unpack, digest-addressed entry with explicit platform — pb hands it a digest it already verified; blob transport by content address is trust-neutral), Unify (whiteout-correct flattening), and Export (prepared rootfs directory) — pb never parses a layer tar. The read-only FUSE mount is ocifs's *other* consumer path (ociplug, CLI); FUSE is not on pb's critical path (CI containers often lack `/dev/fuse`). Specs live in ocifs `docs/specs/` (stipulator corpus). |
| `../projfs-go` | Windows: ProjFS binding — the rootfs-projection equivalent. Most proven of the three FS repos (extensive test surface, spec'd callback contract). Requires the ProjFS optional Windows feature enabled. |
| `../fskit-go` | macOS: FSKit binding. Portable core done; Tier-2 open question — whether a *third-party* signed appex actually gets dispatched. macOS 15.4+, signed/notarized appex, user enablement: real distribution friction. |
| `../container` | Full OCI runtime, and **pb's native-runner isolation layer today**: the settled chain is pb (fetch + verify, platform-strict) → ocifs (store + Unify + Export → prepared rootfs dir) → container (namespaces, pivot ro, cgroups, exec stdio), create-only subset with exec-into-running never called. Its create-path machinery is being lifted into `sandbox` per the adopted consolidation ([sandbox-consolidation.md](./sandbox-consolidation.md)); when that lands, sandbox slots in behind the same seam — pb's runner contract is identical either way, and the lift is not a pb blocker. |
| `../pbr` | Today: a buf-compatible registry (workaround for BSR restrictions). Settled future roles: implements the pb proxy protocol, serves as the BSR protocol bridge (repacking BSR-only modules into canonical archives) so pb itself never learns anything about BSR, and its hosted instance at **pbr.dev** is the recommended opt-in public proxy (deliberately not a default; see the dependency-management note for the revisit trigger). See [dependency-management.md](./dependency-management.md). Execution owned by that repo. |

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

Consequence (per the committed platform scope in
[plugin-execution.md](../specs/plugin-execution.md)): **pb ships linux +
darwin** — the container runner on Linux, the docker runner covering darwin —
and **Windows is a named non-goal** (WSL2 covers it). The ProjFS/FSKit
projections are not in pb's path; they remain ecosystem roles for other
consumers (ociplug, direct mounts). Rootfs materialization for pb is ocifs
Export (a plain directory), not a FUSE/projection mount — CI containers often
lack `/dev/fuse`, and export needs no mount at all.

## Maturity snapshot (2026-08)

- `sandbox`: Linux backend is a spike (namespace core proven; rootfs/seccomp/
  caps/cgroups pending); Windows/darwin are skeletons.
- `projfs-go`: most mature; v0 but heavily tested with a documented contract.
- `fskit-go`: portable core complete; darwin binding registrable on CI; the
  signed-appex mount (the thing that proves dispatch) not yet demonstrated.
- `ociplug`: mature architecture doc; **out of pb's path** (decided
  2026-08) — it is a gRPC-over-unix-socket plugin system
  (go-plugin-shaped, mTLS handshake), the wrong protocol for pb's
  stdin/stdout protoc plugins. It stays an ocifs FUSE-mount consumer,
  and its `internal/verify` (cosign/sigstore-go) is the extraction
  candidate for a standalone verifier module that plugs ocifs's
  verification seam — pb uses neither (gitprov machinery instead).

[protocompile]: https://github.com/bufbuild/protocompile
