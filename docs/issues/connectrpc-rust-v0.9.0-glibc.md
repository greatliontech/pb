# connectrpc/rust v0.9.0 was published glibc-linked, before the kind's Linux trees went static

The plugin catalog (greatliontech/pb-plugins) builds the rust kind's
Linux trees for cargo's musl targets, static, so an OS sandbox row
loads them natively. connectrpc/rust v0.9.0, the one version the
catalog serves, was published before that rule: its Linux images
carry a glibc-linked executable over the catalog's base, which the
OS row refuses (a dynamically linked entrypoint does not load there),
so on such a host it runs under the docker runner, as the node,
release and bazel kinds' plugins do. The catalog's pipeline never
rebuilds a published tag: the plan leaves a signed tag out and pb
refuses to publish over one, so the static build reaches
connectrpc/rust with the next version upstream releases and the bump
appends.

Two defensible contracts:

1. The published tag stands: v0.9.0 runs under the docker runner on
   OS-row hosts until upstream's next version, the first the catalog
   publishes static, and the never-rebuilt promise holds for every
   tag ever published.
2. v0.9.0's package version is deleted from the registry by hand and
   the next publish run builds it again, static: pb's own
   tag-permanence invariant (REQ-publish-immutable, a tag meaning
   one image forever, as a lockfile's pin has it) is broken once,
   stepped around rather than through pb's refusal to publish over
   a tag, and every lockfile pinning the first publish's digest
   (REQ-lock-plugin-entry) refuses generation under
   REQ-lock-digest-enforcement until re-resolved, the tag now naming
   another image.

The tradeoff is the user's: a tag's permanence against the one
version's native run on OS-row hosts.

Lands: user decision.
