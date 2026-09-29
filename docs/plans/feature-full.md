# Plan: feature-full

Spec: docs/specs/migrate.md (the tables and the mapped/unmapped
facts), docs/specs/generation.md, docs/specs/plugin-execution.md,
docs/specs/check-rules.md, docs/specs/module-lockfile.md,
docs/specs/dep-verbs.md, docs/specs/build.md; new documents for the
formatter and the language server

- [ ] 1. The migration corpus: a curated set of buf configurations
      under testdata, each named for the shape it exercises and
      written from scratch under example names (single module with
      the standard categories; multi-module v2 with per-module
      `ignore_only` and `except`; v1 with `buf.work.yaml`; remote
      plugins with and without versions; local plugins as a name and
      as argv; `inputs.paths` beside a second template; `managed`
      with prefix overrides and with `disable`; `include_imports` and
      `clean`; a dependency on a private registry; the `rpc_allow_*`
      options), a golden of `pb migrate`'s report per entry, the
      suite running migrate over all of them; the goldens start as
      the gap list
- [ ] 2. The plugin catalog: a new private repository
      greatliontech/pb-plugins publishing
      `ghcr.io/greatliontech/pb-plugins/<owner>/<plugin>:<version>`
      under buf's names — recipes from bufbuild/plugins at a pinned
      commit, an own recipe of the same shape the escape hatch; a
      per-architecture matrix on native runners (linux/amd64,
      linux/arm64) merged into a manifest list per tag; every image
      and list signed keyless under the release workflow's identity;
      a scheduled bump of the pinned commit opening a pull request
      for the versions the registry lacks, a published tag never
      rebuilt; the initial membership what the projects reference
      (protocolbuffers/go, grpc/go, connectrpc/go, bufbuild/es,
      bufbuild/connect-es, grpc/web, protocolbuffers/js,
      protocolbuffers/csharp, grpc/csharp). In pb: migrate's plugin
      table becomes the rename rule plus a committed copy of the
      catalog's names (versions passed through as spelled, a missing
      build surfacing at the first generate), a test holding the copy
      to the repository's catalog and the images reachable and
      signed; the trust policy's identity rule for the catalog
      documented
- [ ] 3. `local` plugins with argv: `local: [command, args...]` in the
      generation file, the sandbox spawning the command with its
      arguments, migrate mapping buf's list form
- [ ] 4. Generation targets: per-entry `files` globs over the
      workspace's module-relative paths (the glob machinery
      `overrides` has), per-entry `include_imports`, and `clean`
      emptying an entry's output directory of what generation wrote;
      migrate mapping `inputs.paths`, `include_imports` and `clean`,
      a second template becoming further entries
- [ ] 5. Computed overrides: managed mode's prefix and suffix class
      as declared derivations (`go_package` from a prefix and the
      module-relative directory, and the java, csharp, ruby, php
      analogues), spelled explicitly, never inferred; migrate mapping
      `go_package_prefix` and its kin
- [ ] 6. Rulesets decoupled from the build (the parked draft):
      ruleset imports `{path, version, alias}` in the lint file, rule
      names `<alias>:<id>`, a workspace module importable as a
      ruleset from the working tree, rule files with typed functions
      and per-file imports for functions, the lockfile's `rulesets`
      section, the dep verbs acting on ruleset imports, tidy leaving
      rulesets alone; migrate emitting the alias form; a clean break
      from path-qualified names and module-file ruleset declarations
- [ ] 7. Rule variants in buf-rules: the predicates the affected rules
      are built from exported as functions, buf's boolean shaping
      options (`rpc_allow_same_request_response`,
      `rpc_allow_google_protobuf_empty_requests`,
      `rpc_allow_google_protobuf_empty_responses`,
      `ignore_unstable_packages`) as distinct rules under their own
      ids and tags, migrate mapping each option to an exclude and an
      enable, the value-bearing options (`service_suffix`,
      `enum_zero_value_suffix`) unmapped facts naming the one-line
      rule a workspace ruleset declares
- [ ] 8. The formatter: a canonical protobuf format — buf's fixed
      style, no options — its own spec, `pb format` with `--diff`,
      `--exit-code` and `--write`, over the workspace's own files
- [ ] 9. `breaking.base.file: <path>`, a fourth form beside `ref`,
      `version` and `pinned`: the base read as the descriptor set
      `pb build` writes, bytes trusted by being named, never verified
      through the trust policy (the pinned form's role, stated in the
      clause); the alignment engine unchanged below the base's loader
- [ ] 10. Migrate closure: every golden of the corpus reports zero
      unmapped facts, and a local run over the real configurations
      reports the same, recorded per repository by name in the
      commit message; a shape the local run finds missing enters the
      corpus first
- [ ] 11. The language server: its own spec (the capabilities served,
      how a file outside the workspace is treated, how findings map
      to diagnostics), `pb lsp` over the compile and the lint engine
      — diagnostics, definition, hover, references over the build's
      descriptors — and document formatting through the formatter
- [ ] 12. The runner suite's kernel surfaces declared to gomutant's
      observation bracket where paths name them; what the bracket
      cannot name filed against gomutant with the measured residue
      (pulled forward when gomutant's campaign-cost issue resolves)
- [ ] 13. The docker runner runs each plugin container under a per-run
      cgroup parent and attributes a memory kill from the parent's
      `memory.events` delta, the kernel's hierarchical count outliving
      the container's release; the daemon's event the fallback where
      the host's cgroup tree is not pb's to read (a remote daemon, a
      daemon in a VM), the record stating which source spoke; the
      mechanism the user's fork between a per-user slice pool and an
      in-container static shim (docs/issues/docker-oom-event-lost.md
      holds the spike)
- [ ] 14. The runner suite's live memory case records its kill in every
      loaded run and no longer retries a lost event
- [ ] 15. Platforms, the portable half: the suite on macOS and Windows
      runners in continuous integration and cross-compiled release
      binaries with no C toolchain; the file rules witnessed there
      (case folding, the rename of an open file on windows, the user
      directories); ocifs's store and export on both in its own
      matrix; the local scheme's `.exe` resolution on windows
- [ ] 16. The sandbox's darwin row: the Seatbelt backend
      (sandbox-exec profile, rlimits, a process group), its probe
      reporting `OS` or `Minimal`, its `Reach`, on the macOS runners
- [ ] 17. The sandbox's windows row: the AppContainer and Job Object
      backend, its probe, its `Reach`, on the windows runners
- [ ] 18. The native runner for `local` plugins on darwin and windows
      over the sandbox's row, runner selection per platforms.md; the
      local-scheme-darwin issue closes; what only hardware or a
      Docker Desktop daemon can witness (the docker runner's live
      cases on darwin and windows) tracked to a machine reporting
      back
