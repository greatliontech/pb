//go:build linux

package runner

import (
	"syscall"

	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/sandbox"
)

// liveRow is the row this platform's native runner delivers, which
// the live arms demand (platforms.md), liveTier its tier.
const liveRow = sandbox.Strong

var liveTier = plugin.TierStrong

// liveBounds is the accounting the happy path reports here: cgroups
// under a delegated scope, rlimits otherwise — read from the run.
var liveBounds = Accounting("")

// cpuSignalledStatus is a death by SIGXCPU, the kernel's label for the
// CPU-time bound at its limit.
func cpuSignalledStatus() sandbox.ExitStatus {
	return sandbox.ExitStatus{Code: 152, Signaled: true, Signal: syscall.SIGXCPU}
}

// cpuSignalBound says whether a death by SIGXCPU is the CPU-time
// bound on this platform: the kernel's label on darwin alone.
const cpuSignalBound = false

// cpuKillCounted says whether this platform's accounting counts the
// CPU-time bound's kill on its own: the watchdog's and the Job's
// kills are their own; the Linux kernel's at RLIMIT_CPU is told from
// the dead process's CPU clock, the one the limit is checked on (the
// sandbox's "Bounded means bounded").
const cpuKillCounted = true

// killedStatus is a bound's kill as this platform's sandbox reports it.
func killedStatus() sandbox.ExitStatus {
	return sandbox.ExitStatus{Code: 137, Signaled: true, Signal: syscall.SIGKILL}
}
