package runner

// ExportStreams: an export on windows carries no file modes — the
// filesystem has none — so a stream of it would hand the daemon an
// image whose entrypoint no longer executes; the docker runner's byte
// path here is the daemon's pull alone (platforms.md
// REQ-plat-oci-substrate).
const ExportStreams = false
