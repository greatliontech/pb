# Plan: feature-full

Spec: docs/specs/migrate.md (the tables and the mapped/unmapped
facts), docs/specs/generation.md, docs/specs/plugin-execution.md,
docs/specs/check-rules.md, docs/specs/module-lockfile.md,
docs/specs/dep-verbs.md, docs/specs/build.md; new documents for the
formatter and the language server

- [x] 1. The migration corpus: a curated set of buf configurations
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
- [x] 2. `pb plugin build`: the publisher of a plugin image, packaging
      only — per platform an entrypoint file and optionally a base
      image pinned by digest and extra files, one single-layer image
      per platform with the entrypoint set, a manifest list over
      exactly the platforms given, pushed to the reference named, the
      digest reported; compiling and signing the publisher's own
      (its own spec: the image's shape as build writes it, the
      immutability of what it pushes, the report)
- [x] 3. The plugin catalog: a new repository
      greatliontech/pb-plugins publishing
      `ghcr.io/greatliontech/pb-plugins/<owner>/<plugin>:<version>`
      under buf's names from recipes of its own, no Dockerfile and no
      container engine anywhere in its pipeline: a catalog naming
      each plugin's upstream, its recipe kind and the versions
      published, and a tool that plans, builds a platform tree per
      recipe kind, and publishes through `pb plugin build`; the kinds
      being a Go package cross-compiled to every platform, an npm
      package compiled to a standalone executable per platform, an
      upstream release's prebuilt executable per platform it ships,
      and a C++ target built by bazel on a runner of each platform,
      the Linux trees of the last three layered on a base image
      pinned by digest; the platforms each plugin serves stated per
      plugin in the catalog and visible in its list; every image and
      list signed keyless under the publish workflow's identity; a
      scheduled discovery of upstream versions opening a pull request
      for the versions the catalog lacks, a published tag never
      rebuilt; the initial membership what the projects reference
      (protocolbuffers/go, grpc/go, connectrpc/go, bufbuild/es,
      connectrpc/es, grpc/web, protocolbuffers/js,
      protocolbuffers/csharp, grpc/csharp). In pb: migrate's plugin
      table becomes the rename rule plus a committed copy of the
      catalog's names (versions passed through as spelled, a missing
      build surfacing at the first generate; a versionless reference
      the highest tag the registry lists), a test holding the copy to
      the repository's catalog and the images reachable and signed;
      the trust policy's identity rule for the catalog documented
- [x] 4. `local` plugins with argv: `local: [command, args...]` in the
      generation file, the sandbox spawning the command with its
      arguments, migrate mapping buf's list form
- [x] 5. Generation targets: per-entry `files` globs over the
      workspace's module-relative paths (the glob machinery
      `overrides` has), per-entry `include_imports`, and `clean`
      emptying an entry's output directory of what generation wrote;
      migrate mapping `inputs.paths`, `include_imports` and `clean`,
      a second template becoming further entries
- [x] 6. Computed overrides: managed mode's prefix and suffix class
      as declared derivations (`go_package` from a prefix and the
      module-relative directory, and the java, csharp, ruby, php
      analogues), spelled explicitly, never inferred; migrate mapping
      `go_package_prefix` and its kin
- [x] 7. Rulesets decoupled from the build (the parked draft):
      ruleset imports `{path, version, alias}` in the lint file, rule
      names `<alias>:<id>`, a workspace module importable as a
      ruleset from the working tree, rule files with typed functions
      and per-file imports for functions, the lockfile's `rulesets`
      section, the dep verbs acting on ruleset imports, tidy leaving
      rulesets alone; migrate emitting the alias form; a clean break
      from path-qualified names and module-file ruleset declarations
- [x] 8. Rule variants in buf-rules: the predicates the affected rules
      are built from exported as functions, buf's boolean shaping
      options (`rpc_allow_same_request_response`,
      `rpc_allow_google_protobuf_empty_requests`,
      `rpc_allow_google_protobuf_empty_responses`,
      `ignore_unstable_packages`) as distinct rules under their own
      ids and tags, migrate mapping each option to an exclude and an
      enable, the value-bearing options (`service_suffix`,
      `enum_zero_value_suffix`) unmapped facts naming the one-line
      rule a workspace ruleset declares
- [x] 9. The formatter: a canonical protobuf format — buf's fixed
      style, no options — its own spec, `pb format` with `--diff`,
      `--exit-code` and `--write`, over the workspace's own files
- [x] 10. `breaking.base.file: <path>`, a fourth form beside `ref`,
      `version` and `pinned`: the base read as the descriptor set
      `pb build` writes, bytes trusted by being named, never verified
      through the trust policy (the pinned form's role, stated in the
      clause); the alignment engine unchanged below the base's loader
- [x] 11. Migrate closure: every golden of the corpus reports zero
      unmapped facts, and a local run over the real configurations
      reports the same, recorded per repository by name in the
      commit message; a shape the local run finds missing enters the
      corpus first
- [x] 12. The plugin catalog's first cut: every plugin of buf's registry
      under protocolbuffers, grpc, connectrpc, bufbuild,
      grpc-ecosystem and pluginrpc, and community/planetscale-vtprotobuf
      with the other go and node community generators, that the four
      kinds build, each at upstream's latest release under buf's
      spelling — protoc's own generators
      (cpp, java, kotlin, objc, php, pyi, python, ruby) and grpc's
      C++ plugins (cpp, objc, php, python, ruby) by bazel as csharp
      is, the Go and npm ones cross-compiled, protocolbuffers/js from
      its release; pb's catalog copy grows with it; the
      plugin-catalog-growth issue closes but for grpc/java
- [x] 13. Platforms, the portable half: the repository made public at
      this chunk for the macOS and Windows runners (its history
      scanned once more for any private or customer name, the README
      the one word `wip`, no license yet); the suite on macOS and
      Windows runners in continuous integration and cross-compiled
      release binaries with no C toolchain; the file rules witnessed there
      (case folding, the rename of an open file on windows, the user
      directories); ocifs's store and export on both in its own
      matrix; the local scheme's `.exe` resolution on windows
- [x] 14. The sandbox's darwin row: the Seatbelt backend
      (sandbox-exec profile, rlimits, a process group), its probe
      reporting `OS` or `Minimal`, its `Reach`, on the macOS runners;
      the row admitting an entrypoint that executes a sibling of the
      image's tree, as a bundled runtime's launcher does
- [x] 15. The sandbox's windows row: the AppContainer and Job Object
      backend, its probe, its `Reach`, on the windows runners; the
      row admitting a sibling's execution as the darwin row does
- [x] 16. Runner selection per entry: the default choosing `native`
      where the image serves the host and the row meets the floor,
      `docker` for the rest where a daemon is reachable, a layer's
      runner binding every entry, the report naming each entry's
      runner (witnessed on Linux with an image serving another
      platform alone)
- [x] 17. The native runner on darwin and windows over the sandbox's
      row: `local` plugins under no floor, `oci` plugins from the
      image's entry for the host under the policy's lowering to `OS`,
      the row's world the export and the platform's system
      libraries, runner selection per platforms.md; the
      local-scheme-darwin issue closes; what only hardware or a
      Docker Desktop daemon can witness (the docker runner's live
      cases on darwin and windows) tracked to a machine reporting
      back
- [ ] 18. Campaign close-outs: the delta campaigns outstanding since
      the generation work — chunks 5 to 11 — the language server's
      (chunk 21, every package its commits touched: internal/lsp,
      internal/dep, internal/proto/compile, internal/proto/modfiles,
      internal/source/fetch, cmd/pb), the plugin catalog's (chunks 3,
      12, 19, 20, 24, 25, 26 and 27, in greatliontech/pb-plugins: internal/catalog,
      internal/recipe, internal/pipeline, internal/registry,
      internal/github, internal/web, internal/endpoints, cmd/catalog;
      pb's internal/migrate, cmd/pb's migrate verb,
      internal/plugin/oci) and the campaign over the check subsystem
      with `gitdir`, each run on a clean tree as an hours-class
      background run and its records banked and promoted, every open
      mutant dispositioned; the mutation-campaign-freshness-cost and
      check-campaign-evidence-machine-local issues close, and
      mutation-record-ledger-names-old-packages closes with them, the
      re-measurement under the new names the relation it missed,
      gomutant's retarget rewriting identity alone by design
- [x] 19. The release kind reads beyond GitHub releases: Maven Central
      for grpc/java's per-platform executables, grpc's binary host for
      grpc/node (grpc-tools' executable, numbered by grpc-tools, not
      by grpc); community/scalapb-scala from the native executables
      upstream ships (linux and darwin on amd64, the two it ships);
      the go kind building from a repository tag the module proxy
      lacks (community/chrusty-jsonschema's unprefixed tags,
      community/roadrunner-server-php-grpc's nested module); the
      plugin-catalog-growth issue closes
  - [x] 19.1. Triage gate.
  - [x] 19.2. The go kind from a repository tag: the version's tag
        resolved to its commit through the repository, the module
        fetched at that commit, a nested module's path carrying the
        version's major; discovery through the repository's
        releases; the catalog README's kinds table amended;
        community/chrusty-jsonschema and
        community/roadrunner-server-php-grpc enter.
  - [x] 19.3. The release kind's sources: an asset named by a URL
        beside a release's asset name — Maven Central's executables
        with their sha256 verified, grpc's binary host's archives —
        and discovery through Maven's metadata or the npm registry
        as the recipe says; the README's kinds table amended;
        grpc/java and grpc/node enter.
  - [x] 19.4. community/scalapb-scala enters from upstream's native
        executables at v0.11.17; the 0.11 line's later versions
        (v0.11.18 to v0.11.20) ship as the jar alone with no release,
        the 1.0 line's releases carry the executables again and
        enter with the bump once one is stable; the README's ScalaPB
        sentence says so.
  - [x] 19.5. Close-out: pb's catalog names grow by the five
        entrants (grpc/java, grpc/node, community/scalapb-scala,
        community/chrusty-jsonschema,
        community/roadrunner-server-php-grpc), the
        plugin-catalog-growth issue closes, consolidation, the
        campaign (slotted with chunk 18's where not run).
- [x] 20. The rust kind: a crate's executable installed by cargo on a
      runner of the platform itself, as the bazel kind builds — a
      cross toolchain for darwin and windows from one host needs SDKs
      and linkers the catalog does not carry — its versions
      crates.io's; connectrpc/rust
  - [x] 20.1. Triage gate.
  - [x] 20.2. The kind and connectrpc/rust, a recipe's probe
        parameter for a plugin that refuses to run without one; the
        README's kinds table amended.
  - [x] 20.3. The kind's Linux trees static: cargo's musl targets
        on the linux runners, the pipeline adding them beside the
        pinned toolchain, so a Linux entrypoint runs natively on an
        OS sandbox row, static as buf's is.
  - [x] 20.4. Close-out: pb's catalog names grow by connectrpc/rust,
        consolidation, the campaign (slotted with chunk 18's where
        not run).
- [x] 21. The language server: its own spec (the capabilities served,
      how a file outside the workspace is treated, how findings map
      to diagnostics), `pb lsp` over the compile and the lint engine
      — diagnostics, definition, hover, references over the build's
      descriptors — and document formatting through the formatter
  - [x] 21.1. Triage gate.
  - [x] 21.2. The spec (docs/specs/lsp.md): the session, documents
        as overlays, the diagnostics' content and freshness,
        navigation and hover, dependency files, formatting, the
        lifecycle and transport, the invariants.
  - [x] 21.3. `pb lsp` over the binding: the session loaded
        read-only and reloaded as the verbs load theirs, an unpinned
        requirement diagnosed, documents overlaid,
        the compile's errors and the lint's findings published as
        diagnostics with the client's version, a dependency's file
        addressed by the client's capability (served under the
        `pb-module` scheme, or as a file of a read-only copy
        extracted into the dependency source store, `pb clean
        --sources` emptying it), the lifecycle and the transport; the
        server package placed in the layering table; an in-process
        client suite with the parity, supersession and tree-digest
        witnesses and a dependency's address under each client
        capability; the verb documented.
  - [x] 21.4. The binding moved to greatliontech/lsp (its v0.1.2:
        a request whose context was asked for never recycled, a
        request method sent as a notification dropped): the server's
        own transport and dispatch workarounds removed — a malformed
        body answered, an unknown or undecodable notification dropped
        — the chain installed through the binding's option, its error
        observer logged; the pb witnesses of those behaviours kept
        and extended (an invalid-request body, a framing error, a
        request method as a notification).
  - [x] 21.5. Definition, hover and references over the resolved
        build's descriptors, the cursor mapped through the parser's
        tree.
  - [x] 21.6. Formatting into the canonical form through the
        formatter.
  - [x] 21.7. Close-out: consolidation, the spec's statements
        settled, the campaign.
- [ ] 23. The runner suite's kernel surfaces declared to gomutant's
      observation bracket where paths name them, a SandboxRunner.Run
      mutant then classified by its oracle; what the bracket cannot
      name filed against gomutant with the residue chunk 18 measured;
      the runner-oracle-sandbox-bound issue closes
- [x] 24. The swift kind: `swift build` on a runner of the platform,
      the platforms served those the toolchain builds; grpc/swift,
      grpc/swift-protobuf, connectrpc/swift, connectrpc/swift-mocks,
      bufbuild/connect-swift, bufbuild/connect-swift-mocks, apple/swift
  - [x] 24.1. Triage gate.
  - [x] 24.2. The kind and apple/swift: a SwiftPM product built at
        the repository's tag on the platform's runner with the
        toolchain the catalog pins, swiftly installing it on linux
        and darwin, the Linux trees static through swift.org's
        static Linux SDK (its checksum swift.org's own, read by
        `catalog sdk`), resolved to the package's lockfile where it
        commits one; windows not served (no upstream builds there);
        the README's kinds table amended.
  - [x] 24.3. The connect and grpc plugins: connectrpc/swift and
        swift-mocks, bufbuild/connect-swift and connect-swift-mocks
        at buf's last version from the moved repository, grpc/swift
        from the 1.x line, grpc/swift-protobuf from
        grpc-swift-protobuf.
  - [x] 24.4. Close-out: pb's catalog names grow by the seven,
        consolidation, the campaign (slotted with chunk 18's where
        not run).
- [x] 25. The dart kind: `dart compile exe` on a runner of the platform;
      protocolbuffers/dart, connectrpc/dart
  - [x] 25.1. Triage gate.
  - [x] 25.2. The kind and the two plugins: a package's script
        compiled to an executable at the repository's tag on the
        platform's runner with the Dart SDK the catalog pins, the
        runner installing it from Google's archive at the checksum
        published beside it, the Linux trees over the base (the
        runtime links the C library), every platform served; CI's
        live tests on the windows row too; the README's kinds table
        amended.
  - [x] 25.3. Close-out: pb's catalog names grow by the two,
        consolidation, the campaign (slotted with chunk 18's where
        not run).
- [x] 26. The jvm kind (community/scalapb-zio-grpc among its plugins,
      upstream shipping no native executable, only the jar; and
      community/scalapb-scala's v0.11.18 to v0.11.20, the jar alone
      where the releases around them ship the executable — the jar
      serving every version, as buf builds it, so the plugin moves
      to this kind whole and its published versions stand): a
      jlink'd runtime bundled in the image beside the jar, the
      image's entrypoint the argv `/jre/bin/java -jar <jar>` (the
      fork launch mechanism on it, the runtime's spawn helper being
      no file of a windows tree), the
      jar named relative to the working directory, the tree's root,
      since an OS row resolves the program in the tree and hands the
      arguments as spelled, as plugin-publish.md's build takes it,
      no launcher; grpc/kotlin,
      connectrpc/kotlin, bufbuild/connect-kotlin; bufbuild/validate-java,
      a Go generator upstream, by the go kind; the node kind's
      runtime-bundled variant in the same shape, for a package needing
      its files on disk or a native addon (bufbuild/knit-ts,
      community/stephenh-ts-proto)
  - [x] 26.1. Triage gate.
  - [x] 26.2. pb: an entrypoint argv with arguments across the
        runners, witnessed — the native row's checks over argv[0],
        the arguments carried, the docker runner's as the image
        declares — plugin-execution.md's rows read first, amended
        where a row's statement assumes a one-word entrypoint.
  - [x] 26.3. The catalog's jvm kind: a jar from Maven Central at
        its coordinates (group, artifact, classifier and extension,
        the file named by version; ScalaPB's a jar under `unix.sh`),
        verified against the digest Maven publishes beside it (its
        sha256 where it has one, its sha1 for every artifact), the
        runtime linked by jlink from Temurin's jmods for every
        platform on one host whose own jlink is the same pinned
        release's (the JDK the catalog pins, its assets at
        Adoptium's checksums), the tree `jre/` and the jar, the
        entrypoint argv with the jar relative to the root,
        processes forked rather than spawned through the runtime's
        helper, the Linux trees
        over the base (the runtime links the C library), the probe
        running the argv in the tree, versions from Maven's
        metadata; the README's kinds table amended, its Membership
        paragraph losing the launcher and the release kind's ScalaPB
        sentence, the catalog's ScalaPB comment with it;
        community/scalapb-zio-grpc, grpc/kotlin, connectrpc/kotlin,
        bufbuild/connect-kotlin (deprecated upstream, frozen at its
        last), community/scalapb-scala moved to the kind, v0.11.18
        to v0.11.20 entering, its published v0.11.17 standing.
  - [x] 26.4. bufbuild/validate-java by the go kind
        (cmd/protoc-gen-validate-java of the envoyproxy module at
        its tag, deprecated upstream, at v1.3.3; silent, generating
        for the import that carries PGV's options alone).
  - [x] 26.5. The node kind's runtime-bundled variant: node's own
        binary for every platform from nodejs.org at its published
        checksums, the package installed with its dependencies into
        the tree, the entrypoint argv `/node
        app/node_modules/<package>/<script>` with the script
        relative to the root, one host for every platform, the Linux
        trees over the base; bufbuild/knit-ts,
        community/stephenh-ts-proto (five platforms, dprint-node
        shipping no addon for windows/arm64); the README's node row
        amended.
  - [x] 26.6. Close-out: pb's catalog names grow by the seven,
        consolidation, the campaign (slotted with chunk 18's where
        not run).
- [x] 27. The python kind, the jvm kind's shape over a standalone
      interpreter (python-build-standalone's build per platform, the
      package installed per platform by uv under `app/`, the argv
      `/python/bin/python3 -I app/<entrypoint>.py`): bufbuild/py,
      bufbuild/grpc-py, connectrpc/py; connectrpc/python, a Go
      program shipped inside a wheel, by the go kind from the
      repository's tag, frozen; the catalog then holds buf's tiers
      one and two whole
- [x] 28. The docker runner's stream from the store's verified image:
      the daemon handed the image's own layers and configuration (an
      archive it loads, the docker-archive form under the daemon's
      classic image store, the OCI layout under its containerd
      store) instead of a tar of the export's tree, on
      every platform, so the modes and ownership the image declares
      reach the daemon byte for byte and the export tree leaves the
      daemon's path; plugin-execution.md's store byte path and
      platforms.md's windows refusal amended; the
      docker-store-path-windows issue closes, the windows row's
      protocol coverage restored
- [ ] 29. A replaced pair's files addressed by the pair whose bytes
      they are: the `pb-module://` address and the source store's
      copy named by the replacement pair where one applies, as Go's
      module cache and gopls have it, the requirement's name kept to
      the module graph (the module file; the unpinned diagnostics
      already name the source pair); `modfiles.Module` carrying both
      pairs, the requirement it stands for and the source whose
      bytes it holds, `Load` filling both from the root's
      replacements, every name of bytes read from the source pair
      and every name of a requirement from the other; a replaced
      module rendered once, `<path>@<version> => <replacement>` as
      dep-verbs.md spells it, in `Module.Label` and `dep.Pair`
      (today two renderings of a pair, the directory form
      workspace.md's), workspace.md REQ-work-replace stating the
      rendering beside the directory form, the export report
      carrying it; lsp.md REQ-lsp-dependency-files amended; a
      witness that changes a pinned replacement and sees the address
      move with the bytes while the requirement's name stays; the
      lsp-replacement-address issue closes
- [ ] 30. The identity audit: every name a layer hands out for bytes
      walked under the identity-audit issue's lenses across the
      resolver, the stores, the lockfile and provenance keys, the
      language server's addresses and the plugin references; each
      finding a defect fixed here or an issue slotted; the Go
      parallels note extended with what the walk finds; the
      identity-audit issue closes
