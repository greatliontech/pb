# A swift or dart plugin without a committed lockfile resolves its dependencies at build time

The plugin catalog's swift and dart kinds (greatliontech/pb-plugins,
README) build a package at the repository's tag, held to the
lockfile the package commits where it commits one. grpc/swift and
grpc/swift-protobuf commit no Package.resolved, and protobuf.dart's
protoc_plugin and connect-dart's generator, pub workspace members,
commit no pubspec.lock: their trees resolve the dependency graph as
the manifest allows at build time (grpc-swift-protobuf's generator
logic lives in grpc-swift-2, taken `from: "2.3.0"`; protoc_plugin
takes `dart_style: ^3.0.0`), so one plugin version's output can
differ between two trees built apart, even its per-platform trees of
one publish run, with whatever its dependencies have published
since. The published image is pinned by digest and signed, so what a
user runs is fixed once published; what is unpinned is which
dependency versions a tree job builds in.

Two defensible contracts:

1. The kind resolves as upstream's own builds do: a package that
   commits no lockfile is built as its manifest allows, the
   catalog adding nothing upstream does not maintain; a dependency's
   later release can then stop a not-yet-published version from
   building at all (a swift tools version or a dart SDK constraint
   raised above the pinned toolchain's, an API broken), so a retried
   tree job fails on a version that built the day before.
2. The catalog holds a lockfile per plugin version under
   `plugins/<owner>/<plugin>/files`, copied into the source tree
   before the build, so every tree of a version builds one graph: a
   `Package.resolved` beside the swift package's manifest; for dart
   the workspace root's `pubspec.lock`, which pins every workspace
   member's graph (benchmarks, conformance, tools among them, which
   the catalog never builds), held with `dart pub get
   --enforce-lockfile`:
   the catalog then maintains a lockfile upstream does not, per
   version the bump appends, by hand or by a resolve step the bump
   gains.

The tradeoff is the user's: a tree's reproducibility, and a
version's availability to a later build, against the lockfiles the
catalog commits to maintaining.

Lands: user decision.
