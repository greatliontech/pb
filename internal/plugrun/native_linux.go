//go:build linux

package plugrun

// NativeRunner returns the platform's native runner: container's
// pure-Go create path on Linux.
func NativeRunner() (Runner, error) {
	return &ContainerRunner{}, nil
}
