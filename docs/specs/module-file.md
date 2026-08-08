# pb — module file

The module file (`pb.yaml`, defined as a term in `module-archive.md`)
declares a module: its identity and its dependencies. It is deliberately
minimal — it travels inside the module archive and drives other parties'
resolution, so it carries the distribution contract and nothing else. Tool
behavior (generation, lint, trust policy, workspaces) lives in separate
files that never enter the archive's contract role.

**declared dependency** (term): An entry in a module file's `deps` map: a
module path mapped to the minimum version of that module the declaring
module requires.

## Schema

**REQ-modfile-schema** (wire): A module file MUST contain exactly the
top-level keys `module`, a string module path, and — only when the module
has dependencies — `deps`, a map from module path to version string. No
other keys exist.

**REQ-modfile-acceptance** (wire): Parsing MUST accept any YAML spelling
that yields the declared facts unambiguously and reject everything else:
key order, quoting, and line-ending variants are accepted and normalized
by canonical re-emission; merge keys, anchors, aliases, tags, and
non-string mapping keys are rejected anywhere in the document — their
expansion differs across YAML implementations, and a file in the
archive's contract role reads identically everywhere or not at all.

**REQ-modfile-identity** (invariant): The `module` value MUST equal the
module path under which the file's module is required and fetched; a
fetched module whose module file declares a different path fails
verification. This is what prevents a module from being served under an
identity its author did not declare.

**REQ-modfile-versions** (wire): Each `deps` value MUST be a tagged
release version (`vX.Y.Z`, optionally with a prerelease suffix) or a
pseudo-version.

**REQ-modfile-emission** (behavior): Tooling that writes a module file
MUST emit it canonically: UTF-8, LF line endings, two-space indentation,
`module` first, `deps` sorted by key in raw-byte order.
