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

Two designs lift the refusal, with a tradeoff the user weighs:

- An ocifs export carrying the image's file modes on windows as a
  record beside the tree (the filesystem holds none), which the
  stream reads: the smaller change, a second source of truth beside
  the tree that every reader of a windows export must honour.
- The stream built from the store's verified image — its layers and
  configuration, an OCI archive the daemon loads — instead of the
  export's tree: modes and ownership byte for byte on every
  platform, the export tree out of the daemon's path everywhere,
  which would replace the export-tar byte path on the unix rows too
  and so changes what every platform's daemon receives.

Lands: user decision — the record beside the tree, or the stream
from the store's image.
