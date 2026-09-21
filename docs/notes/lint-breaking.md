# Lint and breaking-change detection

Settled concept (decisions accepted 2026-08), and part of core
([feature-set.md](./feature-set.md)): **pb ships the engine, rules are
data.** No built-in rule catalog, no privileged defaults — all rules are
CEL, composed from ruleset repos.

## Why CEL

- **The data model isn't ours.** Descriptors are proto messages
  (`descriptor.proto` — protobuf's self-description), and CEL over proto
  messages is proven machinery (protovalidate, Kubernetes admission
  policies). pb defines a consumption contract — which CEL environment,
  which variables are bound — not a data format. The no-pb-native-anything
  principle holds.
- **Boundedness is a security feature.** CEL is non-Turing-complete, no I/O,
  terminating, cost-estimable. Rules pulled from third-party repos are safe
  *by construction* — no sandbox needed, unlike code plugins. Rules get
  provenance from sigstore and safety from the language itself.

## Settled design

1. **Rulesets are ordinary modules.** A ruleset is files in a git repo,
   fetched through the settled dependency machinery — same
   identity/resolution, canonical archive, lockfile pinning, provenance
   policy (origin-consistency default; strict mode refuses unsigned
   rulesets). Composition = import ruleset modules by path+version, then
   select: enable/exclude by rule id or tag, override severity. The
   greatliontech ruleset is one such repo — zero privileged treatment.

2. **Evaluation target: standard descriptors + a versioned CEL stdlib.**
   Rules evaluate over standard descriptor protos with the entity under
   evaluation bound (`field`, `message`, `file`, …) plus resolved context.
   pb declares a stdlib of CEL functions — `comments(entity)`, naming
   helpers, type navigation, parent/child traversal — because raw
   descriptors (SourceCodeInfo path-encoding, no back-references) are
   unusable alone. The stdlib is the real contract surface, explicitly
   versioned; a ruleset declares the environment version it targets.
   Expressiveness gaps are answered by extending the stdlib — never by
   adding a plugin mechanism to the linter.

3. **Rules return verdicts; the harness owns diagnostics.** A rule is: id,
   target kind (file/package/message/field/enum/enum-value/service/method/
   set), severity, CEL expression, message template. It returns a boolean
   plus optional message; pb maps the entity to source position via
   SourceCodeInfo and renders the diagnostic. Rules never touch locations.
   The rule-file schema (YAML carrying CEL; protovalidate/K8s precedent) is
   a minimal, specced pb consumption contract.

4. **Breaking changes: same engine; pb owns the pairing.** A breaking rule
   is CEL over `(old, new)`. Correspondence is the load-bearing part and
   belongs to the harness: pb aligns entities across versions (by
   fully-qualified name; fields by number), produces pairs including
   added/removed with an absent side, and evaluates per-kind rules against
   them. Rules express the predicate ("type changed", "number reused"),
   never the diff walk. The pairing algorithm is contract and gets specced.
   The comparison base (previous tag, arbitrary ref, lockfile version) is
   config.

5. **Scoped evaluation, including whole-set scope.** Most rules bind one
   entity (cheap, parallel). A `set` scope binds the full descriptor set for
   global properties (cross-file naming consistency, request/response name
   uniqueness across services) via CEL comprehensions, with CEL's cost
   model as the natural brake.

6. **No defaults, honestly.** `pb lint` with zero configured rules does
   nothing and says so — no hidden baseline, no smuggled "minimal"
   category. Cold start is answered by documentation pointing at sample
   rulesets, greatliontech's among them.

7. **buf migration: catalog reimplementation + mapping table.** buf's rules
   are a fixed catalog of Go code, so the converter is two pieces: (a) a CEL
   reimplementation of the catalog as an ordinary ruleset repo (tagged by
   buf's categories), signed, zero privileged status; (b) a mapping table in
   `pb migrate` that rewrites `buf.yaml` `lint`/`breaking` sections (`use`,
   `except`, `ignore`, per-directory overrides) into an import of that
   ruleset plus equivalent selection config — a table, not a runtime compat
   layer, same shape as the BSR dep mapping. After migrate, rule ids live in
   a repo the user can fork or drop; pb never knows they came from buf.
   Rules that are really integrations rather than lint (e.g. "run
   protovalidate's compiler") are listed unmapped-with-reason, not contorted
   into CEL.

   **Porting the full catalog is the acid test for the stdlib**: every rule
   that resists CEL becomes a concrete, prioritized stdlib extension; full
   coverage demonstrates the environment is complete for real-world linting.
   The migration asset is a byproduct of burning down the expressiveness
   risk.

## Deliberately out

A container-based lint escape hatch (OCI image consuming a descriptor set,
emitting diagnostics) is coherent with the plugin story and possible later —
but excluded from the concept on purpose: it reopens the safety hole CEL
closes, and its existence would pressure ruleset authors toward code instead
of data. Real expressiveness gaps improve the stdlib instead.

## Closed — specced

`docs/specs/check-rules.md` covers the rule-file schema, versioned CEL
environment, boundedness, no-defaults behavior, breaking-change pairing,
the lint file (`pb.lint.yaml`), and inline suppression
(`// pb:ignore <rule-name>`, the rule's name or unambiguous id mandatory).

## Remaining drafting

- Stdlib function inventory — emerges from porting buf's catalog (the acid
  test); each resisting rule is a prioritized stdlib extension. Environment
  versioning scheme is specced (integer `celEnv`).
- LSP/MCP later phases consume this same engine for diagnostics.
