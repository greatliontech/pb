# Plan: generation

Spec: docs/specs/generation.md, plugin-execution.md, provenance.md
(REQ-prov-exec-policy, REQ-prov-plugin-signature), module-lockfile.md
(REQ-lock-plugin-entry), module-resolution.md (REQ-resolve-synthesis)

- [ ] 1. Generation file: parse and validate `pb.gen.yaml` — discriminated
      plugin entries, root-relative `out`, `opt`, `overrides`
- [ ] 2. Compilation over the resolution driver: workspace modules and
      build-list archives through protocompile; synthesized-module
      include roots
- [ ] 3. Option overrides and `CodeGeneratorRequest` construction —
      declarative application, deterministic request
- [ ] 4. OCI plugin acquisition: manifest-list fetch, signature
      verification against the trust policy, platform-strict selection,
      digest pins, materialization through ocifs
- [ ] 5. Native runner: export to rootfs, container backend behind the
      runner seam, resource bounds, tier reporting, response authority,
      out containment — `pb gen` end to end
- [ ] 6. Runner selection and the docker runner: flag/env/config/
      capability default, no silent fallback, runner independence
- [ ] 7. `local` scheme: resolution, content-hash pins, policy gating,
      bounds
- [ ] 8. Plugin overrides: invocation-scoped substitution, lockfile
      untouched, policy gating
