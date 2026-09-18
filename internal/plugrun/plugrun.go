// Package plugrun is pb's runner seam (plugin-execution.md, the runner
// term): one process from a prepared root filesystem, stdio only, run
// under resource bounds, with the achieved sandbox tier reported by
// the mechanism (REQ-plugin-reported-tier). Generated output is a pure
// function of the plugin content and the request under every runner
// (REQ-plugin-runner-independence) — a Result carries the tier, the
// bound mechanism, and nothing else the response could depend on.
package plugrun

import (
	"context"
	"errors"
	"fmt"

	"github.com/greatliontech/pb/internal/plugexec"
	"github.com/greatliontech/pb/internal/trust"
)

// Spec is one plugin run: the image rootfs, its process, the request
// bytes on stdin, and the bounds. Limits is the trust policy's
// effective posture (trust.Execution.EffectiveLimits): every field
// set. A zero field is an unbounded resource, and a runner refuses it
// rather than guessing (REQ-plugin-resource-bounds).
type Spec struct {
	Rootfs  string
	Process plugexec.Process
	Stdin   []byte
	Limits  trust.Limits
}

// Result is what a run reports: the plugin's stdout and stderr bytes,
// its exit status (128 plus the signal number for a signal death),
// the isolation tier the mechanism achieved, and the resource-bound
// mechanism that was in effect. Bounds is reporting only —
// REQ-plugin-resource-bounds mandates bounds, not a mechanism — so a
// bound failure is attributable to a specific enforcement.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Tier     string
	Bounds   string
}

// Bound mechanisms a runner reports in Result.Bounds.
const (
	BoundsCgroups = "cgroups"
	BoundsRlimits = "rlimits"
)

// ErrBoundExceeded is the class of a run terminated by a resource
// bound; the wrapping error names the bound and the mechanism that
// enforced it.
var ErrBoundExceeded = errors.New("plugin exceeded a resource bound")

// Runner executes one plugin process (REQ-plugin-sandboxed for the
// native runner; each runner reports its own tier).
type Runner interface {
	Run(ctx context.Context, spec Spec) (*Result, error)
}

// checkLimits refuses a Spec with an unbounded resource: bounds are
// mandatory for every run, and the defaults are the trust policy's to
// fold, not the runner's to invent.
func checkLimits(l trust.Limits) error {
	if l.Memory == 0 || l.CPU <= 0 || l.Pids == 0 || l.Timeout <= 0 {
		return fmt.Errorf("plugrun: refusing an unbounded run (memory %d, cpu %g, pids %d, timeout %s)", l.Memory, l.CPU, l.Pids, l.Timeout)
	}
	return nil
}
