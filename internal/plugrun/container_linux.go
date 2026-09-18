//go:build linux

package plugrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/greatliontech/container"
	"github.com/greatliontech/pb/internal/plugexec"
	"github.com/greatliontech/pb/internal/trust"
	"golang.org/x/sys/unix"
)

// ContainerRunner is the native runner: container's pure-Go create
// path — fresh namespaces, pivot into the image rootfs bind-mounted
// read-only, no network, capability drop, seccomp, no /dev, and
// resource bounds (REQ-plugin-sandboxed, REQ-plugin-resource-bounds).
// Success derives tier Strong from the full application: every
// mechanism either applied or the run failed — there is no partial
// path — so the report is the mechanism's, not an assumption
// (REQ-plugin-reported-tier; a container-side applied-mechanism report
// is tracked ecosystem work).
type ContainerRunner struct {
	// StateDir holds per-run container state; a temp dir when empty.
	StateDir string

	// cgroupsAvailable picks the bound mechanism; nil means
	// container.CgroupsAvailable. Tests set it to exercise both
	// mechanisms on one host.
	cgroupsAvailable func() bool
}

func (r *ContainerRunner) cgroups() bool {
	if r.cgroupsAvailable != nil {
		return r.cgroupsAvailable()
	}
	return container.CgroupsAvailable()
}

// Run executes the plugin process (REQ-plugin-response-authority's
// transport half: stdout and stderr are collected verbatim; judgment
// is the caller's). A run the bounds terminated returns
// ErrBoundExceeded naming the bound and the mechanism wherever the
// mechanism attributes the termination: the wall clock always, cgroup
// memory kills and refused forks from the kernel's event counters.
// Under rlimits CPU-time exhaustion arrives as a SIGKILL the kernel
// does not label — the plugin is its pid namespace's init, to which
// the kernel delivers no ordinary signal such as SIGXCPU, only the
// forced kill at the hard limit — so that death is reported as either
// the CPU-time bound or an external kill, never claimed as one. An
// rlimit refusal of memory or a fork is not a termination — the plugin
// fails on its own terms and its failure is surfaced verbatim.
func (r *ContainerRunner) Run(ctx context.Context, spec Spec) (result *Result, err error) {
	limits := spec.Limits
	if err := checkLimits(limits); err != nil {
		return nil, err
	}
	if len(spec.Process.Argv) == 0 {
		return nil, errors.New("plugrun: the plugin process has no argv")
	}
	state := r.StateDir
	if state == "" {
		var err error
		state, err = os.MkdirTemp("", "pb-plugrun-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(state)
	}
	target, err := os.MkdirTemp(state, "root-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(target)

	cfg := container.DefaultConfig()
	cfg.Root = target
	// container's ReadonlyRoot remounts post-pivot repeating the locked
	// mount flags, so the root the plugin sees is read-only even inside
	// an unprivileged user namespace. The pivot itself still writes a
	// transient scratch directory into the new root — the shared
	// export handed here — before that remount lands; see
	// docs/issues/plugrun-pivot-scratch-shared-rootfs.md.
	cfg.ReadonlyRoot = true
	cfg.Hostname = "pb-plugin"
	// A protoc plugin is a stdio filter: no /dev, no /proc, and no
	// mask paths (there is nothing mounted to mask). This keeps
	// scratch-based plugin images first-class — the read-only rootfs
	// need not contain any mountpoint directories at all.
	cfg.SetupDev = false
	cfg.Devices = nil
	cfg.MaskPaths = nil
	cfg.ReadonlyPaths = nil
	// A bind mount carries no filesystem type; the kernel ignores the
	// field under MS_BIND.
	cfg.Mounts = []container.Mount{
		{Source: spec.Rootfs, Target: target, Type: "none", Flags: syscall.MS_BIND},
	}
	// Bounds are mandatory (REQ-plugin-resource-bounds); the mechanism
	// is not. Cgroups where placement is available; otherwise POSIX
	// rlimits, with the wall clock enforced by the runner either way.
	// An interactive rootless session often has no writable cgroup
	// subtree (its scope sits beside user@.service), and refusing to
	// bound there would make the bound mandatory in name only. Where
	// placement is available because pb's own cgroup is delegated —
	// a Delegate=yes scope, a container's namespace root — cgroup v2
	// lets that cgroup hold either processes or child controllers,
	// never both, so container reorganizes it: every member process,
	// pb and its neighbours alike, moves into a leaf child so the
	// run's cgroup can be bounded beside it. Placement within the
	// delegated cgroup changes; nothing outside it does
	// (docs/issues/plugrun-cgroups-delegated-placement.md).
	mechanism := BoundsCgroups
	if r.cgroups() {
		cfg.CgroupsRequired = true
		cfg.Resources = cgroupResources(limits)
	} else {
		mechanism = BoundsRlimits
		cfg.Rlimits = rlimits(limits)
	}

	var stdout, stderr bytes.Buffer
	proc := &container.Process{
		Cmd:     spec.Process.Argv[0],
		Args:    spec.Process.Argv[1:],
		Env:     spec.Process.Env,
		WorkDir: spec.Process.WorkDir,
		Stdin:   bytes.NewReader(spec.Stdin),
		Stdout:  &stdout,
		Stderr:  &stderr,
	}

	runCtx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()

	cont := container.New(fmt.Sprintf("pb-%d", time.Now().UnixNano()), cfg)
	if err := cont.Run(proc); err != nil {
		return nil, fmt.Errorf("plugrun: starting the plugin process: %w (stderr: %s)", err, tailBytes(stderr.Bytes()))
	}
	defer func() {
		// Destroy releases the run's cgroup; a leak is a runner failure
		// even after a clean run.
		if derr := destroy(cont); derr != nil && err == nil {
			result, err = nil, fmt.Errorf("plugrun: releasing the run's resources: %w", derr)
		}
	}()
	// The cgroup is located while the process is alive: its procfs
	// entry vanishes once it is reaped, while the cgroup directory —
	// and its event counters — outlive the process until Destroy.
	var cgroupDir string
	if mechanism == BoundsCgroups {
		cgroupDir, err = cgroupDirOf(cont.Pid())
		if err != nil {
			_ = cont.Signal(syscall.SIGKILL)
			_ = cont.Wait()
			return nil, fmt.Errorf("plugrun: locating the run's cgroup: %w", err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- cont.Wait() }()
	var waitErr error
	select {
	case <-runCtx.Done():
		// Killing the container's init takes its whole pid namespace
		// with it, so no descendant outlives the bound.
		_ = cont.Signal(syscall.SIGKILL)
		<-done
		if ctx.Err() != nil {
			return nil, fmt.Errorf("plugrun: plugin run cancelled: %w", ctx.Err())
		}
		return nil, fmt.Errorf("%w: wall clock (%s, enforced by the runner; stderr: %s)", ErrBoundExceeded, limits.Timeout, tailBytes(stderr.Bytes()))
	case waitErr = <-done:
	}
	exit, sig, ok := exitStatus(waitErr, cont.ExitCode())
	if !ok {
		return nil, fmt.Errorf("plugrun: waiting for the plugin process: %w (stderr: %s)", waitErr, tailBytes(stderr.Bytes()))
	}
	res := &Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: exit, Tier: plugexec.TierStrong, Bounds: mechanism}
	if mechanism == BoundsCgroups {
		ev, err := readCgroupEvents(cgroupDir)
		if err != nil {
			return nil, fmt.Errorf("plugrun: reading the run's cgroup events: %w", err)
		}
		if bound := ev.exceeded(limits, res.ExitCode != 0); bound != "" {
			return nil, fmt.Errorf("%w: %s (enforced by cgroups; stderr: %s)", ErrBoundExceeded, bound, tailBytes(stderr.Bytes()))
		}
	}
	if mechanism == BoundsRlimits && sig == unix.SIGKILL {
		return nil, fmt.Errorf("plugrun: plugin killed by SIGKILL: the CPU-time bound (%s over %g cores, enforced by rlimits) or an external kill — rlimits cannot attribute which (stderr: %s)", limits.Timeout, limits.CPU, tailBytes(stderr.Bytes()))
	}
	return res, nil
}

// destroy releases the container's resources, retrying the cgroup
// removal briefly: the kernel reports EBUSY on a just-emptied cgroup
// while it finishes offlining it.
func destroy(cont *container.Container) error {
	var err error
	for attempt := 0; attempt < 50; attempt++ {
		if err = cont.Destroy(); !errors.Is(err, syscall.EBUSY) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}

// cgroupResources maps the bounds onto cgroup v2 controllers: a hard
// memory limit with group OOM killing — a memory kill takes the whole
// process group, so exceeding the bound terminates the plugin rather
// than an arbitrary child (container writes memory.oom.group only
// where the kernel offers it; without it a child's kill still counts
// in the events and is attributed to the bound) — a process-count
// limit, and a CPU bandwidth quota of the permitted cores over a 100ms
// period. A quota throttles and never terminates, so CPU under
// cgroups is bounded by the wall clock's attribution.
func cgroupResources(l trust.Limits) *container.Resources {
	const period = 100000
	return &container.Resources{
		// container's field name is inverted from what it writes:
		// DisableOOMKiller sets memory.oom.group=1.
		Memory: &container.MemoryResources{Max: int64(l.Memory), DisableOOMKiller: true},
		Pids:   &container.PidsResources{Max: int64(l.Pids)},
		CPU:    &container.CPUResources{Quota: int64(math.Ceil(l.CPU * period)), Period: period},
	}
}

// rlimits maps the bounds onto POSIX limits: RLIMIT_AS for memory —
// coarser than memory.max, it caps address space, so heavy mappers may
// hit it early — RLIMIT_NPROC for processes, and RLIMIT_CPU for CPU
// time, sized as the wall clock over the permitted cores so it can
// only fire on a plugin busier than the wall clock allows. Soft and
// hard coincide: the soft limit's SIGXCPU never reaches a namespace
// init, so a gap would only grant unbounded seconds past the bound.
func rlimits(l trust.Limits) []container.Rlimit {
	cpuSeconds := uint64(math.Ceil(l.Timeout.Seconds()*l.CPU)) + 1
	return []container.Rlimit{
		{Type: unix.RLIMIT_AS, Soft: l.Memory, Hard: l.Memory},
		{Type: unix.RLIMIT_NPROC, Soft: l.Pids, Hard: l.Pids},
		{Type: unix.RLIMIT_CPU, Soft: cpuSeconds, Hard: cpuSeconds},
	}
}

// Container's Wait reports the payload's outcome two ways: the create
// path reaps the payload with wait4 and returns one of these sentences
// with the code recorded on the container, while its exec-shaped path
// returns the *exec.ExitError itself. Both forms are read so a change
// of path upstream cannot turn an outcome into a runner failure; the
// text forms are container's stated contract for the payload's status
// (a typed outcome from container would delete this parse —
// docs/issues/plugrun-container-outcome-api.md).
const (
	waitSignaled = "container killed by signal "
	waitExited   = "container exited with status "
)

// exitStatus reads a container Wait error into the payload's outcome:
// nil is exit 0; a signal death reports 128 plus the signal number and
// the signal; anything that is not a process outcome sets ok false.
// recorded is the container's own exit code, consulted for the
// text-form reports.
func exitStatus(err error, recorded int) (code int, sig syscall.Signal, ok bool) {
	if err == nil {
		return 0, 0, true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		ws, isWait := exitErr.Sys().(syscall.WaitStatus)
		if !isWait {
			return 0, 0, false
		}
		if ws.Signaled() {
			return 128 + int(ws.Signal()), ws.Signal(), true
		}
		return ws.ExitStatus(), 0, true
	}
	msg := err.Error()
	if rest, found := strings.CutPrefix(msg, waitSignaled); found {
		n, perr := strconv.Atoi(rest)
		if perr != nil {
			return 0, 0, false
		}
		return 128 + n, syscall.Signal(n), true
	}
	if rest, found := strings.CutPrefix(msg, waitExited); found {
		n, perr := strconv.Atoi(rest)
		if perr != nil || n != recorded {
			return 0, 0, false
		}
		return n, 0, true
	}
	return 0, 0, false
}

// cgroupDirOf resolves a process's cgroup v2 directory: its procfs
// entry (the "0::<path>" line) names the cgroup relative to this
// process's cgroup namespace, and the unified hierarchy's mount entry
// in mountinfo says where that namespace's view is mounted and which
// cgroup its mount root is — both expressed in the same namespace, so
// the join holds inside a container as on the host.
func cgroupDirOf(pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", err
	}
	rel, ok := cgroupV2Path(data)
	if !ok {
		return "", fmt.Errorf("process %d has no cgroup v2 entry", pid)
	}
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	mountpoint, root, ok := cgroup2Mount(mounts)
	if !ok {
		return "", errors.New("no cgroup2 mount in this mount namespace")
	}
	under, ok := strings.CutPrefix(rel, strings.TrimSuffix(root, "/"))
	if !ok {
		return "", fmt.Errorf("cgroup %s lies outside the mounted subtree %s", rel, root)
	}
	return filepath.Join(mountpoint, under), nil
}

// cgroup2Mount finds the unified hierarchy in a mountinfo listing: the
// mount point and the cgroup its root maps to (field 4 and field 5 of
// the record; the filesystem type follows the "-" separator).
func cgroup2Mount(data []byte) (mountpoint, root string, ok bool) {
	for _, line := range strings.Split(string(data), "\n") {
		pre, post, found := strings.Cut(line, " - ")
		if !found {
			continue
		}
		pf := strings.Fields(pre)
		sf := strings.Fields(post)
		if len(pf) < 5 || len(sf) < 1 || sf[0] != "cgroup2" {
			continue
		}
		return unescapeMountField(pf[4]), unescapeMountField(pf[3]), true
	}
	return "", "", false
}

// unescapeMountField decodes mountinfo's octal escapes (\040 for a
// space and the like).
func unescapeMountField(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// cgroupV2Path finds the unified-hierarchy entry in a procfs cgroup
// listing.
func cgroupV2Path(data []byte) (string, bool) {
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return rest, true
		}
	}
	return "", false
}

// cgroupEvents are the kernel's own counters of bound enforcement:
// memory.events oom_kill (processes the memory bound killed) and
// pids.events max (forks the process bound refused).
type cgroupEvents struct {
	oomKill uint64
	pidsMax uint64
}

func readCgroupEvents(dir string) (cgroupEvents, error) {
	var ev cgroupEvents
	mem, err := os.ReadFile(filepath.Join(dir, "memory.events"))
	if err != nil {
		return ev, err
	}
	pids, err := os.ReadFile(filepath.Join(dir, "pids.events"))
	if err != nil {
		return ev, err
	}
	ev.oomKill = cgroupCounter(mem, "oom_kill")
	ev.pidsMax = cgroupCounter(pids, "max")
	return ev, nil
}

// cgroupCounter reads one "key value" line of a cgroup events file;
// an absent key counts as zero.
func cgroupCounter(data []byte, key string) uint64 {
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || k != key {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// exceeded names the bound a failed run exceeded, or "" when the
// counters attribute nothing. A memory kill is a termination whatever
// the exit status; a refused fork only failed the plugin if the
// plugin then failed — a plugin that absorbed the refusal and
// responded ran within its bound.
func (ev cgroupEvents) exceeded(l trust.Limits, failed bool) string {
	if ev.oomKill > 0 {
		return fmt.Sprintf("memory (%d bytes)", l.Memory)
	}
	if ev.pidsMax > 0 && failed {
		return fmt.Sprintf("process count (%d)", l.Pids)
	}
	return ""
}
