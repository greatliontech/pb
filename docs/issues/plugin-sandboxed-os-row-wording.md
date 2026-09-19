# The sandboxed-plugin wording assumes the Strong row's world

Lands: when pb pins a sandbox release carrying the Linux OS row

REQ-plugin-sandboxed states the plugin's world as the Strong row
presents it: "its root filesystem is the image's, read-only" — the
export at `/`. sandbox's Linux OS row (docs/specs/sandbox.md, the
ladder), reached by a host without unprivileged user namespaces,
bounds the world to the same export by a Landlock allowlist at the
export's host path: the tree is readable and never written, the
network is denied at the socket, and only a static entrypoint loads,
but the export is not at `/` and an image-absolute path the plugin
dereferences at runtime resolves on the host. REQ-plugin-min-tier
admits that row through an explicit lowering, so once pb pins the
sandbox release that delivers it, a lowered policy runs a plugin in
that world and the requirement's wording must state it: the world is
the export as `Root` bounds it on the row that ran, at `/` on the
Strong row and at its host path on the OS row, where the entrypoint
must be static. The runner's own refusals (a dynamic entrypoint, a
hostname) are the row's, reported through the sandbox's undeliverable
error as any other.
