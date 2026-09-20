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
	"strings"

	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/provenance/trust"
)

// Spec is one plugin run: the image rootfs, its process, the request
// bytes on stdin, the bounds, and the tier floor. Limits is the trust
// policy's effective posture (trust.Execution.EffectiveLimits): every
// field set. A zero field is an unbounded resource, and a runner
// refuses it rather than guessing (REQ-plugin-resource-bounds).
// MinTier is the policy's effective floor (trust.Execution
// .EffectiveMinTier), a plugin tier name: a runner whose host
// cannot reach it runs nothing and fails with ErrTierUnreachable
// (REQ-plugin-min-tier); an absent floor is refused like an
// unbounded resource.
//
// Scheme is the entry's identity scheme: an oci run has its world in
// Rootfs — the image export — or in Reference, which only the docker
// runner consumes: a daemon-local image an override names
// (REQ-plugin-override), or, with Pull set, the repository at the
// digest pb verified for the daemon to pull
// (REQ-plugin-core-verifies); a local run has neither, its
// Process a host binary run in the host's world with the host's
// environment, under the bounds alone (plugin-execution.md, "Local
// binaries").
type Spec struct {
	Scheme    string
	Rootfs    string
	Reference string
	// Pull marks Reference as the registry's repository at a digest pb
	// verified, for the daemon to pull (REQ-plugin-core-verifies);
	// unset, Reference is a daemon-local image the daemon already holds.
	Pull bool
	// Entry is the manifest-list entry the seam admitted for a
	// pulled image — os/arch, with its variant where stated — the one
	// child the daemon is to pull and run; empty otherwise.
	// The acquisition knows the admitted entry for an export too; the
	// verb hands it on for a pulled image alone, so a spec never
	// carries an acquisition's image facts whole.
	Entry   string
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
// process bounds; CPU time is a POSIX rlimit under either.
type Accounting string

const (
	BoundsCgroups Accounting = "cgroups"
	BoundsRlimits Accounting = "rlimits"
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

// DaemonImages marks a runner that runs a daemon image (Spec.Reference,
// daemon-local or pulled): the docker runner alone. A daemon-local
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
	if len(spec.Process.Argv) == 0 && spec.Reference == "" {
		return errors.New("runner: the plugin process has no argv")
	}
	return nil
}

// checkScheme refuses a Spec whose scheme and world disagree: an oci
// run has exactly one of a rootfs and a daemon image, an image the
// daemon is to pull names a digest, a local run has neither world,
// and no other scheme runs.
func checkScheme(spec Spec) error {
	switch spec.Scheme {
	case plugin.SchemeOCI:
		if (spec.Rootfs == "") == (spec.Reference == "") {
			return errors.New("runner: an oci run has exactly one of a rootfs and a daemon image")
		}
		if spec.Pull && !strings.Contains(spec.Reference, "@") {
			return fmt.Errorf("runner: the daemon pulls a verified digest, and %q names none", spec.Reference)
		}
		if spec.Pull && spec.Entry == "" {
			return errors.New("runner: the daemon pulls the admitted entry, and none is named")
		}
		if !spec.Pull && spec.Entry != "" {
			return errors.New("runner: an entry is named for a pulled image alone")
		}
	case plugin.SchemeLocal:
		if spec.Rootfs != "" || spec.Reference != "" || spec.Pull {
			return errors.New("runner: a local run carries a world of its own")
		}
	default:
		return fmt.Errorf("runner: refusing a run with no identity scheme (%q)", spec.Scheme)
	}
	return nil
}
