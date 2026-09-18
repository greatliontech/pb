//go:build !linux

package plugrun

import (
	"errors"
)

// NativeRunner is unavailable off Linux: the docker runner covers
// darwin (plugin-execution.md, the runner term), and no run ever
// falls back silently.
func NativeRunner() (Runner, error) {
	return nil, errors.New("plugrun: the native runner is Linux-only; use the docker runner")
}
