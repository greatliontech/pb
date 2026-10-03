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
request follows HTTP redirects only to HTTPS URLs and boundedly: a
redirect to any other scheme, or an excessive chain, aborts the request,
which then counts as failed — discovery never fetches over cleartext and
carries no cookie state in either direction.

**REQ-resolve-probing** (behavior): Absent a `.git` segment and a vanity
redirect, the repository split MUST be found by probing path prefixes in
increasing length with `git ls-remote` over the module's transport —
HTTPS, or SSH where REQ-resolve-ssh routes the module — taking the first
prefix that answers as the repository; the probe result is not authority
— every fetched artifact still verifies per its own contract. The bare
hostname is never probed — a host answering reference listings at its
root would silently capture every module path on it; a repository rooted
at a bare host declares itself via a `.git` segment or a vanity
redirect.

## Private origins

**credential file** (term): The file the `netrc` setting names
(`user-config.md`), by default `.netrc` in the user's home directory
— `_netrc` on Windows — in the netrc grammar: whitespace-separated
tokens, a token between double quotes taken whole with `\"`, `\\`,
`\n`, `\r` and `\t` escaped as curl reads them; `machine <host>`
opening an entry for the host, `default` opening the fallback entry,
`login <name>` and `password <secret>` filling the open entry,
`account <name>` accepted and unused, `macdef <name>` opening a macro
that runs through the next blank line and is skipped. An entry
holding both a login and a password is a credential; one lacking
either is none; the default entry is read and lent to no host — pb
reaches the hosts a dependency graph names, not ones the user typed,
and a credential for every host would go to any of them, as Go
declines it for the same reason. A host is matched by name alone,
case-insensitively, any port aside, the first entry naming it
winning. The file absent at the default location, or a host with no
home directory to derive that location from, is an empty one; a file
a stated layer names that is absent or unreadable, and in any file a
token the grammar does not name or a quote the line does not close,
is an error naming the layer.

**REQ-resolve-credentials** (behavior): Every request over HTTPS to a
host the credential file holds a credential for — the discovery of a
vanity redirect, a reference listing, an origin fetch, a proxy fetch
(`module-proxy.md` REQ-proxy-config), a redirect's target by the
target's own entry — MUST carry the credential as HTTP basic
authorization, and no request over cleartext carries any: a credential
goes to the host it names over a channel that hides it, or nowhere.

**REQ-resolve-ssh** (behavior): A module the `ssh` setting matches —
comma-separated glob patterns in the `noproxy` grammar
(`module-proxy.md` REQ-proxy-config), one matching the module path or
any leading segment prefix of it — MUST be reached over SSH: its
origin, however resolved, is reached at the HTTPS repository's host
name and path spelled `ssh://git@<host name>/<path>`, the HTTPS port
dropped, while the origin's identity — the repository URL the
module's provenance is held to (`provenance.md`
REQ-prov-origin-consistency) and every record and report name — stays
the HTTPS one, the setting routing transport alone —
reference listings, probing and fetching alike, authenticated through
the running SSH agent alone —
no key file read, no password asked — as the user `git`, the host's
key verified against the user's known hosts (`SSH_KNOWN_HOSTS`, else
the user's and the system's known-hosts files) and the host and port
as the user's OpenSSH client configuration maps them; a host whose key
no known-hosts file holds, and a run with no agent to reach, each fail
naming the cause — probing included: a prefix with no agent to reach
or whose host the run cannot trust fails resolution there rather than
reading as a prefix that does not answer, while a prefix refusing
authentication is one that does not answer — a forge refuses an
anonymous listing of a repository that does not exist — an HTTPS
refusal named as such when no prefix answers, an SSH one carried in
the attempt's own error. The setting
routes transport alone: a matching module still consults the source
list, and `noproxy` alone routes it to the origin.

## Versions

**REQ-resolve-release-tags** (wire): A tagged release of a module rooted
at the repository root MUST correspond to a git tag named exactly the
version; a declared module rooted at a subtree uses tags prefixed with
the subtree path (`<subtree>/vX.Y.Z`). A tag names a version of a
subtree module by the subtree's state at the commit the tag names, the
one commit a tag speaks of: `<subtree>/vX.Y.Z` where a module file
lies at the subtree there, the repository-level `vX.Y.Z` where the
subtree lies there holding none, the repository versioned as a whole;
a version both name is ambiguous and fails resolution, and a
subtree-prefixed tag over a commit where the subtree is absent fails
it too, the tag claiming a module the commit lacks; a tag naming no
commit is no release and names no version. The listing of a
subtree module is judged at the origin's default-branch head, the only
state a listing can be read from: subtree-prefixed tags where a module
file lies at the subtree there, repository-level tags where it lies
holding none, subtree-prefixed where it is absent, no synthesized
module being there; the listing is advisory, a version it names
resolving by the tagged commit's own state, which may name none.

**REQ-resolve-synthesized-tags** (behavior): A synthesized module MUST
take its tagged releases from repository-level tags. A module of either
kind holding no release tag in its namespace has the pseudo-version of
the origin's default-branch head commit as its latest; a subtree absent
at head has none, head being no version of it.

**REQ-resolve-pseudo-commit** (invariant): A pseudo-version MUST resolve
only to the commit whose hash and commit time it embeds; a pseudo-version
naming a commit absent from the origin fails resolution. An embedded
hash prefix carried by more than one commit at the origin identifies
none of them: resolution fails rather than choosing, so what a version
resolves to never depends on enumeration order.

**REQ-resolve-pseudo-base** (invariant): A pseudo-version's base MUST
derive from the module's own release-tag history at the embedded commit
— its tag namespace per REQ-resolve-release-tags judged at that commit,
subtree-prefixed where a module file lies at the subtree there,
repository-level where it lies holding none, and no namespace where it
is absent, the version then naming nothing of the module — taking the
highest of that namespace's release tags, by name alone, on an
ancestor of that commit, or the zero base when none exists, so a crafted
pseudo-version cannot outrank the module's real releases in selection; a
base-inconsistent pseudo-version fails resolution. Ancestry is
reachability: the commit itself counts among its ancestors, so a tag on
the embedded commit is a valid base. A pseudo-version-shaped tag is not
a release tag and never seeds a base.

## Synthesis

**REQ-resolve-synthesis** (behavior): A subtree containing no module file
MUST be consumable as a synthesized module: its file set is the subtree's
(per the archive contract), its include root is the subtree root, and it
declares no dependencies.

**well-known imports** (term): The protobuf installation's
`google/protobuf` source files, embedded in the toolchain at a version
pinned by the toolchain alone. Imports of these paths are satisfied by
the toolchain and never looked up in modules, so no module can shadow
them. `google/protobuf/go_features.proto` is not among them — protobuf
installations do not ship it — and resolves through modules like any
other import.

**REQ-resolve-unsatisfied-imports** (behavior): A protobuf import that
neither the well-known imports nor any module in the build list
satisfies MUST fail the operation with an error naming, for every such
import, the importing module, the importing file, and the unsatisfied
import path — the report is exhaustive and deterministically ordered, so
what a user sees never depends on traversal order.

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
function of the resolution root's requirements, its replacements
(`workspace.md` REQ-work-replace, a directory replacement's module
file among them, REQ-work-replace-dir), and the pinned module files of the
graph — independent of fetch order, source list, and cache state.
