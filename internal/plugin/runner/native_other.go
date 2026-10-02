//go:build !linux && !darwin && !windows

package runner

import (
	"fmt"
	"runtime"
)

// NativeRunner is unavailable on a platform with no sandbox row: the
// refusal names it and no run ever falls back to a bare exec
// (platforms.md REQ-plat-local-runner); the docker runner covers what
// a daemon can run (plugin-execution.md, REQ-plugin-runner-selection).
func NativeRunner() (Native, error) {
	return nil, fmt.Errorf("the native runner reaches no sandbox row on %s/%s", runtime.GOOS, runtime.GOARCH)
}
