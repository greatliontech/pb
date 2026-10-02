//go:build !windows

package runner

// ExportStreams: an export on this platform carries the image's file
// modes, so the docker runner can hand it to the daemon as a stream
// (platforms.md REQ-plat-oci-substrate).
const ExportStreams = true
