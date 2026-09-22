# Two dependency table entries wait on links carried

Lands: the migrate plan's chunk 9 — links carried and
`buf.build/envoyproxy/protoc-gen-validate` and
`buf.build/opencensus/opencensus` entered as entries of their own —
envoy and xds restored to the table as TestDependencyLayouts
verifies them

The dependency table (`migrate.md` REQ-migrate-deps) admits an entry
with its layout verified against its origin. Two of the entries the
plan named — `buf.build/envoyproxy/envoy` and `buf.build/cncf/xds`,
at `github.com/envoyproxy/envoy/api` and `github.com/cncf/xds` —
depend on `buf.build/envoyproxy/protoc-gen-validate`, whose
repository carries symbolic links the module archive refuses
(docs/issues/dependency-table-symlinks.md), and envoy on
`buf.build/opencensus/opencensus`, which the table never held and
which enters as an entry of its own
(`github.com/census-instrumentation/opencensus-proto` at its `src`
subtree), verified as every entry is. They are out of the table until
the layout test runs over them: an entry standing unverified would
map a name to a declaration whose imports may go unsatisfied, the
tidy then failing after the files are written, where an unmapped name
leaves a working tree and the flag. Until then a migration naming
either reports it unmapped, and `--dep` with a version declares it
without discovery.
