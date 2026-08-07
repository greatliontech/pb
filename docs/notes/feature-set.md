# Feature set

Settled (decisions accepted 2026-08). The command surface sorted into core /
deferred / hard no — deferred means on-record and later, never silently cut;
hard no means excluded by design with the reason stated.

**Core is Linux-only.** Windows/macOS arrive behind the same runner contract
as sandbox backends mature ([ecosystem.md](./ecosystem.md)); nothing else in
the tool is platform-conditional.

## Core

- **`pb build`** — compile via protocompile, output FileDescriptorSet. The
  primitive everything else consumes (generate, lint, breaking, interop).
- **`pb generate`** — plugins from OCI images, sandboxed, strict platform
  rule ([plugin-execution.md](./plugin-execution.md)).
- **Lint / breaking** — the CEL engine ([lint-breaking.md](./lint-breaking.md)).
  Core, not a later phase.
- **Dependency commands, Go-shaped** — `init`, `tidy` (scan proto imports,
  add missing / drop unused requirements), `update`, `download`, `graph`,
  `why`, `verify` (re-check digests + provenance of the whole graph against
  the lockfile). ([dependency-management.md](./dependency-management.md))
- **Workspaces** — multi-module monorepos with local-resolution semantics
  (go.work-shaped). Core because monorepos are the dominant proto reality
  and bolting workspaces on later warps every resolution decision made
  without them. Shape TBD in the spec.
- **`pb migrate`** — one-shot buf config import: dep mapping table, lint
  mapping table, config rewrite. Never runtime compatibility.
- **Declarative option overrides in generation config** — the slim core of
  buf's "managed mode": per-package/per-file option values (`go_package`
  et al.) applied at generate time. The heuristic magic of full managed mode
  stays out; the declarative form is near-mandatory for Go codegen against
  third-party protos.
- **`pb export`** — flatten module + deps into a plain include directory for
  protoc/other-tool interop. The escape hatch that proves no lock-in.
- **`pb attest`** (name TBD) — author-side signing helper: build the
  canonical archive for a tag, produce the sigstore bundle, run in CI.
  Provenance doesn't become an ecosystem norm unless producing bundles is
  one command. Open question it surfaces: **where bundles live at origin**
  (the proxy serves `@v/<version>.bundle` — from what upstream source: git
  ref/notes, release asset, OCI registry?). Settle in the archive/proxy
  spec.
- **Trivial utilities** — `ls-files`, cache path/clean, `version`.

## Deferred

- **LSP, MCP** — consume the same engines (compiler diagnostics, lint);
  later phases.
- **`pb format`** — real value, self-contained project (comment-preserving
  AST printing), zero coupling to the pillars. Later, whole.
- **`pb convert`** — schema-aware payload conversion (bin/JSON/text).
  Descriptor-powered utility, zero coupling. Later.
- **`pb vendor`** — Go-style vendoring for air-gapped/hermetic builds.
  Lockfile + cache covers most needs first.
- **Windows/macOS** — trajectory settled; behind the runner contract.

## Hard no

- **`push`, registry/org/login management** — nothing to push to;
  publishing a module *is* pushing a git tag. Private-repo auth is git's
  own (SSH/credential helpers); proxy auth is netrc-shaped config. pb grows
  no identity system.
- **Remote plugin execution / remote codegen / generated SDKs** — the
  anti-pillar. Local, sandboxed, or not at all.
- **RPC client (`buf curl` equivalent)** — outside the mission; grpcurl and
  friends exist and `pb export` / descriptor sets feed them.
- **Embedded registry/proxy server** — serving is pbr's job; pb is a
  client, full stop.
- **Runtime buf-config compatibility** — migrate, don't emulate.
- **Telemetry** — none, ever. A documented property of the tool, not an
  accident.
- Settled exclusions restated: platform emulation fallback, custom
  checksum-DB service, container-based lint escape hatch.
