# pb — workspaces

A workspace groups local modules — typically in one repository — so they
resolve against each other's working copies instead of published versions.
Its semantics follow Go workspaces: a `use` list, local override, one
lockfile at the root.

**workspace** (term): A directory containing a workspace file `pb.work`,
serving as the resolution root for the modules it uses.

**workspace module** (term): A module whose root directory is listed in
the workspace file's `use` list.

**REQ-work-schema** (wire): The workspace file MUST contain exactly the
top-level key `use`: a list of relative directory paths, each the module
root of a declared module.

**REQ-work-local-resolution** (invariant): A requirement on a workspace
module's path MUST resolve to its local working copy, at whatever state
it is in — never to a published version, regardless of what version any
requirement declares.

**REQ-work-external-resolution** (behavior): Requirements on non-
workspace modules MUST resolve normally, with version selection running
over the union of all workspace modules' requirements.

**REQ-work-lockfile** (structural): A workspace MUST have exactly one
lockfile, at the workspace root; workspace modules have no lockfiles of
their own.

**REQ-work-default** (behavior): Absent a workspace file, a module's own
root MUST serve as the resolution root — a single-module workspace in
all but name.
