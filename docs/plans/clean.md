# Plan: the clean verb

Spec: docs/specs/dep-verbs.md (the cache's layout and transparency),
docs/specs/plugin-execution.md (the plugin store and its evidence)

- [ ] 1. `pb clean` empties the cache whole — module archives, the
      plugin store's images and the evidence beside them — the next
      resolve refetching and re-verifying from the lockfile's pins;
      `--modules` and `--plugins` narrow it to one store, evidence
      always going with the plugin store; the lint file, the lockfile
      and the trusted root never touched (spec clause, verb, tests)
