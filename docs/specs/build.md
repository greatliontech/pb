# pb — build

Build materializes the compiled schema of a resolution root as one
descriptor set for tooling outside pb: protoc and its plugins driven
by another toolchain (`--descriptor_set_in`), gRPC tools and servers
that take a schema without reflection, registries and CI that store a
schema per release and diff releases, and runtimes that decode
messages dynamically. Inside pb the same compiled set is what every
plugin request is built from; build hands it out as a file, the
schema as a plugin would be handed it before any entry's overrides.

**build** (term): The verb `pb build <file>`, run against the
resolution root governing the working directory, located and held to
membership as `dep-verbs.md` locates a dep verb's root. The resolved
build — the workspace modules and the build list — is what the verb
compiles; "the verb" below is the operation, "the resolved build" its
subject.

**descriptor set** (term): A `google.protobuf.FileDescriptorSet`
message in protobuf binary wire form: the compiled schema of a
resolved build, one `FileDescriptorProto` per file.

**REQ-build-compile** (behavior): The verb MUST compile the resolved
build exactly as generation does (`generation.md` REQ-gen-compile)
before writing anything: the build list resolved and its first-use
pins persisted (`module-lockfile.md` REQ-lock-first-use), every
workspace module's files with imports resolved first against the
well-known imports and then against the build list's modules, an
unsatisfied import and an import path more than one module provides
failing as they fail there — so a descriptor set is the schema a
plugin would be handed before any entry's overrides, or nothing is
written.

**REQ-build-set** (wire): The descriptor set MUST hold every file
reachable from the workspace modules' files, the well-known imports
among them, in topological order — dependencies before importers,
each file's imports visited in declaration order, the workspace
modules' files as roots in compile order — each file's descriptor as
a plugin request carries it (`generation.md` REQ-gen-request): full
options, source-retention options not stripped — protoc strips them
from a descriptor set unless told to retain them, a divergence that
only ever hands a consumer more — and the source information the
compiler records, comments and spans; the sources' declared options
and no generation entry's option overrides, which are an entry's own;
and nothing else — no compiler version, no timestamp, no environment.
A resolved build with no protobuf file yields an empty set.

**REQ-build-output** (behavior): The output file, a required argument
whose parent directory exists, MUST be written whole: the set in
protobuf deterministic serialization, written to a temporary sibling
and moved into place as a regular, non-executable file at the mode a
created file gets — `0644` less the process's umask — a regular file
already there replaced and a symbolic link there replaced rather than
followed, a directory there refused, a path naming a protobuf source
(`.proto`) refused since the next load would compile it, so that no
reader observes a partial set and a failure leaves the path as the
verb found it.

**REQ-build-report** (behavior): The verb MUST report on standard
output one line naming the count of files in the set and the output
file as given.

**REQ-build-determinism** (invariant): A descriptor set MUST be a pure
function of the workspace modules' sources, the pinned build and the
toolchain — the well-known imports it embeds and the compiler and
serializer it links — byte-identical across runs of one toolchain,
independent of cache state, of traversal order and of the generation
file, which the verb never reads (`module-resolution.md`
REQ-resolve-determinism).
