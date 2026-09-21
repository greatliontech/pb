# Five dependency table entries await fetching by need or links carried

Lands: the migrate plan's chunk 8 for googleapis, grpc-gateway and
grpc, its chunk 9 for envoy and xds, each entry restored to the
table as TestDependencyLayouts verifies it

The dependency table (`migrate.md` REQ-migrate-deps) admits an entry
with its layout verified against its origin. Five of the entries the
plan named — `buf.build/googleapis/googleapis`,
`buf.build/grpc-ecosystem/grpc-gateway`, `buf.build/envoyproxy/envoy`,
`buf.build/cncf/xds` and `buf.build/grpc/grpc`, at
`github.com/googleapis/googleapis`, `github.com/grpc-ecosystem/grpc-gateway`,
`github.com/envoyproxy/envoy/api`, `github.com/cncf/xds` and
`github.com/grpc/grpc-proto` — depend on googleapis or are it, and
the direct construction's in-memory clone of googleapis' whole
history ends the process on this host before a version is discovered
(docs/issues/direct-clone-in-memory.md). They are out of the table
until the construction fetches what its proof needs and the layout
test runs over them: an entry standing unverified would map a name
to a declaration whose imports may go unsatisfied, the tidy then
failing after the files are written, where an unmapped name leaves a
working tree and the flag. Two of them, envoy and xds, also depend
on `buf.build/envoyproxy/protoc-gen-validate`, out until chunk 9
carries links (docs/issues/dependency-table-symlinks.md), so they
restore there; and envoy on `buf.build/opencensus/opencensus`, which
the table never held and which chunk 9 adds as an entry of its own
(`github.com/census-instrumentation/opencensus-proto` at its `src`
subtree), verified as every entry is. Until then a migration naming
any of the five reports it unmapped, and `--dep` with a version
declares it without discovery.
