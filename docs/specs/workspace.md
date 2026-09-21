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

**REQ-work-schema** (wire): The workspace file MUST contain exactly the
top-level key `use`: a list of relative directory paths, each the module
root of a declared module.

**REQ-work-emission** (behavior): Tooling that writes a workspace file
MUST emit it canonically: UTF-8, LF line endings, two-space
indentation, `use` first and alone, its entries cleaned and sorted in
raw-byte order, each spelled as `check-rules.md` REQ-lint-emission
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

**REQ-work-default** (behavior): Absent a workspace file, a module's own
root MUST serve as the resolution root — a single-module workspace in
all but name.

**REQ-work-membership** (behavior): Operating from a module inside a
workspace whose `use` list does not include it MUST fail: the governing
resolution root would neither treat the module as local nor carry its
requirements, so resolving silently against it answers for the wrong
root.
