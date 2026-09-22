# A statement behind a block comment on its line escapes the migration's placing

Lands: when the checker and the migration share one line scanner
(docs/issues/one-line-scanner.md, the migrate plan's chunk 11), the
scanner giving a line's first token past its comments

The migration reports a rewritten directive displaced where the
line of code it leads declares nothing a finding sits on — an
option, reserved, extensions, import, package, syntax or edition
statement, or a body's closing brace (migrate.md
REQ-migrate-comments). It reads the line's first token after
trimming spaces and tabs alone, so a statement preceded by a block
comment on its own line — `/* x */ option a = 1;`, `/* x */ }` —
escapes every branch, and a directive leading it is reported placed
where the checker reads it not. The shape is rare in proto source;
the fix wants the first token past the line's comments, which one
scanner both readers consume provides.
