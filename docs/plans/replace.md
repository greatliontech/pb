# Plan: the replace directive

Spec: docs/specs/module-file.md (the directive's form), docs/specs/
module-resolution.md (the graph read through a replacement), docs/
specs/workspace.md (a directory's content as the workspace's own)

- [ ] 1. The path form: `replace X => Y` in the module file, Y fetched,
      verified and pinned as any module and judged under its own
      path; the requirement graph and tidy read through the
      replacement; the resolution root's own module files never carry
      one, and publishing ignores it (spec clauses, module-file
      grammar, resolution, tidy, the fork fixture from
      docs/issues/replace-directive.md as the anchor case)
- [ ] 2. The directory form: `replace X => ./dir`, the directory read
      as a workspace module is — its module file declaring its
      requirements, no pin recorded, the workspace's trust covering
      it, every version of X replaced; the lockfile's and the trust
      policy's reading stated in the spec
