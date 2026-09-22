# Plan: the migrate verb

Spec: docs/specs/migrate.md; the files it writes per module-file.md,
workspace.md, check-rules.md (the lint file), generation.md.

- [x] 1. The lint file's per-module selection: a `modules` map keyed
      by workspace-relative module directory, each entry's `enable`,
      `exclude` and `severity` replacing the root's for that module's
      files (check-rules.md amended)
- [x] 2. The buf configuration read: buf.yaml v1 and v2, buf.work.yaml,
      buf.gen.yaml v1 and v2, buf.lock v1 and v2, each under its own
      version, every unmodeled key an unmapped fact
- [x] 3. Modules and the workspace: the module path from `--module` or
      the repository's origin, a module file per module, the
      workspace file for several
- [x] 4. Dependencies: the dependency table's ten entries, each
      layout-verified, and `--dep`; declarations written for the tidy
      to version
- [x] 5. Lint and breaking: the lint file over the buf-rules ruleset,
      categories and ids as qualified tags and names, per-module
      sections as `modules` entries, ignores by path, the
      rule-shaping options unmapped; the suppression comments
      rewritten in place
- [x] 6. Generation: the plugin table over the forked plugin images
      and `--plugin`, local plugins, the declarative half of managed
      mode as overrides, the rest unmapped
- [x] 7. The verb: assembly, every unmodeled key of every buf file read
      an unmapped fact, the comments rewritten over the checked files,
      the tidy that ends it, the report and its exit status,
      `pb migrate` registered
- [x] 8. The direct construction fetches what its proof needs — refs for
      a listing, a tag's or a commit's tree at depth one, and for a
      pseudo-version the history that decides its base per
      REQ-resolve-pseudo-base, derived under the chunk's own gate —
      into a bare repository kept in the module cache and reused, no
      default depth; googleapis, grpc-gateway and grpc verified and
      restored to the table
- [ ] 9. Links carried: a module's file set records a symbolic link as
      git's entry of mode 120000 over the target path and a submodule
      as mode 160000 over its recorded commit id, the tree hash
      recomputing exactly in the origin's object format, none ever
      followed or fetched, extraction writing neither
      (module-archive.md's forbidden entries repealed; its file set,
      mode normalization, manifest, tree recompute, zip mode and
      materialization amended); protovalidate,
      protoc-gen-validate, envoy and xds verified and restored,
      opencensus entering with envoy
- [ ] 10. Private origins: HTTPS credentials for a host from the user's
      .netrc, a proxy's host included, and SSH through the agent for
      `git@host:` origins (module-resolution.md, module-proxy.md and
      user-config.md amended)
- [ ] 11. Plan close-out
