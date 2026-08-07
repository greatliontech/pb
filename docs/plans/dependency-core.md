# Plan: dependency core

Spec: docs/specs/module-archive.md, module-file.md, module-lockfile.md,
module-resolution.md, module-proxy.md, provenance.md, workspace.md

- [x] 1. Canonical manifest & digest: file-set validation, manifest
      rendering, `pb1:` digest
- [x] 2. ZIP wire container: produce and verify against a digest
- [x] 3. Git tree recomputation & binding (SHA-1 and SHA-256)
- [ ] 4. Module file: parse, validate, canonical emission
- [ ] 5. Lockfile: model, canonical emission, pin enforcement
- [ ] 6. Versions & selection: version syntax, pseudo-versions, MVS,
      determinism
- [ ] 7. Origin resolution: path split, vanity redirect, probing, tag
      listing, synthesis
- [ ] 8. Proxy client: escaping, endpoints, source list, fall-through,
      verification wiring
- [ ] 9. Direct source: git-backed fetching, proxy equivalence
- [ ] 10. Provenance verification core (shared-library extraction
      coordinated with skillset; pb-side interface first)
- [ ] 11. Trust policy evaluation & lockfile provenance records
- [ ] 12. Workspace resolution root
- [ ] 13. dep verbs: init, tidy, download, update, graph, why, verify
