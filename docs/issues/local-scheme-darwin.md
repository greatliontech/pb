# The local scheme runs nowhere without a native runner

Lands: sandbox delivers a darwin row: a Start on darwin succeeds and
reports a tier

A `local` plugin runs on the native runner — a host binary runs on
the host, under the resource bounds every scheme carries
(REQ-plugin-resource-bounds). pb has a native runner on Linux only:
sandbox's darwin backend is a placeholder, and no other mechanism
bounds a host process there. So on macOS, which the platform scope
supports through the `docker` runner for `oci` plugins, every `local`
entry is refused before anything runs, naming the platform. The
requirements for the scheme (REQ-plugin-local-resolution,
REQ-plugin-local-pin) are unconditional and stay so; the gap is the
runner's.
