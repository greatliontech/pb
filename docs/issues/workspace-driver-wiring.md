# Workspace resolution clauses bind at the driver

REQ-work-local-resolution and REQ-work-external-resolution are covered
at their decision surfaces — `workspace.Root.IsLocal` (the
unconditional override) and `workspace.Root.Requirements` (the external
edge union) — but their operative halves are resolver behavior: the
driver must consult IsLocal before any fetch (a local path never
resolves to a published version) and must seed version selection with
exactly the union. REQ-work-membership (operating from a module a
workspace does not list is an error) is entirely driver behavior and is
gapped.

Lands: 13
