# Archive extraction has no consumer

Lands: when a spec names a consumer that materializes a module archive
onto a filesystem — an export verb, a cache extraction — and that
consumer is built over ExtractZip

archive.ExtractZip is the one implementer of REQ-archive-no-exec-materialization
(module-archive.md): it writes the file set's regular files alone,
none executable, no link and no submodule entry, refusing bytes that differ from the verified manifest. Nothing in the toolchain
calls it: the module cache keeps the wire container and every consumer
reads members through ZipFiles and ZipModuleFile, so the clause's contract
is enforced over a function only its tests reach (TestExtractNoExec,
TestExtractNoExecProperty, TestExtractRefusesBadDigest,
TestZipLinksCarried's extraction arm). The clause names cache
extraction and export as the tooling it binds; neither exists yet.
When one does, it is built over ExtractZip and the clause's
enforcement pointer names the consumer's witness beside the
function's.
