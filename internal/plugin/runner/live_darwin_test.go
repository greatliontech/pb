//go:build darwin

package runner

import (
	"syscall"

	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/sandbox"
)

// liveRow is the row this platform's native runner delivers, which
// the live arms demand (platforms.md), liveTier its tier.
const liveRow = sandbox.OS

var liveTier = plugin.TierOS

// liveBounds is the accounting this platform's row reports.
const liveBounds = BoundsWatchdog

// cpuSignalledStatus is a death by SIGXCPU, the kernel's label for the
// CPU-time bound at its limit.
func cpuSignalledStatus() sandbox.ExitStatus {
	return sandbox.ExitStatus{Code: 152, Signaled: true, Signal: syscall.SIGXCPU}
}

// cpuSignalBound says whether a death by SIGXCPU is the CPU-time
// bound on this platform: the kernel's label on darwin alone.
const cpuSignalBound = true

// killedStatus is a bound's kill as this platform's sandbox reports it.
func killedStatus() sandbox.ExitStatus {
	return sandbox.ExitStatus{Code: 137, Signaled: true, Signal: syscall.SIGKILL}
}
