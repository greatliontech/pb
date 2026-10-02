//go:build windows

package runner

import (
	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/sandbox"
)

// liveRow is the row this platform's native runner delivers, which
// the live arms demand (platforms.md), liveTier its tier.
const liveRow = sandbox.OS

var liveTier = plugin.TierOS

// liveBounds is the accounting this platform's row reports.
const liveBounds = BoundsJobObject

// cpuSignalledStatus: the platform has no signals; a plain failure.
func cpuSignalledStatus() sandbox.ExitStatus { return sandbox.ExitStatus{Code: 152} }

// cpuSignalBound says whether a death by SIGXCPU is the CPU-time
// bound on this platform: the kernel's label on darwin alone.
const cpuSignalBound = false

// killedStatus is a bound's kill as this platform's sandbox reports it.
func killedStatus() sandbox.ExitStatus { return sandbox.ExitStatus{Code: sandboxKillExitCode} }
