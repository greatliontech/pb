//go:build linux

package plugrun

// NativeRunner returns the platform's native runner: sandbox's
// create-only backend on Linux.
func NativeRunner() (Runner, error) {
	return &SandboxRunner{}, nil
}
