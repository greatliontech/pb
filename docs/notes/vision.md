# Vision

pb is a protobuf build tool in the space `buf` occupies — compile, generate,
manage dependencies — built on [protocompile] and designed around two
replacements for the parts of the buf ecosystem that are structurally coupled
to the BSR:

1. **Dependency management in the style of Go modules.** Git is the origin,
   a dumb stateless proxy caches in the middle, integrity comes from content
   hashes and sigstore provenance — not from trusting a registry.
2. **Local plugin execution.** Plugins are plain multi-platform OCI images run
   locally in an OS-enforced sandbox — not remote execution on someone else's
   infrastructure.

Lint, breaking-change detection, LSP, and MCP are explicitly later
implementation phases, though the lint/breaking concept is settled — pb ships
the engine, all rules are CEL, rulesets are ordinary modules
([lint-breaking.md](./lint-breaking.md)). protocompile provides the
foundation for all of them (full position info, AST access), so deferring
them costs nothing architecturally.

## The core inversion

The deepest problem with the BSR is not that it is the only registry — it is
that buf's design requires a registry at all. The Go module model dissolves
the registry:

- The **origin** is a git repo. Module identity is a path — a host followed
  by at least one path segment — resolved at the source.
- The **proxy** is a dumb cache: no accounts, no push, no admin. Anyone can
  run one; availability and speed are its only jobs.
- **Integrity** does not depend on the intermediary: a documented canonical
  archive hash plus sigstore signatures verify against the origin identity,
  so a proxy cannot tamper.

This inverts pbr's role: today pbr is "a BSR you can self-host"; in this model
pbr plausibly survives as the proxy implementation plus a buf-compat facade
for migration, but the registry concept itself mostly evaporates. See
[ecosystem.md](./ecosystem.md).

## No pb-native anything

A load-bearing design principle, settled early: **pb defines consumption
contracts, not formats.** The moment pb defines a proprietary format — a
plugin packaging scheme, a privileged registry, a special namespace — it
creates a spec the ecosystem must target and pb must maintain forever: the
exact failure mode being escaped. Instead:

- A plugin is any multi-platform OCI image whose entrypoint speaks the
  standard protoc plugin protocol. No required metadata.
- Official convenience images (see [plugin-execution.md](./plugin-execution.md))
  get zero privileged treatment in the tool — no default registry fallback,
  no known namespace. A plugin reference is a plugin reference.
- Compatibility with buf configs is a one-shot `pb migrate` import, not
  runtime compatibility — runtime compat would inherit buf's churn.

## Why not just use buf

- Dependency management is coupled to the public BSR and only that; the local
  cache layout and hashing scheme are undocumented moving targets.
- Remote plugin execution sends your schemas to third-party infrastructure
  and is unavailable offline.
- pbr exists as a workaround for BSR restrictions, but remains hostage to
  buf's protocol and cache/hashing churn. pb is the proper replacement, not a
  better workaround.

[protocompile]: https://github.com/bufbuild/protocompile
