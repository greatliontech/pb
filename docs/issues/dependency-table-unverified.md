# Five dependency table entries wait on googleapis' layout

Lands: user decision — googleapis' origin holds a `preview` tree
duplicating its packages, which no module pb can name compiles as a
set; either a curated origin the table names (a mirror of
`github.com/googleapis/googleapis` without `preview`, kept in step
with it as buf keeps its BSR module), or a way for a module to leave
part of its subtree out (a file-set exclusion in the module contract,
which a synthesized module has no file to declare in); the three
entries below restore with the choice, envoy and xds after them with
links carried

The dependency table (`migrate.md` REQ-migrate-deps) admits an entry
with its layout verified against its origin. Five of the entries the
plan named — `buf.build/googleapis/googleapis`,
`buf.build/grpc-ecosystem/grpc-gateway`, `buf.build/envoyproxy/envoy`,
`buf.build/cncf/xds` and `buf.build/grpc/grpc`, at
`github.com/googleapis/googleapis`, `github.com/grpc-ecosystem/grpc-gateway`,
`github.com/envoyproxy/envoy/api`, `github.com/cncf/xds` and
`github.com/grpc/grpc-proto` — depend on googleapis or are it. The
direct construction fetches googleapis by need now (module-proxy.md
REQ-proxy-direct-fetch): its head resolves to a pseudo-version and
its tree arrives in seconds. What stops the entry is the repository
itself: beside `google/`, which holds the packages the BSR module
serves, its root holds `preview/google/api` and `preview/google/type`,
fifty-seven files redeclaring the packages under `google/`, so the
repository's file set compiles from no root (`google.type.PhoneNumber`
declared twice), and `google/` as the root would serve
`api/annotations.proto` where every importer names
`google/api/annotations.proto`. buf's own module leaves `preview`
out by curation. The layout test (TestDependencyLayouts) compiles
grpc-gateway (38 files at v2.30.0) and grpc-proto (26 files at its
head) with googleapis mapped, so both restore the moment googleapis
does. They are out of the table until then: an entry standing
unverified would map a name to a declaration whose imports may go
unsatisfied, the tidy then failing after the files are written, where
an unmapped name leaves a working tree and the flag. Two of them,
envoy and xds, also depend on `buf.build/envoyproxy/protoc-gen-validate`,
out until links are carried (docs/issues/dependency-table-symlinks.md);
and envoy on `buf.build/opencensus/opencensus`, which the table never
held and which enters as an entry of its own
(`github.com/census-instrumentation/opencensus-proto` at its `src`
subtree), verified as every entry is. Until then a migration naming
any of the five reports it unmapped, and `--dep` with a version
declares it without discovery.
