# The host platform is spelled three ways across the plugin domain

Lands: package-structure plan chunk 7

Three packages of the plugin domain each spell the platform a plugin
runs on: oci's HostPlatform returns an oci.Platform{OS, Arch} the
acquirer's platform check reads; local's Platform returns the
"os/arch" string the lockfile keys a binary's pin by per host
platform; every runner reports its own through Platform() (os, arch)
— the native runner the host's, the docker runner the daemon's. The
first two are one fact, the host's, in two shapes; the third is the
runner's report, which the verbs compare against the host's. One
type on the domain root, plugin.Platform{OS, Arch} with its "os/arch"
spelling, would make the two spellings one and give the runner's
report the same type: the root imports nothing internal, so every
subpackage may take it. The lockfile's "os/arch" key is a wire fact
(module-lockfile.md) and stays as spelled; the collapse changes no
byte of it.
