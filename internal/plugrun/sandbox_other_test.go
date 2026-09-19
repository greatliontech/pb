//go:build !linux

package plugrun

import "testing"

func setupSandbox(m *testing.M) int { return m.Run() }

// requireSandbox skips: the native runner is Linux-only.
func requireSandbox(t *testing.T) {
	t.Helper()
	t.Skip("the native runner is Linux-only")
}
