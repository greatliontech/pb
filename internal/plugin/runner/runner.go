// Package runner is pb's runner seam (plugin-execution.md, the runner
// term): one process from a prepared root filesystem, stdio only, run
// under resource bounds, with the achieved sandbox tier reported by
// the mechanism (REQ-plugin-reported-tier). Generated output is a pure
// function of the plugin content and the request under every runner
// (REQ-plugin-runner-independence) — a Result carries the tier, the
// bound mechanism, and nothing else the response could depend on.
package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/provenance/trust"
)

// Spec is one run: the identity scheme, the world — an oci run's
// image, one of plugin's three arms; a local run none, its Process a
// host binary run in the host's world with the host's environment,
// under the bounds alone (plugin-execution.md, "Local binaries") —
// the process, its standard input, the bounds and the tier floor.
type Spec struct {
	Scheme string
	// Image is the world an oci run is in: an *plugin.Export the
	// runner enters, a *plugin.Pulled the daemon pulls at the verified
	// digest (REQ-plugin-core-verifies), a *plugin.DaemonLocal the
	// daemon holds already (REQ-plugin-override); nil for a local run.
	Image   plugin.Image
	Process plugin.Process
	Stdin   []byte
	Limits  trust.Limits
	MinTier string
}

// Result is what a run reports: the plugin's stdout and stderr bytes,
// its exit status (128 plus the signal number for a signal death),
// the isolation tier the substrate achieved, and the accounting that
// enforced the memory and process bounds. The tier is spelled in
// plugin's vocabulary — a wire fact of the trust policy and the
// lockfile — and each runner maps its own substrate's report onto it,
// never the other way round. Bounds is reporting only —
// REQ-plugin-resource-bounds mandates bounds, not a mechanism — so a
// bound failure is attributable to a specific enforcement.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Tier     string
	Bounds   Accounting
}

// Accounting names the mechanism that enforced a run's memory and
// process bounds, and counted the CPU-time bound's kill where it
// could: the kernel's cgroups or POSIX rlimits on Linux, the
// sandbox's watchdog on darwin, the Job Object on windows, the
// daemon's record under the docker runner (REQ-plugin-resource-bounds).
type Accounting string

const (
	BoundsCgroups   Accounting = "cgroups"
	BoundsRlimits   Accounting = "rlimits"
	BoundsWatchdog  Accounting = "watchdog"
	BoundsJobObject Accounting = "job-object"
)

// ErrBoundExceeded is the class of a run terminated by a resource
// bound; the wrapping error names the bound and the mechanism that
// enforced it.
var ErrBoundExceeded = errors.New("plugin exceeded a resource bound")

// ErrTierUnreachable is the class of a run refused because the host
// reaches no sandbox tier at or above Spec.MinTier; the wrapping
// error states the tier the host reaches and why. Nothing ran.
var ErrTierUnreachable = errors.New("the host cannot reach the required sandbox tier")

// Runner executes one plugin process (REQ-plugin-sandboxed for the
// native runner; each runner reports its own tier) on a substrate
// whose platform it names, so acquisition selects the image for the
// platform the plugin will run on (REQ-plugin-platform-strict) — a
// daemon's containers run the daemon's platform, not the host's.
type Runner interface {
	Run(ctx context.Context, spec Spec) (*Result, error)
	Platform() plugin.Platform
}

// checkLimits refuses a Spec with an unbounded resource: bounds are
// mandatory for every run, and the defaults are the trust policy's to
// fold, not the runner's to invent.
func checkLimits(l trust.Limits) error {
	if l.Memory == 0 || l.CPU <= 0 || l.Pids == 0 || l.Timeout <= 0 {
		return fmt.Errorf("runner: refusing an unbounded run (memory %d, cpu %g, pids %d, timeout %s)", l.Memory, l.CPU, l.Pids, l.Timeout)
	}
	return nil
}

// beforeStart reports a failure on the way to the start: the caller's
// cancellation, where the context ended, is what happened, whatever
// step it ended; otherwise err as it is.
func beforeStart(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("runner: plugin run cancelled: %w", ctx.Err())
	}
	return err
}

// pluginHostname is the hostname a run presents wherever its row
// presents one — the docker runner's container, the native runner's
// Strong row: a plugin there never observes the host's, or a per-run
// one (REQ-plugin-runner-independence). The native runner's OS row
// presents none, and a plugin there observes the host's
// (REQ-plugin-sandboxed).
const pluginHostname = "pb-plugin"

// DaemonImages marks a runner that runs a daemon image (a Pulled or a
// DaemonLocal world): the docker runner alone. A daemon-local
// override, and the docker byte path, are refused before anything
// runs unless the selected runner is one.
type DaemonImages interface {
	RunsDaemonImages()
}

// CheckSpec refuses a Spec no runner runs: unbounded limits, a
// scheme and a world that disagree, or a process with no argv where
// no daemon image supplies one. It is the one preamble every runner
// applies first — each adds only the refusals of its own substrate
// after it — and a stand-in runner applies it too, so a spec no
// runner would run is refused where it is built.
func CheckSpec(spec Spec) error {
	if err := checkLimits(spec.Limits); err != nil {
		return err
	}
	if err := checkScheme(spec); err != nil {
		return err
	}
	if len(spec.Process.Argv) == 0 && !daemonSupplies(spec.Image) {
		return errors.New("runner: the plugin process has no argv")
	}
	return nil
}

// daemonImage is the image as the daemon is told it, for a world the
// daemon holds or pulls: a pulled image's repository at its digest, a
// daemon-local image's reference; none for an export or no world.
// The one switch over the daemon's arms: what the daemon supplies the
// process for is what it is told an image for. It reads a world the
// scheme check has passed, a typed nil refused there.
func daemonImage(img plugin.Image) (string, bool) {
	switch img := img.(type) {
	case *plugin.Pulled:
		return img.Reference(), true
	case *plugin.DaemonLocal:
		return img.Reference, true
	}
	return "", false
}

// daemonSupplies reports whether the daemon applies the image's own
// process: a world the daemon is told an image for.
func daemonSupplies(img plugin.Image) bool {
	_, ok := daemonImage(img)
	return ok
}

// checkScheme refuses a Spec whose scheme and world disagree: an oci
// run is in one world, its facts complete — an export's rootfs, a
// pulled image's repository, digest and admitted entry, a daemon
// image's reference — a local run in none, and no other scheme
// runs. Which world, and that it is one, the type says.
func checkScheme(spec Spec) error {
	switch spec.Scheme {
	case plugin.SchemeOCI:
		// A typed nil is no world either: refused here, where the spec
		// is built, never dereferenced by a runner.
		switch img := spec.Image.(type) {
		case *plugin.Export:
			if img == nil || img.Rootfs == "" {
				return errors.New("runner: an export names no rootfs")
			}
		case *plugin.Pulled:
			if img == nil || img.Repository == "" || img.Digest == "" {
				return errors.New("runner: the daemon pulls a verified digest, and none is named")
			}
			if img.Entry == "" {
				return errors.New("runner: the daemon pulls the admitted entry, and none is named")
			}
		case *plugin.DaemonLocal:
			if img == nil || img.Reference == "" {
				return errors.New("runner: a daemon image names no reference")
			}
		default:
			return errors.New("runner: an oci run has a world, an export or a daemon image")
		}
	case plugin.SchemeLocal:
		if spec.Image != nil {
			return errors.New("runner: a local run carries a world of its own")
		}
	default:
		return fmt.Errorf("runner: refusing a run with no identity scheme (%q)", spec.Scheme)
	}
	return nil
}
