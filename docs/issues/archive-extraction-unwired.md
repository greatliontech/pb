# Archive extraction has no consumer

Lands: chunk 2 of the export plan (docs/plans/export.md) — the export
verb (docs/specs/export.md) is the consumer that materializes module
files

archive.ExtractZip is the one implementer of REQ-archive-no-exec-materialization
(module-archive.md): it writes the file set's regular files alone,
none executable, no link and no submodule entry, refusing bytes that differ from the verified manifest. Nothing in the toolchain
calls it: the module cache keeps the wire container and every consumer
reads members through ZipFiles and ZipModuleFile, so the clause's contract
is enforced over a function only its tests reach (TestExtractNoExec,
TestExtractNoExecProperty, TestExtractRefusesBadDigest,
TestZipLinksCarried's extraction arm). The clause names export as
the tooling it binds.
The export verb is that consumer, writing from the file sets the
build already loads (archive.ZipFiles, regular files by construction)
rather than over ExtractZip, which nothing else reaches: the plan
retires ExtractZip and moves the clause's enforcement pointer and its
no-exec witnesses to the export's writer.
