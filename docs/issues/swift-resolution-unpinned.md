# A swift plugin without a committed lockfile resolves its dependencies at build time

The plugin catalog's swift kind (greatliontech/pb-plugins, README)
builds a SwiftPM product at the repository's tag, held to the
lockfile the package commits where it commits one. grpc/swift and
grpc/swift-protobuf commit none: their trees resolve the dependency
graph as the manifest allows at build time (grpc-swift-protobuf's
generator logic lives in grpc-swift-2, taken `from: "2.3.0"`), so one
plugin version's output can differ between two trees built apart,
even its per-platform trees of one publish run, with whatever its
dependencies have published since. The published image is pinned by
digest and signed, so what a user runs is fixed once published; what
is unpinned is which dependency versions a tree job builds in.

Two defensible contracts:

1. The kind resolves as upstream's own builds do: a package that
   commits no lockfile is built as its manifest allows, the
   catalog adding nothing upstream does not maintain; a dependency's
   later release can then stop a not-yet-published version from
   building at all (a tools version raised above the pinned
   toolchain's, an API broken), so a retried tree job fails on a
   version that built the day before.
2. The catalog holds a `Package.resolved` per plugin version under
   `plugins/<owner>/<plugin>/files`, copied into the source tree
   before the build, so every tree of a version builds one graph:
   the catalog then maintains a lockfile upstream does not, per
   version the bump appends, by hand or by a resolve step the bump
   gains.

The tradeoff is the user's: a tree's reproducibility, and a
version's availability to a later build, against the lockfiles the
catalog commits to maintaining.

Lands: user decision.
