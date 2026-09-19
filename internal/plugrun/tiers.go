package plugrun

import (
	"fmt"
	"math"

	"github.com/greatliontech/pb/internal/plugexec"
	"github.com/greatliontech/pb/internal/trust"
	"github.com/greatliontech/sandbox"
)

// tierOf names a reported isolation in the seam's vocabulary.
func tierOf(i sandbox.Isolation) (string, bool) {
	switch i {
	case sandbox.None:
		return plugexec.TierNone, true
	case sandbox.Minimal:
		return plugexec.TierMinimal, true
	case sandbox.OS:
		return plugexec.TierOS, true
	case sandbox.Strong:
		return plugexec.TierStrong, true
	}
	return "", false
}

// isolationOf reads the seam's tier floor into the sandbox's; an
// unrecognized or absent floor is a caller error — the default is the
// trust policy's to fold, never the runner's to invent.
func isolationOf(tier string) (sandbox.Isolation, error) {
	switch tier {
	case plugexec.TierNone:
		return sandbox.None, nil
	case plugexec.TierMinimal:
		return sandbox.Minimal, nil
	case plugexec.TierOS:
		return sandbox.OS, nil
	case plugexec.TierStrong:
		return sandbox.Strong, nil
	}
	return 0, fmt.Errorf("plugrun: refusing a run with no sandbox tier floor (%q)", tier)
}

// cpuSeconds sizes the CPU-time bound as the wall clock over the
// permitted cores, plus one: it can only fire on a plugin busier
// than the wall clock allows, and a plugin exactly at the clock is
// the wall clock's to end.
func cpuSeconds(l trust.Limits) uint64 {
	return uint64(math.Ceil(l.Timeout.Seconds()*l.CPU)) + 1
}
