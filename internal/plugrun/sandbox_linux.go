//go:build linux

package plugrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"syscall"

	"github.com/greatliontech/pb/internal/plugexec"
	"github.com/greatliontech/pb/internal/trust"
	"github.com/greatliontech/sandbox"
)

// SandboxRunner is the native runner: sandbox's create-only Linux
// backend behind the runner seam. pb's request is intent only — the
// image export as the Root, no grants, no network, the policy's
// limits, the tier floor as MinTier — and the report is the
// backend's: the tier of the row that fully applied, and the
// accounting that enforced the memory and process bounds
// (REQ-plugin-sandboxed, REQ-plugin-resource-bounds,
// REQ-plugin-reported-tier). The export is never written — a shared,
// read-only tree is a valid Root — so concurrent runs of one image
// share it. Where pb's own cgroup is delegated, sandbox may
// reorganize that cgroup to place the run's beside pb: its member
// processes, pb's included, move into a leaf child for the cgroup's
// lifetime; accounting outside the delegated cgroup is unchanged.
// The memory bound is the whole of what the plugin may hold: under
// cgroups the sandbox closes swap to the run, and under rlimits the
// address-space cap includes it — as the docker runner's record must
// show swap capped at the bound.
type SandboxRunner struct {
	// create makes the run's sandbox; nil means sandbox.New. Tests set
	// it to drive the post-wait reading with a sandbox of their own.
	create func(sandbox.Spec) (sandbox.Sandbox, error)
}

// Platform is the host's: the sandbox runs the host's kernel.
func (r *SandboxRunner) Platform() (string, string) { return runtime.GOOS, runtime.GOARCH }

// Run executes the plugin process (REQ-plugin-response-authority's
// transport half: stdout and stderr are collected verbatim; judgment
// is the caller's). The wall clock is the runner's — sandbox adds no
// timeout of its own, and the end of its Start context kills the
// whole run by the strongest tie the host holds. A run a bound
// terminated returns ErrBoundExceeded naming the bound and the
// accounting that attributes it (outcome).
func (r *SandboxRunner) Run(ctx context.Context, spec Spec) (result *Result, err error) {
	limits := spec.Limits
	if err := checkLimits(limits); err != nil {
		return nil, err
	}
	if len(spec.Process.Argv) == 0 {
		return nil, errors.New("plugrun: the plugin process has no argv")
	}
	if err := checkScheme(spec); err != nil {
		return nil, err
	}
	if spec.Image != "" {
		return nil, errors.New("plugrun: a daemon-local image runs on the docker runner only")
	}
	floor, err := isolationOf(spec.MinTier)
	if err != nil {
		return nil, err
	}
	if err := plugexec.CheckEnv(spec.Process.Env); err != nil {
		return nil, fmt.Errorf("plugrun: %v", err)
	}
	var stdout, stderr bytes.Buffer
	create := r.create
	if create == nil {
		create = sandbox.New
	}
	sb, err := create(specOf(spec, floor, &stdout, &stderr))
	if err != nil {
		return nil, fmt.Errorf("plugrun: %w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	if err := sb.Start(runCtx); err != nil {
		return nil, startError(spec.Scheme, err, stderr.Bytes())
	}
	defer func() {
		// Destroy releases the run's accounting; a leak is a runner
		// failure even after a clean run.
		if derr := sb.Destroy(); derr != nil && err == nil {
			result, err = nil, fmt.Errorf("plugrun: releasing the run's resources: %w", derr)
		}
	}()
	es, werr := sb.Wait()
	if waitFailed(werr, runCtx.Err()) {
		return nil, fmt.Errorf("plugrun: waiting for the plugin process: %w (stderr: %s)", werr, tailBytes(stderr.Bytes()))
	}
	st, err := sb.Stats()
	if err != nil {
		return nil, fmt.Errorf("plugrun: reading the run's accounting: %w", err)
	}
	tier, ok := tierOf(sb.Tier())
	if !ok || (st.Accounting != sandbox.AccountingCgroups && st.Accounting != sandbox.AccountingRlimits) {
		return nil, fmt.Errorf("plugrun: the sandbox reported tier %v and accounting %v for a bounded run", sb.Tier(), st.Accounting)
	}
	if err := outcome(es, st, limits, runCtx.Err(), ctx.Err(), werr); err != nil {
		return nil, fmt.Errorf("%w (stderr: %s)", err, tailBytes(stderr.Bytes()))
	}
	return &Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: es.Code, Tier: tier, Bounds: accountingOf(st.Accounting)}, nil
}

// accountingOf names a reported accounting in the seam's vocabulary.
func accountingOf(a sandbox.Accounting) Accounting {
	switch a {
	case sandbox.AccountingCgroups:
		return BoundsCgroups
	case sandbox.AccountingRlimits:
		return BoundsRlimits
	}
	return Accounting(a.String())
}

// specOf is the whole of pb's intent for one run. An oci run: the
// image export as the Root, the process from the image's config, an
// empty network, a fixed hostname — a plugin never observes the
// host's, so its output cannot depend on it
// (REQ-plugin-runner-independence) — the policy's limits, and the
// policy's floor. A local run: the host binary in the host's world,
// the host's environment and network, no hostname of its own, the
// policy's limits, and any row the host affords.
func specOf(spec Spec, floor sandbox.Isolation, stdout, stderr io.Writer) sandbox.Spec {
	out := sandbox.Spec{
		Exec:    spec.Process.Argv[0],
		Args:    spec.Process.Argv[1:],
		Env:     spec.Process.Env,
		WorkDir: spec.Process.WorkDir,
		Limits: sandbox.Limits{
			MemoryBytes: spec.Limits.Memory,
			CPUSeconds:  cpuSeconds(spec.Limits),
			MaxProcs:    spec.Limits.Pids,
		},
		MinTier: floor,
		Stdin:   bytes.NewReader(spec.Stdin),
		Stdout:  stdout,
		Stderr:  stderr,
	}
	if spec.Scheme == plugexec.SchemeLocal {
		out.Network = true
		return out
	}
	out.Root = spec.Rootfs
	out.Network = false
	out.Hostname = "pb-plugin"
	return out
}

// startError reads a Start refusal: a host below the tier floor is
// ErrTierUnreachable carrying the sandbox's own statement of the row
// it reaches (REQ-plugin-min-tier); an intent the host's row cannot
// deliver at all — a Root to restrict the world to, a denied network
// — is named as such, since no floor lowers past what
// REQ-plugin-sandboxed requires of every run; anything else is the
// run failing to start, with the plugin's stderr so far.
func startError(scheme string, err error, stderr []byte) error {
	if errors.Is(err, sandbox.ErrWeakerThanRequired) {
		return fmt.Errorf("%w: %v", ErrTierUnreachable, err)
	}
	if errors.Is(err, sandbox.ErrUndeliverable) {
		if scheme == plugexec.SchemeLocal {
			return fmt.Errorf("plugrun: the sandbox row this host reaches cannot run this local plugin: %w", err)
		}
		return fmt.Errorf("plugrun: the sandbox row this host reaches cannot deliver an oci plugin's isolation (a read-only image root and no network are required at every tier, so lowering the tier floor cannot help): %w", err)
	}
	return fmt.Errorf("plugrun: starting the plugin process: %w (stderr: %s)", err, tailBytes(stderr))
}

// waitFailed tells a wait that failed from a wait that reports the
// run context's own end: the kill the context issued can land on a
// payload already exiting, and the sandbox then returns the context's
// error with no exit status — an outcome of the clock or the caller's
// cancellation (outcome), not a failure to wait.
func waitFailed(werr, clock error) bool {
	return werr != nil && !(clock != nil && errors.Is(werr, clock))
}

// outcome reads a finished run into its report, in the order the
// facts bind (REQ-plugin-resource-bounds): a bound the accounting
// counted names itself whatever else happened — a memory kill the
// cgroup counted is the memory bound, a fork it refused the process
// bound if the plugin then failed; then the run context's end — the
// caller's cancellation, or the wall clock, whose kill can land on a
// payload already exiting, in which case the wait error is the
// context's own and reads the same way; then a SIGKILL nothing
// counted, the CPU-time bound — RLIMIT_CPU under every accounting,
// whose hard limit is the forced, unlabeled kill a namespace init
// receives — or an external kill, which no accounting tells apart,
// so neither is claimed. Rlimits count nothing: a refused allocation
// or fork fails the plugin on its own terms, surfaced verbatim. On
// the one path where the wait error is the context's own — the
// payload exited clean as the kill landed — the sandbox releases its
// cgroup before reading the counters, so a bound counted in that
// same instant is not seen and the clock names the end.
func outcome(es sandbox.ExitStatus, st sandbox.Stats, l trust.Limits, clock, parent, werr error) error {
	if st.MemoryKills > 0 {
		return fmt.Errorf("%w: memory (%d bytes) (enforced by %s)", ErrBoundExceeded, l.Memory, st.Accounting)
	}
	if st.ForksRefused > 0 && es.Code != 0 {
		return fmt.Errorf("%w: process count (%d) (enforced by %s)", ErrBoundExceeded, l.Pids, st.Accounting)
	}
	if clock != nil && (es.Signaled || werr != nil) {
		if parent != nil {
			return fmt.Errorf("plugrun: plugin run cancelled: %w", parent)
		}
		return fmt.Errorf("%w: wall clock (%s, enforced by the runner)", ErrBoundExceeded, l.Timeout)
	}
	if es.Signaled && es.Signal == syscall.SIGKILL {
		return fmt.Errorf("plugrun: plugin killed by SIGKILL: the CPU-time bound (%s over %g cores, enforced by rlimits) or an external kill — rlimits cannot attribute which", l.Timeout, l.CPU)
	}
	return nil
}
