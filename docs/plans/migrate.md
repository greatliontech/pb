# Plan: the migrate verb

Spec: docs/specs/migrate.md; the files it writes per module-file.md,
workspace.md, check-rules.md (the lint file), generation.md.

- [ ] 1. The lint file's per-module selection: a `modules` map keyed
      by workspace-relative module directory, each entry's `enable`,
      `exclude` and `severity` replacing the root's for that module's
      files (check-rules.md amended)
- [ ] 2. The buf configuration read: buf.yaml v1 and v2, buf.work.yaml,
      buf.gen.yaml v1 and v2, buf.lock v1 and v2, each under its own
      version, every unmodeled key an unmapped fact
- [ ] 3. Modules and the workspace: the module path from `--module` or
      the repository's origin, a module file per module, the
      workspace file for several
- [ ] 4. Dependencies: the dependency table's ten entries, each
      layout-verified, and `--dep`; declarations written for the tidy
      to version
- [ ] 5. Lint and breaking: the lint file over the buf-rules ruleset,
      categories and ids as qualified tags and names, per-module
      sections as `modules` entries, ignores by path, the
      rule-shaping options unmapped; the suppression comments
      rewritten in place
- [ ] 6. Generation: the plugin table over the forked plugin images
      and `--plugin`, local plugins, the declarative half of managed
      mode as overrides, the rest unmapped
- [ ] 7. The verb: assembly, the tidy that ends it, the report and its
      exit status, `pb migrate` registered
- [ ] 8. Plan close-out
