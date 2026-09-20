package runner

import (
	"fmt"
	"math"

	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/sandbox"
)

// tierOf names a reported isolation in the seam's vocabulary.
func tierOf(i sandbox.Isolation) (string, bool) {
	switch i {
	case sandbox.None:
		return plugin.TierNone, true
	case sandbox.Minimal:
		return plugin.TierMinimal, true
	case sandbox.OS:
		return plugin.TierOS, true
	case sandbox.Strong:
		return plugin.TierStrong, true
	}
	return "", false
}

// isolationOf reads the seam's tier floor into the sandbox's; an
// unrecognized or absent floor is a caller error — the default is the
// trust policy's to fold, never the runner's to invent.
func isolationOf(tier string) (sandbox.Isolation, error) {
	switch tier {
	case plugin.TierNone:
		return sandbox.None, nil
	case plugin.TierMinimal:
		return sandbox.Minimal, nil
	case plugin.TierOS:
		return sandbox.OS, nil
	case plugin.TierStrong:
		return sandbox.Strong, nil
	}
	return 0, fmt.Errorf("runner: refusing a run with no sandbox tier floor (%q)", tier)
}

// cpuSeconds sizes the CPU-time bound as the wall clock over the
// permitted cores, plus one: it can only fire on a plugin busier
// than the wall clock allows, and a plugin exactly at the clock is
// the wall clock's to end.
func cpuSeconds(l trust.Limits) uint64 {
	return uint64(math.Ceil(l.Timeout.Seconds()*l.CPU)) + 1
}
