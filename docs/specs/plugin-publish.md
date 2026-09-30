# pb — publishing a plugin image

A plugin image is a filesystem and an entrypoint speaking the protoc
plugin protocol, served for the platforms it runs on
(`plugin-execution.md`: the plugin image term, REQ-plugin-plain-oci,
REQ-plugin-platform-strict). Its shape is pb's contract, so pb offers
the tool that produces it: `pb plugin build` packages entrypoints
built by any means into that shape and publishes it, without a
container engine. Compiling the entrypoint and signing what is
published are the publisher's own, done with their tools; pb verifies
signatures (`provenance.md`) and never vouches.

**platform tree** (term): A directory the publisher hands `build` for
one platform (`platforms.md`, the platform term): its files are the
image's files for that platform, the entrypoint among them at the
path the publisher names.

**base** (term): An image the publisher names for a platform tree, by
a reference with a digest, whose layers and configuration the
platform's image is built over: the runtime a plugin needs beneath
its own files (a JVM, an interpreter) — the image serves a platform
only where its base does.

## The verb

**REQ-publish-verb** (behavior): `pb plugin build <reference>` MUST
take a full plugin reference with a tag (`plugin-execution.md`
REQ-plugin-no-privileged-source, `generation.md` REQ-gen-schema's
spelling: an unambiguous registry host, no digest), the entrypoint as
argv (`--entrypoint`, repeatable, the first value an absolute path
within the platform tree), the files marked executable beside the
entrypoint (`--executable`, repeatable, absolute paths within the
tree), and one platform tree per platform
(`--platform <os>/<arch>=<directory>`, at least one, no platform
twice, each spelled as the platform term has it), optionally a base
per platform (`--base <os>/<arch>=<reference>@<digest>`, only for a
platform given a tree); refuse before publishing anything, naming
the flag, where a value does not parse, a tree is not a directory (a
link to one is that directory: the tree's location is no input), the
entrypoint's path or an executable's names no regular file in a
tree — every tree, an image that fails only when the plugin execs
what was never marked being held at its tag forever — a tree holds a
symbolic link (an image pb runs carries none, `module-archive.md`'s
reading of links for what pb materializes), or a base cannot be
fetched at its digest or is not the platform's — a digest naming one
image is that image whatever platform is asked for, so its
configuration must name the platform; then build one image per
platform
(REQ-publish-image), the manifest list over exactly those platforms
(REQ-publish-list), publish the list and its images to the reference
under the publisher's own registry credentials (the ambient
credential store, as a pull uses it), and report the reference with
the list's digest and, per platform, the image's digest — the
reference the generation file names and the digest the lockfile pins
(`module-lockfile.md` REQ-lock-plugin-entry).

## The image

**REQ-publish-image** (wire): The image built for a platform MUST be
an OCI image whose configuration names the platform (`os`,
`architecture`, no variant), whose entrypoint is the argv given with
no `cmd`, whose process runs as an unprivileged numeric user
(`65534:65534`, a runtime honouring users needing no passwd file for
a numeric one; pb's own runners run the process as their own user)
at the root directory, with no environment of its own, no creation
time, and no history — over exactly one layer, uncompressed (its
digest the tar's own, which no compressor's output enters), holding
the platform tree's files at their tree-relative paths under the
root, each a regular file or directory, a file's mode `0755` where
the build names it executable — the entrypoint's file always — and
`0644` otherwise, the host's mode bits never consulted, a
directory's `0755`, every entry owned by `0:0` with the zero time,
entries in the sorted order of their names as the layer spells them
(a directory's with its trailing slash); where a base is named, the
base's layers precede that one and the base's configuration is kept
whole but for the entrypoint, `cmd`, `os`, `architecture`, `variant`,
the creation time and the history, which are the build's — the
base's environment, working directory, user and OS version standing,
as the runtime beneath the plugin needs them, the OS version named
on the list's entry as on the configuration.

**REQ-publish-list** (wire): The manifest list published MUST be an
OCI image index whose entries are exactly the platforms given, one
each, every entry's descriptor naming its platform as the image's
configuration does, in the platform term's spelling order; a list
with two entries for one `os/arch` is never built, as
`plugin-execution.md` REQ-plugin-platform-strict would refuse it.

**REQ-publish-determinism** (invariant): The digest of every image and
of the list MUST be a pure function of the inputs — the trees' paths
and contents, the entrypoint, the executables named, the platforms,
each base at its digest — independent of the build's host, time and
user, the trees' locations, modes and timestamps, and the build of
pb that ran (no compressor's output enters a digest), so a publisher
rebuilding the same inputs anywhere reproduces the digest a lockfile
pins.

**REQ-publish-immutable** (behavior): A reference whose tag already
names a list MUST NOT be published over: `build` reads the tag first
and refuses naming the digest it holds and the digest it would
publish where they differ — a tag means one image forever, as a
lockfile's pin has it — and publishes nothing, reporting the digest
unchanged, where they are the same. The reading is the publisher's
guard against itself and every publisher before it: two publishers
racing one tag are the registry's to order, no registry offering a
conditional publish, and a registry that will not say whether the
tag exists — one refusing to answer for a repository not yet created
— refuses the publish, the repository created first.
