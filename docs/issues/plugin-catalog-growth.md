# The plugin catalog lacks the plugins three configurations reference

A migration over the buf configurations on hand reports three whose
only unmapped facts are plugin references the catalog does not
publish: `buf.build/protocolbuffers/python` with `buf.build/grpc/python`,
`buf.build/protocolbuffers/java` with `buf.build/grpc/java`, and
`buf.build/community/planetscale-vtprotobuf`. Each maps today only
through `--plugin <name>=<reference>` to an image the user publishes
with `pb plugin build`.

The catalog repository (greatliontech/pb-plugins) grows by recipes of
its kinds (migrate.md, the plugin catalog): `protocolbuffers/python`
and `protocolbuffers/java` are protoc's own generators, a C++ target
built by bazel on a runner of each platform, as `protocolbuffers/csharp`
is; `grpc/python` is grpc's C++ plugin, built the same way; `grpc/java`
ships prebuilt executables per platform with its releases, the
upstream-release kind; `community/planetscale-vtprotobuf` is a Go
package cross-compiled to every platform. pb's copy of the catalog's
names (`migrate.Catalog`, held to the repository by `TestPluginCatalog`)
then grows by those five, and the three configurations migrate with no
replacement.

Lands: the feature-full plan, chunk 21
