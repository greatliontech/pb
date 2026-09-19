# The docker runner's root is the image's but for the daemon's own mounts

Lands: user decision (whether REQ-plugin-sandboxed's "root
filesystem is the image's" admits a daemon's management mounts)

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
