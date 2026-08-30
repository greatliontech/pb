# Plugin execution

Plugins run locally, in an OS-enforced sandbox, from plain OCI images. This
replaces buf's remote plugin execution: hermetic, versioned, offline-capable,
and schemas never leave the machine.

## Settled direction

- **A plugin is a plain multi-platform OCI image with an entrypoint that
  speaks the standard protoc plugin protocol** (CodeGeneratorRequest on
  stdin, CodeGeneratorResponse on stdout). There is no pb-native image
  format. Metadata (OCI annotations, e.g. a default-options hint) may be
  parsed for convenience but is never required. Everything load-bearing
  already has a standard home: identity/pinning = OCI digest, platform
  selection = manifest list, provenance = sigstore signature over the digest,
  behavioral contract = the plugin protocol itself.
- **Strict platform rule: no matching platform in the manifest list → refuse
  to run.** No emulation fallback, no accommodation. The manifest list is the
  author's declaration of what they support; pb believes it and errors
  clearly, attributing the gap to the image, not to pb. Plugin authors are
  responsible for platform support. This also removes the micro-VM sandbox
  tier from pb's critical path — if it ever lands in `sandbox` it's a bonus,
  not a dependency.
- **The native runner is `container` behind pb's runner seam.** A protoc
  plugin is the ideal create-only tenant: one process, stdio, no
  exec-into-running, no lifecycle. pb's contract is "run this image's
  plugin process with this stdin, satisfying the tier floor" and pb
  consumes the *reported* tier — never assumes one. The rootfs is an
  ocifs `Export` (no FUSE on the critical path); sandbox is parked and
  ociplug is not in pb's path ([sandbox-consolidation.md](./sandbox-consolidation.md),
  [ecosystem.md](./ecosystem.md)).
- **The sandbox is a differentiator, not plumbing.** A signed plugin image
  running with no network and a read-only rootfs is a security story neither
  buf remote execution nor bare protoc-plugin installs can match. One
  provenance model (sigstore) covers both modules and plugins.
- **Official convenience images, zero privileged treatment.** Possibly fork
  bufbuild/plugins (Apache-2.0) for its catalog and build automation, publish
  multi-platform signed images. But the tool gives them no default registry
  fallback and no known namespace — they are just images the project happens
  to publish and sign. Expected split: Go-based plugins (most of the modern
  ecosystem) cross-compile to darwin/windows near-free; C++-based ones
  (protoc itself, grpc C++ family) stay linux-only manifests, which the
  strict platform rule handles honestly.
- **Existing images work where their platforms match.** buf's published
  plugin images are already OCI images with entrypoints — they qualify on
  linux today with no bridging.

## Closed — specced

- Consumption contract: `docs/specs/plugin-execution.md` (plain-OCI, no
  privileged source, digest pinning via the lockfile's `plugins` entries,
  strict platform refusal, verify-before-run, sandboxed single process,
  response authority).
- Backend taxonomy: identity schemes (`oci` default with the full
  guarantee set; `local` host binaries as an explicit policy-gated
  downgrade, content-hash pinned by default; `remote` reserved as its
  own trust category — it ships descriptors off-machine) are per-entry
  and committed; runners (`native`, `docker`) are machine-scoped
  mechanism, never committed, capability-defaulted, no silent fallback.
  Generated output is runner-independent by contract. Core does pin,
  verification, and platform checks off the manifest; runners only
  execute — blob acquisition may ride the daemon (`docker pull` by
  digest) as machine-scoped opt-in because content addressing makes the
  byte path trust-neutral.
- Dev loop: no committed dev-mode entries — an invocation-scoped
  `--plugin-override` substitutes content behind a declared ref, loudly,
  with the lockfile untouched in both directions.
- Verification policy: `docs/specs/provenance.md` — one trust policy file
  (`pb.trust.yaml`) governs modules and plugin images alike; unsigned
  allowed-and-recorded by default, `require-provenance` opt-in. Its
  `execution` block is the committed home of the whole plugin-execution
  posture: tier floor, permitted schemes, local-pin opt-out, override
  permission, resource ceilings. Generation configuration (`pb.gen.yaml`)
  carries none of it.
- Default MinTier: `Strong` (core is Linux-only where namespaces afford
  it); unattainable tier fails loudly with the explicit-lowering path
  stated. Platform scope: Linux (native+docker) and macOS (docker);
  Windows is a named non-goal with WSL2 as the sanctioned route.
- Generation attestation: deferred — recorded in
  [feature-set.md](./feature-set.md)'s deferred bucket.
