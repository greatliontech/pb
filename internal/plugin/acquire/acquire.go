// Package acquire is the shape every plugin acquisition yields, whichever
// scheme produced it: the process to run, the lockfile pin the
// acquisition ran under, and — for an image — the image's facts behind
// a pointer a host binary leaves nil, so an absent image is never taken
// for an empty one. The generate verb consumes it in one shape; the
// oci and local acquirers produce it. It is a subpackage rather than
// the domain's root because it carries the lockfile's pin, and the
// lockfile imports the root.
package acquire

import (
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/plugin"
)

// Acquired is one plugin ready to run.
type Acquired struct {
	// Process is the process to run: argv, environment and working
	// directory. For an image it is the image config's process, zero
	// where the daemon pulls and applies the image's own
	// configuration; for a host binary, the binary's absolute path.
	Process plugin.Process
	// Pin is the lockfile pin the acquisition ran under, freshly
	// recorded on first use.
	Pin lockfile.PluginPin
	// Image is the image's facts; nil for a host binary.
	Image *Image
}

// Image is what an acquired image is run from: an exported root
// filesystem, or a reference the daemon runs — pulled at a verified
// digest, or held by the daemon already — and the manifest-list entry
// admitted for the platform.
type Image struct {
	// Rootfs is the exported root filesystem — the store's shared
	// export-cache entry; treat it as read-only. Empty where the
	// daemon pulls.
	Rootfs string
	// Reference is the image the daemon runs: with Pull, the
	// repository at the verified digest for the daemon to pull;
	// without, an image the daemon holds already, run as it is.
	// Empty where a rootfs was exported.
	Reference string
	// Pull says the daemon pulls Reference at its verified digest
	// before running it; a daemon-local image is run without.
	Pull bool
	// Platform is the manifest-list entry the seam admitted for the
	// platform — os/arch, with its variant where the entry states one
	// — the one child of the verified index the run uses: an export
	// already is that child; a daemon is told it (Reference) and
	// pulls exactly that.
	Platform string
}
