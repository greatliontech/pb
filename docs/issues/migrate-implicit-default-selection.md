# buf's implicit default selection migrates to nothing

Lands: the migrate plan's chunk 5

A buf module with no `lint` or `breaking` section, or a `v1` workspace
directory with no `buf.yaml` at all, is checked by buf under its
default selection — `DEFAULT` for lint, `FILE` for breaking — the same
as a module that spells those out. `pb migrate` writes the lint file
only where a section carries anything (migrate.md REQ-migrate-verb),
so the implicit selection migrates to nothing and the module is
checked under pb's own default, with no line of the report saying
so. Chunk 5 decides whether that is the migration — pb's default
standing in for buf's — or whether the report names buf's implicit
selection as a fact, mapped to pb's default or unmapped; the reader
of an absent member and of a sectionless one must take one path,
since buf treats the two alike.
