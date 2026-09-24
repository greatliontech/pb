# Plan: a descriptor set as the breaking base

Spec: docs/specs/check-rules.md (the lint file's `breaking.base`)

- [ ] 1. `breaking.base.file: <path>`, a fourth form beside `ref`,
      `version` and `pinned`: the base read as the descriptor set
      `pb build` writes, bytes trusted by being named, never verified
      through the trust policy (the pinned form's role, stated in the
      clause); the alignment engine unchanged below the base's loader
      (spec clause, lint file schema, loader, tests over a set the
      build verb wrote)
