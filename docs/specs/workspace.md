# pb — workspaces

A workspace groups local modules — typically in one repository — so they
resolve against each other's working copies instead of published versions.
Its semantics follow Go workspaces: a `use` list, local override, one
lockfile at the root.

**workspace** (term): A directory containing a workspace file `pb.work`,
serving as the resolution root for the modules it uses. The nearest
workspace file above a directory governs it; a workspace directory used
by an outer workspace is an ordinary member there, and resolves as its
own workspace only from within.

**workspace module** (term): A module whose root directory is listed
in the workspace file's `use` list. A workspace module may lie within
another: each resolves locally as a member
(REQ-work-local-resolution), and the outer, holding a module file
beneath its root, is never publishable (`module-archive.md`
REQ-archive-nested-module) — a working arrangement, not a published
one.

**REQ-work-schema** (wire): The workspace file MUST contain the
top-level key `use`: a list of relative directory paths, each the module
root of a declared module; and, optionally, `replace`: a non-empty
mapping from a module path to a replacement, spelled either
`<module path>@<version>`, the version a tagged release or a
pseudo-version (REQ-work-replace), or a relative root-contained
directory path led by `./`, or the bare `.` for the root itself
(REQ-work-replace-dir). No other keys exist.

**REQ-work-emission** (behavior): Tooling that writes a workspace file
MUST emit it canonically: UTF-8, LF line endings, two-space
indentation, `use` first, its entries cleaned and sorted in raw-byte
order, then `replace` where the file holds one, its keys in raw-byte
order and each value canonical — a pair as `<module path>@<version>`,
a directory as `./` and its cleaned path, the root as `.` — each
scalar spelled as `check-rules.md` REQ-lint-emission
spells a scalar — plain where the file's reader reads the plain
spelling back as exactly that text and no YAML schema of any version
reads it as other than text, double-quoted otherwise — and never a
rendering the file's reader rejects or reads as a different file, so
two writers of one workspace produce one file and a file written once
reads the same under every reader.

**REQ-work-local-resolution** (invariant): A requirement on a workspace
module's path MUST resolve to its local working copy, at whatever state
it is in — never to a published version, regardless of what version any
requirement declares. Version disagreements among requirements on such
a path — major crossings included — never fail selection: no external
version answers for the path, so no disagreement about one can make
the working copy wrong.

**REQ-work-external-resolution** (behavior): Requirements on non-
workspace modules MUST resolve normally, with version selection running
over the union of all workspace modules' requirements.

**REQ-work-lockfile** (structural): A workspace MUST have exactly one
lockfile, at the workspace root; workspace modules have no lockfiles of
their own.

**REQ-work-replace** (behavior): A replacement `X: Y@v` MUST make the
module Y at v stand for X throughout the build: every node of X in the
requirement graph reads its requirements from Y@v's module file (a
synthesized Y declares none, `module-resolution.md`
REQ-resolve-synthesis), and X's selected version's file set is Y@v's.
Selection over X's versions runs unchanged, so X keeps its place and
its path in the build list, and every version of X is replaced alike.
Y@v is fetched, verified and pinned, and its provenance judged, under
Y's own path — never X's — and X itself is never fetched. Module files
never carry a replacement: only the resolution root's own workspace
file does, so a replacement never reaches a consumer of the workspace's
published modules, and a module required both as X's replacement and
in its own right is two modules of the build, each providing the
other's import paths, which the compile refuses as any duplicate
provider (`generation.md` REQ-gen-compile).

**REQ-work-replace-dir** (behavior): A replacement `X: ./dir` MUST
make the directory stand for X throughout the build as a workspace
module's working copy stands for its own path: every node of X in the
requirement graph reads its requirements from the directory's module
file, and X's selected version's file set is the directory's files, a
nested module's excluded, whatever state the working copy is in.
Selection over X's versions runs unchanged, as under REQ-work-replace.
Nothing is fetched for X, no pin is recorded for it, and the trust
policy is never consulted: the working tree is the workspace's own,
and the directory's content is an input of the build on the same
footing as a workspace module's (REQ-work-local-resolution), not a
verified artifact — the lockfile holds no record of it, and a pin
under X from before the replacement is outside the graph, pruned as
any unreachable pin (`dep-verbs.md` REQ-dep-tidy). The directory
answers for X whatever module path its own module file declares, as
a pinned replacement does; a lockfile inside it is a clone's own,
never the workspace's, and is not read. Reports and errors name the
directory's files by their place in the tree, as a workspace
module's, and the module as `X@<version> (<directory>)`, the
directory as the workspace file spells it.

**REQ-work-replace-names** (behavior): A workspace file MUST be
refused when a replacement names, on either side, a workspace module
(the local override already answers for the path, and a workspace
module's directory named as a replacement is the same module twice),
when its replacement is the replaced path itself or a path the same
file replaces (the graph reads through one step, never a chain), or
when its replacement directory is not root-contained or holds no
module file (the directory is read as a workspace module is, and a
workspace module declares itself).

**REQ-work-default** (behavior): Absent a workspace file, a module's own
root MUST serve as the resolution root — a single-module workspace in
all but name.

**REQ-work-membership** (behavior): Operating from a module inside a
workspace whose `use` list does not include it MUST fail: the governing
resolution root would neither treat the module as local nor carry its
requirements, so resolving silently against it answers for the wrong
root.
