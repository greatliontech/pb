# Plan: generation

Spec: docs/specs/generation.md, plugin-execution.md, provenance.md
(REQ-prov-exec-policy, REQ-prov-plugin-signature), module-lockfile.md
(REQ-lock-plugin-entry), module-resolution.md (REQ-resolve-synthesis)

- [x] 1. Generation file: parse and validate `pb.gen.yaml` — discriminated
      plugin entries, root-relative `out`, `opt`, `overrides`
- [x] 2. Compilation over the resolution driver: workspace modules and
      build-list archives through protocompile; synthesized-module
      include roots
- [x] 3. Option overrides and `CodeGeneratorRequest` construction —
      declarative application, deterministic request
- [x] 4. OCI plugin acquisition: manifest-list fetch, signature
      verification against the trust policy, platform-strict selection,
      digest pins, materialization through ocifs
- [x] 5. Native runner: export to rootfs, container backend behind the
      runner seam, resource bounds, tier reporting, response authority,
      out containment — `pb generate` end to end
- [ ] 6. Native runner over sandbox: the runner seam's Spec mapped to
      intent (the export as `Root`, no grants, the policy's limits, the
      tier floor as `MinTier`), tier and bounds accounting consumed as
      reported, container out of the dependency graph; the three
      container-bound issues close
- [ ] 7. Runner selection: flag over environment over user configuration
      over the capability default, an unavailable selected runner fails
      naming it, no silent fallback — the seam `pb generate` picks its
      runner through, with the native runner its only member
- [ ] 8. Docker runner: verified store content into the daemon by a
      trust-neutral byte path, `--network none --read-only` plus the
      bounds, tier and bounds mechanism derived from the daemon's own
      record of the created container, contract tests over a fake
      `docker` on PATH and a live test where a daemon exists; runner
      independence becomes falsifiable
- [ ] 9. `local` scheme: PATH and root-relative resolution, content-hash
      pins keyed by host platform, policy gating, bounds without a
      sandbox (tier `None`)
- [ ] 10. Plugin overrides: invocation-scoped substitution from an OCI
      layout or archive, lockfile untouched in both directions, policy
      gating, reported on standard error
