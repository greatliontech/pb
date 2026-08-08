# pb — module resolution

Resolution maps module paths to origins, versions to commits, and a
requirement graph to one version per module. It has no registry: a module
path resolves at its origin (with an optional vanity redirect), versions
are git tags or pseudo-versions, and selection is minimal version
selection over declared dependencies. Archives, digests, and the module
file are defined in `module-archive.md` and `module-file.md`;
pseudo-versions in `module-proxy.md`.

**vanity redirect** (term): An HTML meta tag in the parsed head of the
discovery document, requested over HTTPS at the module path, that names
the git repository backing a prefix of that path:
`<meta name="pb-import" content="<prefix> git <repository URL>">`,
discovered by requesting `https://<module path>?pb-get=1`. A
declaration outside the parsed head is not a redirect — body content
may be user-generated and must not redirect a module.

**repository split** (term): The division of a module path into the
prefix naming the origin repository and the remaining segments naming the
subtree within it.

**build list** (term): The result of version selection: for each module
path reachable from the resolution root's requirements, exactly one
selected version.

## Path resolution

**REQ-resolve-path-syntax** (wire): A module path MUST be a hostname —
two or more dot-separated, non-empty DNS labels of lowercase ASCII
letters, digits, and hyphens, no label beginning or ending with a hyphen
— followed by one or more `/`-separated non-empty segments
each consisting of ASCII letters, digits, and the characters `.` `-` `_`
`~`, with no segment beginning or ending with a dot; no scheme, port,
query, or fragment appears. The character discipline is load-bearing
downstream: proxy escaping is defined over ASCII case, vanity-redirect
probing embeds the path in a URL, and canonical YAML emission writes
paths as plain scalars.

**REQ-resolve-vcs-suffix** (behavior): A module path containing a segment
ending in `.git` MUST resolve with that segment as the final segment of
the repository prefix — the repository is the HTTPS remote at the prefix,
and the remaining segments are the subtree.

**REQ-resolve-vanity** (behavior): Absent a `.git` segment, a vanity
redirect whose prefix is a segment-exact prefix of the module path MUST
take precedence over probing, with the longest matching prefix winning
when several apply. Discovery is one request to the module path itself
with `?pb-get=1`; the response may declare redirects for any of the
path's prefixes; a failed request or a response declaring no matching
prefix falls through to probing, and a declared repository URL that is
not HTTPS fails resolution rather than redirecting. The discovery
request follows HTTP redirects only to HTTPS URLs: a redirect to any
other scheme aborts the request, which then counts as failed — discovery
never fetches over cleartext.

**REQ-resolve-probing** (behavior): Absent a `.git` segment and a vanity
redirect, the repository split MUST be found by probing path prefixes in
increasing length with `git ls-remote` over HTTPS, taking the first
prefix that answers as the repository; the probe result is not authority
— every fetched artifact still verifies per its own contract. The bare
hostname is never probed — a host answering reference listings at its
root would silently capture every module path on it; a repository rooted
at a bare host declares itself via a `.git` segment or a vanity
redirect.

## Versions

**REQ-resolve-release-tags** (wire): A tagged release of a module rooted
at the repository root MUST correspond to a git tag named exactly the
version; a declared module rooted at a subtree uses tags prefixed with
the subtree path (`<subtree>/vX.Y.Z`).

**REQ-resolve-synthesized-tags** (behavior): A synthesized module MUST
take its tagged releases from repository-level tags, falling back to the
pseudo-version of the origin's default-branch head commit when no such
tag exists.

**REQ-resolve-pseudo-commit** (invariant): A pseudo-version MUST resolve
only to the commit whose hash and commit time it embeds; a pseudo-version
naming a commit absent from the origin fails resolution.

**REQ-resolve-pseudo-base** (invariant): A pseudo-version's base MUST
derive from the module's own release-tag history at the embedded commit
— its tag namespace per REQ-resolve-release-tags, repository-level for a
synthesized module — taking the highest of those release tags on an
ancestor of that commit, or the zero base when none exists, so a crafted
pseudo-version cannot outrank the module's real releases in selection; a
base-inconsistent pseudo-version fails resolution.

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
selection over the requirement graph — whose nodes are the resolution
root plus every (module path, version) pair some node requires,
transitively, a pair remaining a node even when its path selects a higher
version: the selected version of each module path is the maximum, under
semantic-version ordering with pseudo-versions ordered by their embedded
timestamp, of the versions required for that path across the graph —
never a version newer than required, never one older than any
requirement.

**REQ-resolve-no-import-versioning** (behavior): Resolution MUST NOT
derive a module path from a version: a major version bump keeps the
module path, version-shaped path segments are ordinary segments (protobuf
packages carry their own version idiom inside the schema, and modules
legitimately root at such subtrees), and module identity flows through
origin resolution verbatim.

**REQ-resolve-major-crossing** (behavior): Selection that raises a
module's major version above what some requirement in the graph declares
MUST fail the operation unless the resolution root itself requires that
module at the selected major — the failure names the module, a
requirement at the selected version, and the lowest requirement below
the selected major (ties broken by requirer, so the report is
deterministic), and states that recording the selected major in the
root's requirements accepts it. When the root does require the selected
major, the crossing is instead reported as a warning naming the module,
the selected version, and the lowest crossed requirement's version.
The rule reads only the requirement graph: no configuration input exists,
so the build list and its diagnostics stay a pure function of the
requirements.

**REQ-resolve-determinism** (invariant): The build list MUST be a pure
function of the resolution root's requirements and the pinned module
files of the graph — independent of fetch order, source list, and cache
state.
