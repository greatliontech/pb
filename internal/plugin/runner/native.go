//go:build linux || darwin || windows

package runner

// NativeRunner returns the platform's native runner: the sandbox's
// row for the host (platforms.md REQ-plat-local-runner,
// REQ-plat-oci-substrate).
func NativeRunner() (Native, error) {
	return &SandboxRunner{}, nil
}
