# The docker runner's root is the image's but for the daemon's own mounts

Lands: user decision (whether REQ-plugin-sandboxed's "root
filesystem is the image's" admits a daemon's management mounts, and
whether the image's own configuration binding under a daemon image
is a named deviation of the `docker` runner)

REQ-plugin-sandboxed requires an `oci` plugin's root filesystem to be
the image's, read-only. A Docker daemon binds its own `/etc/hosts`,
`/etc/hostname` and `/etc/resolv.conf` over the container's root and
mounts a writable tmpfs at `/dev/shm`; it also injects a default
`PATH` into the environment where the image states none. None of
this is pb's to switch off through the daemon's API, and the native
runner presents none of it, so a plugin reading those paths — or
`PATH` — observes the substrate (REQ-plugin-runner-independence).
Relayed: either the requirement states the daemon's management
mounts as the `docker` runner's known deviation, or the runner is
the one that cannot conform.

A daemon image widens the same question to the image's own
configuration. Under the store byte path the export carries no
configuration but the process pb states (`docker import` discards
the rest), and the native runner applies none; under a daemon-local
override or the `docker` byte path (REQ-plugin-core-verifies) the
daemon applies the image's whole configuration: a `VOLUME` binds a
writable anonymous volume over the read-only root (released with
the container), a `USER` sets the uid, a `HEALTHCHECK` runs. So one
digest can answer differently by byte path — a plugin reading its
uid, or writing under a declared volume — which
REQ-plugin-runner-independence forbids. Relayed with the mounts:
either REQ-plugin-core-verifies names what the image's configuration
binds on this path as the runner's known deviation, or the runner
neutralizes it (no volumes, a fixed uid) at the cost of running the
image other than as its configuration says.
