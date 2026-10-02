//go:build !linux && !darwin && !windows

package runner

import "testing"

func setupSandbox(m *testing.M) int { return m.Run() }

// requireSandbox skips: this platform has no sandbox row.
func requireSandbox(t *testing.T) {
	t.Helper()
	t.Skip("no sandbox row on this platform")
}
