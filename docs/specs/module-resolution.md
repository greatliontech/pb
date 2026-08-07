# pb — module resolution

Resolution maps module paths to origins, versions to commits, and a
requirement graph to one version per module. It has no registry: a module
path resolves at its origin (with an optional vanity redirect), versions
are git tags or pseudo-versions, and selection is minimal version
selection over declared dependencies. Archives, digests, and the module
file are defined in `module-archive.md` and `module-file.md`;
pseudo-versions in `module-proxy.md`.

**vanity redirect** (term): An HTML meta tag, served over HTTPS at a
module path prefix, that names the git repository backing that prefix:
`<meta name="pb-import" content="<prefix> git <repository URL>">`,
discovered by requesting `https://<prefix>?pb-get=1`.

**repository split** (term): The division of a module path into the
prefix naming the origin repository and the remaining segments naming the
subtree within it.

**build list** (term): The result of version selection: for each module
path reachable from the resolution root's requirements, exactly one
selected version.

## Path resolution

**REQ-resolve-path-syntax** (wire): A module path MUST be a hostname
followed by one or more `/`-separated non-empty segments, containing no
scheme, no port, no query, and no fragment.

**REQ-resolve-vcs-suffix** (behavior): A module path containing a segment
ending in `.git` MUST resolve with that segment as the final segment of
the repository prefix — the repository is the HTTPS remote at the prefix,
and the remaining segments are the subtree.

**REQ-resolve-vanity** (behavior): Absent a `.git` segment, a vanity
redirect whose prefix is a prefix of the module path MUST take precedence
over probing, with the longest matching prefix winning when several
apply.

**REQ-resolve-probing** (behavior): Absent a `.git` segment and a vanity
redirect, the repository split MUST be found by probing path prefixes in
increasing length with `git ls-remote` over HTTPS, taking the first
prefix that answers as the repository; the probe result is not authority
— every fetched artifact still verifies per its own contract.

## Versions

**REQ-resolve-release-tags** (wire): A tagged release of a module rooted
at the repository root MUST correspond to a git tag named exactly the
version; a declared module rooted at a subtree uses tags prefixed with
the subtree path (`<subtree>/vX.Y.Z`).

**REQ-resolve-synthesized-tags** (behavior): A synthesized module MUST
take its tagged releases from repository-level tags, falling back to
pseudo-versions when no such tag exists.

**REQ-resolve-pseudo-commit** (invariant): A pseudo-version MUST resolve
only to the commit whose hash and commit time it embeds; a pseudo-version
naming a commit absent from the origin fails resolution.

## Synthesis

**REQ-resolve-synthesis** (behavior): A subtree containing no module file
MUST be consumable as a synthesized module: its file set is the subtree's
(per the archive contract), its include root is the subtree root, and it
declares no dependencies.

**REQ-resolve-unsatisfied-imports** (behavior): A protobuf import that no
module in the build list satisfies MUST fail the operation with an error
naming the importing module, the importing file, and the unsatisfied
import path.

## Selection

**REQ-resolve-mvs** (behavior): Version selection MUST be minimal version
selection: the selected version of each module path is the maximum, under
semantic-version ordering with pseudo-versions ordered by their embedded
timestamp, of the versions required for that path across the resolution
root and every module in the build list — never a version newer than
required, never one older than any requirement.

**REQ-resolve-no-import-versioning** (structural): Module paths MUST NOT
carry semantic-import-versioning suffixes: a major version bump keeps the
module path, and protobuf packages carry their own version idiom inside
the schema.

**REQ-resolve-major-crossing** (behavior): Selection that raises a
module's major version above what any requirement in the graph declared
directly MUST be reported to the user as a warning naming the module and
both versions.

**REQ-resolve-determinism** (invariant): The build list MUST be a pure
function of the resolution root's requirements and the pinned module
files of the graph — independent of fetch order, source list, and cache
state.
