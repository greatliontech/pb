# The docker runner's store byte path on windows

The `docker` runner hands the daemon pb's verified export as a
stream (`docs/specs/plugin-execution.md` REQ-plugin-core-verifies):
a tar of the export's tree, modes kept. On windows the export's
tree carries no file modes — the filesystem has none — so the stream
would hand the daemon an image whose entrypoint no longer executes.
The runner refuses the stream there, the docker runner is withheld
as a substrate for an exported entry under the store byte path, and
the daemon's pull of the verified digest is the one byte path
(`docs/specs/platforms.md` REQ-plat-oci-substrate). The fake
daemon's protocol tests over the stream skip on windows with it:
that coverage is withheld there until the stream's design lands.

Resolution (decided): the stream is built from the store's verified
image — its layers and configuration, an OCI archive the daemon
loads — instead of the export's tree, on every platform, so the
modes and ownership the image declares reach the daemon byte for
byte and the export tree leaves the daemon's path.

Lands: docs/plans/feature-full.md chunk 28.
