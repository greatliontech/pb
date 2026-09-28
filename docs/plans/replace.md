# Plan: the replace directive

Spec: docs/specs/workspace.md (the directive's form, in the workspace
file), docs/specs/module-resolution.md (the graph read through a
replacement), docs/specs/dep-verbs.md (the verbs over a replaced path)

- [x] 1. The path form: `replace: {X: Y@v}` in the workspace file, Y
      fetched, verified and pinned as any module and judged under its
      own path; the requirement graph, tidy, download and update read
      through the replacement; module files never carry one, and
      publishing ignores it (spec clauses, workspace-file grammar,
      resolution, the verbs, the fork fixture from
      docs/issues/replace-directive.md as the anchor case)
- [ ] 2. The directory form: `replace: {X: ./dir}`, the directory read
      as a workspace module is — its module file declaring its
      requirements, no pin recorded, the workspace's trust covering
      it, every version of X replaced; the lockfile's and the trust
      policy's reading stated in the spec; the verbs' rendering of a
      replaced path settled for both forms
      (docs/issues/replace-verbs-render-the-pair.md,
      docs/issues/replace-reader-by-construction.md)
