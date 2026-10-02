//go:build linux

package runner

// NativeRunner returns the platform's native runner: sandbox's
// create-only backend on Linux.
func NativeRunner() (Native, error) {
	return &SandboxRunner{}, nil
}
