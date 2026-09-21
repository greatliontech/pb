# Five dependency table entries await the clone on disk

Lands: the migrate plan's chunk 8, each entry restored to the table
as TestDependencyLayouts verifies it

The dependency table (`migrate.md` REQ-migrate-deps) admits an entry
with its layout verified against its origin. Five of the entries the
plan named — `buf.build/googleapis/googleapis`,
`buf.build/grpc-ecosystem/grpc-gateway`, `buf.build/envoyproxy/envoy`,
`buf.build/cncf/xds` and `buf.build/grpc/grpc`, at
`github.com/googleapis/googleapis`, `github.com/grpc-ecosystem/grpc-gateway`,
`github.com/envoyproxy/envoy/api`, `github.com/cncf/xds` and
`github.com/grpc/grpc-proto` — depend on googleapis or are it, and
the direct construction's in-memory clone of googleapis ends the
process on this host before a version is discovered
(docs/issues/direct-clone-in-memory.md). They are out of the table
until the clone lives on disk and the layout test runs over them: an
entry standing unverified would map a name to a declaration whose
imports may go unsatisfied, the tidy then failing after the files
are written, where an unmapped name leaves a working tree and the
flag. Two of them, envoy and xds, also depend on
`buf.build/envoyproxy/protoc-gen-validate`, out of the table for its
symbolic link (docs/issues/dependency-table-symlinks.md), and envoy
on `buf.build/opencensus/opencensus`, which the table never held;
their verification is bound to those as well. Until then a migration
naming any of the five reports it unmapped, and `--dep` with a
version declares it without discovery.
