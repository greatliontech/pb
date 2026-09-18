# Ecosystem: sibling repos and their roles

pb composes existing greatliontech repos rather than building isolation,
filesystems, or provenance from scratch. Maturity notes are as of 2026-08
and go stale fast — verify against the repos before relying on them.

## Roles

| Repo | Role for pb |
| --- | --- |
| [protocompile] (bufbuild) | Compiler foundation: parse, resolve, descriptors, position info. Apache-licensed and stable; the irony of building on bufbuild's compiler is accepted — forking concerns wait for an actual provocation. |
| `../skillset` | Sigstore provenance machinery proven in another tool, and the architectural template for pb's provenance: gitsign-signed git objects with embedded Rekor proofs, verified fully offline against a pinned TUF trusted root. Settled: extract its provenance core as a shared library both tools consume; the extraction is the moment to lift its SHA-1-only fail-closed scope (SHA-256 object-format repos). |
| `../sandbox` | **Parked.** A create-only, cross-platform sandbox contract (tier reporting, `MinTier` refusal) at spike level: namespace core only, no rootfs/seccomp/caps/cgroups. No consumer needs it today — pb and ociplug both run on `container`. Its one load-bearing idea, the tier contract, is lifted into `container` instead of container's machinery being lifted into it; sandbox becomes worth building when a consumer with a genuine cross-platform requirement appears. |
| `../ocifs` | **The layer-semantics owner and pb's image store.** Specced (store, layer semantics, export, projection, writable, verification seam, api) with gmdb bookkeeping and GC. pb consumes it as a library: acquire by digest with explicit platform (pb verifies manifest-list signatures itself and plugs in through the seam), then `Export` — the unified view materialized into a content-addressed, immutable rootfs directory. FUSE is not on pb's path (CI containers often lack `/dev/fuse`); the mount stays ocifs's other consumer surface. The writable layer + commit is the road to native image building in pb. ocifs-side addition pb wants: export from an already-pulled image, so acquisition resolves once. |
| `../projfs-go` | Windows: ProjFS binding — the rootfs-projection equivalent. Most proven of the three FS repos (extensive test surface, spec'd callback contract). Requires the ProjFS optional Windows feature enabled. |
| `../fskit-go` | macOS: FSKit binding. Portable core done; Tier-2 open question — whether a *third-party* signed appex actually gets dispatched. macOS 15.4+, signed/notarized appex, user enablement: real distribution friction. |
| `../container` | **pb's native runner backend.** Full pure-Go create path (`/proc/self/exe` re-exec, `SysProcAttr` clone; cgo only for exec-into-running, which pb never calls): pivot_root, capability drop, seccomp, minimal `/dev`, cgroup v2 memory/cpu/pids with OOM notification, masked/readonly paths, all namespaces. pb's runner seam wraps it: `Root` = the ocifs export, read-only, no network, stdio only, bounds from the trust policy. Container-side additions pb still needs: an applied-mechanism/tier report (the sandbox contract lifted here), a pivot that needs no scratch directory inside the new root (`pivot_root(".", ".")` — pb binds a shared read-only export as the root), the payload's wait status and cgroup path as API rather than error text, vacating a delegated cgroup (moving the caller into a leaf child) before enabling controllers so rootless and in-container placement works, and dropping its stale ocifs example pin; the cgroup-delegation behavior landed (`CgroupsAvailable`, `CgroupsRequired`, rootless delegated-subtree placement). |
| `../ociplug` | gRPC-over-unix-socket plugin system (stdio handshake, mTLS, manifest permissions). **Not in pb's path**: pb plugins speak the stdio protoc protocol. ociplug consumes ocifs and container like pb does; its `internal/verify` (sigstore-go, cosign discovery) is the cosign verifier that plugs into ocifs's seam for consumers who want it — pb never depends on it. |
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

- `sandbox`: parked at spike level (see the roles table).
- `ocifs`: specced and landed — store on gmdb with GC, export, seam,
  writable layer with acceptance workloads.
- `container`: pure-Go create path complete with an OCI runtime-spec
  compliance tracker; pb's native backend.
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
