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
- **The runner targets `sandbox` from day one.** A protoc plugin is the
  ideal create-only sandbox tenant: one process, stdio, no exec-into-running,
  no lifecycle. pb's contract is "run this image's plugin process with this
  stdin, satisfying MinTier X" and pb consumes the *reported* tier — never
  assumes one. sandbox's Linux backend arrives by lifting container's proven
  machinery ([sandbox-consolidation.md](./sandbox-consolidation.md)); the
  image-mount side (ocifs / projfs-go / fskit-go) composes with sandbox at
  the ociplug layer. See [ecosystem.md](./ecosystem.md).
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
- Verification policy: `docs/specs/provenance.md` — one trust policy file
  (`pb.trust.yaml`) governs modules and plugin images alike; unsigned
  allowed-and-recorded by default, `require-provenance` opt-in.
- Default MinTier: `Strong` (core is Linux-only where namespaces afford
  it); unattainable tier fails loudly with the explicit-lowering path
  stated. Other platforms document their own defaults when they arrive.
- Generation attestation: deferred — recorded in
  [feature-set.md](./feature-set.md)'s deferred bucket.
