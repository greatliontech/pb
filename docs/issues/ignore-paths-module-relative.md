# An ignore glob cannot scope to one module

Lands: the migrate plan's chunk writing the lint file's ignores
(chunk 5)

A finding's path is module-relative (`b.proto` for the file
`b/b.proto` of module `b`), and an ignore entry's globs match that
path (check-rules.md REQ-lint-config-schema), so two workspace
modules whose files share a path share every ignore over it, and a
per-module ignore — buf's `ignore` and `ignore_only` under a `v2`
module — merges into the root's unscoped when migrated. The output
line shares the ambiguity: two modules' `b.proto` print alike.

The fork. One: finding paths and ignore globs become workspace-
relative (`b/b.proto`), a module's directory prefixed, which scopes
an ignore to a module, tells two modules' files apart in the output,
and changes every finding line and every ignore a lint file holds.
Two: paths stay module-relative and a module's ignores live in its
`modules` entry beside its selection, the root's ignores applying to
every module, which keeps the output and existing files as they are
and gives per-module ignores a home. The tradeoff the user weighs:
one path vocabulary across output and configuration against an
unchanged output form with one more per-module field.
