# The direct construction clones an origin into memory

Lands: the migrate plan's chunk 8

The direct construction fetches an origin as a bare clone in memory,
every branch and tag (`internal/source/direct`): hermetic, and the
size of the origin's whole history. An origin the size of
googleapis — the first dependency the migration maps, and one every
grpc-gateway, envoy, xds and grpc module depends on — is hundreds of
megabytes of history, and the clone ends the process on a host with
a memory guard before a version is discovered: the dependency
table's layout test (`migrate.md` REQ-migrate-deps) could verify no
entry whose dependencies reach googleapis, and a user's `pb dep tidy`
against such an origin meets the same clone. The remedy derives from
the module cache the client already keeps (`module-proxy.md`): the
clone lives in the cache's filesystem, fetched once and updated on
later fetches, so an origin's history costs disk rather than memory
and a second resolution reuses it. The clone stays bare and complete
— the ancestry checks and tag namespaces read from it are unchanged
— only its storage moves.
