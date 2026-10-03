package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/provenance/trust"
)

// DockerRunner runs an oci plugin in a container of a Docker daemon
// (plugin-execution.md, the runner term). pb's verified content
// reaches the daemon by one of two trust-neutral byte paths
// (REQ-plugin-core-verifies): the verified image as an archive its
// image store loads, the daemon fetching nothing, held to the
// identity the daemon reports and released with the run where the
// run introduced it, or — under the docker byte path — a pull of
// the repository at the digest the seam admitted, naming the
// manifest-list entry it admitted, the daemon holding the image as
// its own afterwards. A run killed outright — the process gone,
// not cancelled — leaves its container and, on the store path, its
// `pb-plugin-run:*` reference to the loaded image behind, as the
// daemon lists them. The container is
// created with no network, a read-only root, every capability
// dropped, no_new_privs, and the policy's bounds. The tier and the
// accounting are derived
// from the daemon's own record of the created container, read back
// before it starts, never from the flags pb passed
// (REQ-plugin-reported-tier): a record that does not show the
// boundary, or that recorded other bounds, refuses the run before it
// runs. Attribution reads the daemon after the run: a plugin that
// died by a kill is read against the daemon's event log for the
// container around its finish, an oom event there being the memory
// bound — the record's own flag is set from that same event and
// places it nowhere in time, so a kill the plugin outlived sets it
// too; a kill the daemon recorded nowhere, which it can lose under
// load, is a death the record cannot tell apart and is reported as
// such (REQ-plugin-resource-bounds: the record's own limit, no
// counter of the kernel's surviving the container's release); the
// daemon exposes
// no refused-fork counter, so a fork the process bound refused is
// not attributable here and the plugin's own failure is surfaced
// verbatim.
type DockerRunner struct {
	// CLI is the docker command, resolved on PATH; "docker" when
	// empty.
	CLI string

	platform plugin.Platform    // the daemon's
	seccomp  string             // the seccomp profile the daemon runs containers under, as it names it
	form     plugin.ArchiveForm // the form of archive the daemon's image store loads
}

// NewDockerRunner returns the runner for the daemon cli reaches, or
// why none does: the daemon's version, platform, security options
// and image store are asked for, and a refusal is the reason.
func NewDockerRunner(cli string) (*DockerRunner, error) {
	r := &DockerRunner{CLI: cli}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := r.docker(ctx, nil, "version", "--format", "{{.Server.Os}} {{.Server.Arch}}")
	if err != nil {
		return nil, err
	}
	var os_, arch string
	if _, err := fmt.Sscan(string(out), &os_, &arch); err != nil || os_ == "" || arch == "" {
		return nil, fmt.Errorf("%s version: the daemon named no platform (%q)", r.cli(), strings.TrimSpace(string(out)))
	}
	if os_ != "linux" {
		return nil, fmt.Errorf("%s version: the daemon runs %s containers; the docker runner hands images to a daemon running Linux containers (platforms.md REQ-plat-oci-substrate)", r.cli(), os_)
	}
	r.platform = plugin.Platform{OS: os_, Arch: arch}
	out, err = r.docker(ctx, nil, "info", "--format", "{{json .SecurityOptions}}")
	if err != nil {
		return nil, err
	}
	var opts []string
	if err := json.Unmarshal(out, &opts); err != nil {
		return nil, fmt.Errorf("%s info: security options unreadable: %v", r.cli(), err)
	}
	for _, o := range opts {
		if rest, ok := strings.CutPrefix(o, "name=seccomp,profile="); ok {
			r.seccomp = rest
		}
	}
	// The daemon's image store decides the form of an archive it
	// loads: the containerd store names itself in the driver's
	// status; any other is the classic store.
	out, err = r.docker(ctx, nil, "info", "--format", "{{json .DriverStatus}}")
	if err != nil {
		return nil, err
	}
	var status [][]string
	if err := json.Unmarshal(out, &status); err != nil {
		return nil, fmt.Errorf("%s info: driver status unreadable: %v", r.cli(), err)
	}
	r.form = plugin.DockerArchive
	for _, row := range status {
		if len(row) == 2 && row[0] == "driver-type" && strings.HasPrefix(row[1], "io.containerd.snapshotter.") {
			r.form = plugin.OCILayout
		}
	}
	return r, nil
}

// Platform is the daemon's: its containers run there, whatever the
// host is.
func (r *DockerRunner) Platform() plugin.Platform { return r.platform }

// RunsDaemonImages: a daemon-local image is this runner's to run.
func (r *DockerRunner) RunsDaemonImages() {}

func (r *DockerRunner) cli() string {
	if r.CLI == "" {
		return "docker"
	}
	return r.CLI
}

// docker runs one docker command with stdin and returns its stdout;
// a failure carries the command's stderr.
func (r *DockerRunner) docker(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.cli(), args...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", r.cli(), args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// dockerRecord is what the daemon records of a container, the
// fields the derivation reads.
type dockerRecord struct {
	Config struct {
		Hostname string
		Env      []string
	}
	HostConfig struct {
		NetworkMode    string
		ReadonlyRootfs bool
		Privileged     bool
		Isolation      string
		Runtime        string
		CapDrop        []string
		CapAdd         []string
		SecurityOpt    []string
		PidMode        string
		IpcMode        string
		UsernsMode     string
		UTSMode        string
		CgroupnsMode   string
		Binds          []string
		Mounts         []any
		Tmpfs          map[string]string
		Sysctls        map[string]string
		Devices        []any
		Memory         int64
		MemorySwap     int64
		PidsLimit      *int64
		Ulimits        []struct {
			Name       string
			Hard, Soft int64
		}
	}
	AppArmorProfile string
	State           struct {
		Status     string
		ExitCode   int
		Error      string
		FinishedAt string // the daemon's clock, RFC 3339 with nanoseconds
	}
}

// Run executes the plugin process (REQ-plugin-response-authority's
// transport half). The wall clock is the runner's: its end kills the
// container.
func (r *DockerRunner) Run(ctx context.Context, spec Spec) (result *Result, err error) {
	limits := spec.Limits
	if err := CheckSpec(spec); err != nil {
		return nil, err
	}
	if export, ok := spec.Image.(*plugin.Export); ok && export.Archive == nil {
		return nil, errors.New("runner: the export offers no archive for the daemon; the docker runner hands the daemon the verified image itself (REQ-plugin-core-verifies)")
	}
	if spec.Scheme == plugin.SchemeLocal {
		return nil, errors.New("runner: a local plugin is a host binary; the docker runner runs images only")
	}
	if ref, daemon := daemonImage(spec.Image); daemon {
		if _, err := name.ParseReference(ref); err != nil || strings.HasPrefix(ref, "-") {
			return nil, fmt.Errorf("runner: %q does not name a daemon image", ref)
		}
	}
	if _, err := isolationOf(spec.MinTier); err != nil {
		return nil, err
	}
	if err := plugin.CheckEnv(spec.Process.Env); err != nil {
		return nil, fmt.Errorf("runner: %v", err)
	}
	if limits.Memory > math.MaxInt64 || limits.Pids > math.MaxInt64 {
		return nil, fmt.Errorf("runner: the daemon cannot record bounds above %d", int64(math.MaxInt64))
	}

	p, err := r.prepare(ctx, spec, limits)
	defer func() {
		// The daemon holds nothing of the run afterwards — the
		// container and the anonymous volumes an image declares go
		// with it; a leak is a runner failure even after a clean run.
		// A daemon image (a daemon-local override, or one the daemon
		// pulled for this run) is the daemon's and stays.
		if p.container != "" {
			if _, rerr := r.docker(context.WithoutCancel(ctx), nil, "rm", "--force", "--volumes", p.container); rerr != nil && err == nil {
				result, err = nil, fmt.Errorf("runner: releasing the run's container: %w", rerr)
			}
		}
		if p.reference != "" {
			if _, rerr := r.docker(context.WithoutCancel(ctx), nil, "rmi", p.reference); rerr != nil && err == nil {
				result, err = nil, fmt.Errorf("runner: releasing the run's image: %w", rerr)
			}
		}
	}()
	if err != nil {
		// Whatever daemon step the caller's cancellation ended is
		// reported as the cancellation.
		return nil, beforeStart(ctx, err)
	}
	// The record's verdicts: no context ends these, so none reads as
	// a cancellation.
	container := p.container
	tier, err := deriveTier(p.record, r.seccomp)
	if err != nil {
		return nil, err
	}
	bounds, err := deriveBounds(p.record, limits)
	if err != nil {
		return nil, err
	}
	if err := deriveWorld(p.record, spec.Process.Env); err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	start := exec.CommandContext(runCtx, r.cli(), "start", "--attach", "--interactive", container)
	start.Stdin = bytes.NewReader(spec.Stdin)
	var stdout, stderr bytes.Buffer
	start.Stdout, start.Stderr = &stdout, &stderr
	// The clock's end kills the container through the daemon — the
	// whole container, not the attached client — and the attach then
	// returns.
	start.Cancel = func() error {
		_, err := r.docker(context.WithoutCancel(ctx), nil, "kill", container)
		return err
	}
	start.WaitDelay = limits.Timeout
	startErr := start.Run()
	var exit *exec.ExitError
	if startErr != nil && !errors.As(startErr, &exit) && !(runCtx.Err() != nil && errors.Is(startErr, runCtx.Err())) {
		return nil, fmt.Errorf("runner: attaching to the container: %w (stderr: %s)", startErr, tailBytes(stderr.Bytes()))
	}
	// The record is read after the run even when the caller's context
	// ended: the outcome is what says the kill landed.
	after, err := r.inspect(context.WithoutCancel(ctx), container)
	if err != nil {
		return nil, err
	}
	if after.State.Error != "" {
		// The daemon could not start the process at all: the record's
		// exit status is the daemon's, not the plugin's.
		return nil, fmt.Errorf("runner: starting the plugin process: %s (stderr: %s)", after.State.Error, tailBytes(stderr.Bytes()))
	}
	// A death by kill is read against the daemon's event log for the
	// container around its finish, before the container is released
	// (REQ-plugin-resource-bounds): the record's flag is set from that
	// same oom event but places it nowhere in time — a kill the plugin
	// outlived early in the run sets it too. The clock's kill emits no
	// oom event.
	memoryKill := false
	if after.diedByKill() {
		memoryKill, err = r.oomEvent(ctx, container, after)
		if err != nil {
			return nil, err
		}
	}
	if err := dockerOutcome(after, limits, runCtx.Err(), ctx.Err(), memoryKill); err != nil {
		return nil, fmt.Errorf("%w (stderr: %s)", err, tailBytes(stderr.Bytes()))
	}
	return &Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: after.State.ExitCode, Tier: tier, Bounds: bounds}, nil
}

// prepared is what the daemon steps before the start leave behind:
// the run's own reference to the image it loaded (none for a daemon
// image, which is the daemon's and stays), the container created,
// and its record.
type prepared struct {
	reference, container string
	record               dockerRecord
}

// prepare runs the daemon steps before the start — the load, the
// create, the record — so one failure path reports them and the run
// releases whatever they left. What the record says is judged by the
// caller: a verdict is no daemon step.
func (r *DockerRunner) prepare(ctx context.Context, spec Spec, limits trust.Limits) (p prepared, err error) {
	image, daemon := daemonImage(spec.Image)
	// The platform named to the daemon: for a pulled or a loaded
	// image the very manifest-list entry the seam admitted, variant
	// included, so the daemon pulls and runs that child and no other
	// of the verified index — its own default
	// (DOCKER_DEFAULT_PLATFORM) and its own variant matching aside;
	// the daemon's own platform where an export names no entry.
	platform := r.platform.String()
	if _, local := spec.Image.(*plugin.DaemonLocal); local {
		platform = "" // created as it is: pb selects nothing of an override
	}
	pulled, pull := spec.Image.(*plugin.Pulled)
	if pull {
		platform = pulled.Entry
		// The daemon fetches the content at the digest pb verified
		// with its own credentials; the image is then the daemon's
		// own and stays (REQ-plugin-core-verifies).
		if _, err := r.docker(ctx, nil, "pull", "--platform", platform, image); err != nil {
			return p, fmt.Errorf("runner: the daemon pulling %s: %w", image, err)
		}
	}
	if export, ok := spec.Image.(*plugin.Export); ok {
		// The verified image streams into the daemon as an archive
		// its image store loads — the image's own configuration and
		// layers, so the daemon runs it as it declares itself, the
		// modes and ownership it declares reaching the daemon byte
		// for byte on every platform — and the daemon's report of
		// what it loaded is held to the identity the archive names:
		// the image is then the daemon's, as a pulled one is, and
		// the create names it. A write failure ends the load with
		// the daemon's own report.
		if export.Entry != "" {
			platform = export.Entry
		}
		pr, pw := io.Pipe()
		type written struct {
			id  string
			err error
		}
		archived := make(chan written, 1)
		go func() {
			id, err := export.Archive(ctx, pw, r.form)
			archived <- written{id, err}
			pw.CloseWithError(err)
		}()
		// The archive's own failure ends the pipe with it, which the
		// load's stdin copy reports as the load's failure: no archive
		// fails with the load succeeding.
		loaded, err := r.docker(ctx, pr, "load", "--quiet")
		pr.Close()
		a := <-archived
		if err != nil {
			if a.err != nil {
				return p, fmt.Errorf("runner: loading the verified image into the daemon: %w (the archive: %v)", err, a.err)
			}
			return p, fmt.Errorf("runner: loading the verified image into the daemon: %w", err)
		}
		image = loadedID(string(loaded))
		if image == "" {
			return p, fmt.Errorf("runner: the daemon reported no image for the load (%q)", strings.TrimSpace(string(loaded)))
		}
		// A loaded image's ID is its content's, the same whoever
		// loaded or pulled it before, so the run owns a reference of
		// its own to it, not the image: releasing that reference
		// leaves an image anything else holds, and takes one nothing
		// did. The reference is taken before the identity is judged,
		// so what the daemon loaded is released either way; a tag
		// that fails — another run's release taking the very image
		// between this load and this tag, which refuses here rather
		// than running anything — leaves the loaded image held by
		// nothing, which the next release of it takes.
		reference := runReference()
		if _, err := r.docker(ctx, nil, "tag", image, reference); err != nil {
			return p, fmt.Errorf("runner: naming the loaded image for the run: %w", err)
		}
		p.reference = reference
		if image != a.id {
			return p, fmt.Errorf("runner: the daemon loaded %s where the verified image is %s", image, a.id)
		}
		daemon = true
	}

	memory := strconv.FormatUint(limits.Memory, 10)
	args := []string{"create", "--interactive", "--network", "none", "--read-only",
		"--hostname", pluginHostname,
		"--memory", memory, "--memory-swap", memory,
		"--pids-limit", strconv.FormatUint(limits.Pids, 10),
		"--ulimit", "cpu=" + strconv.FormatUint(cpuSeconds(limits), 10),
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges"}
	if spec.Process.WorkDir != "" {
		args = append(args, "--workdir", spec.Process.WorkDir)
	}
	for _, kv := range spec.Process.Env {
		args = append(args, "--env", kv)
	}
	if daemon {
		// A daemon image runs under its own configuration: the daemon
		// applies its entrypoint, command, environment and working
		// directory — and is the daemon's by now: a daemon-local image
		// it does not hold is never fetched (REQ-plugin-override; pb
		// verifies nothing a daemon pulls unasked), a pulled one was
		// fetched above at the verified digest, a loaded one was
		// handed to it whole. A pulled or loaded image is created
		// under the platform named, so the daemon's own default
		// (DOCKER_DEFAULT_PLATFORM) never refuses the image as
		// another platform's; a daemon-local override alone is
		// created as it is: pb selects nothing there
		// (REQ-plugin-override). A client older than 20.10 knows no
		// --pull and no --platform on create and refuses in its own
		// words.
		if platform != "" {
			args = append(args, "--platform", platform)
		}
		args = append(args, "--pull", "never", image)
	}
	out, err := r.docker(ctx, nil, args...)
	if err != nil {
		return p, fmt.Errorf("runner: creating the container: %w", err)
	}
	container := strings.TrimSpace(string(out))
	if container == "" {
		return p, errors.New("runner: the daemon reported no container for the create")
	}
	p.container = container

	p.record, err = r.inspect(ctx, container)
	return p, err
}

func (r *DockerRunner) inspect(ctx context.Context, container string) (dockerRecord, error) {
	out, err := r.docker(ctx, nil, "inspect", "--type", "container", container)
	if err != nil {
		return dockerRecord{}, fmt.Errorf("runner: reading the daemon's record of the container: %w", err)
	}
	var recs []dockerRecord
	if err := json.Unmarshal(out, &recs); err != nil || len(recs) != 1 {
		return dockerRecord{}, fmt.Errorf("runner: the daemon's record of the container is unreadable: %v", err)
	}
	return recs[0], nil
}

// The window around a container's finish, on the daemon's clock, in
// which the oom event that ended it lands. Measured on Docker 29.7.2
// (cgroup v2, the systemd driver), idle and loaded, the event
// followed the finish by three to seven milliseconds in every run
// and never preceded it; the arm before the finish admits a driver
// ordering the two the other way by a little, and no more, since
// every millisecond of it admits a kill the plugin outlived as its
// death. A kill it outlived lies further back.
const (
	oomEventBefore = 100 * time.Millisecond
	oomEventAfter  = 250 * time.Millisecond
)

// diedByKill reports the death a bound's kill is: status 137, the
// daemon's report of a SIGKILL.
func (rec dockerRecord) diedByKill() bool { return rec.State.ExitCode == 137 }

// oomEvent reads the daemon's event log for the container around its
// finish, on the daemon's own clock — the record's finish stamp, and
// the daemon's time now — so no skew between this host and the
// daemon moves the window: an oom event there is the daemon's record
// of the kill the plugin died by, the one record that places it in
// time. The log's end is the daemon's present, or
// the finish plus the event's lag where the read comes sooner, so
// the read waits that lag at most. The log is a ring the daemon
// keeps in memory, read right after the run. The read is bounded in
// its own right, the caller's cancellation notwithstanding: the
// outcome is reported even to a caller that gave up.
func (r *DockerRunner) oomEvent(ctx context.Context, container string, rec dockerRecord) (bool, error) {
	finished, err := time.Parse(time.RFC3339Nano, rec.State.FinishedAt)
	if err != nil || finished.IsZero() {
		return false, fmt.Errorf("runner: the daemon's record of the container carries no finish time (%q)", rec.State.FinishedAt)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	out, err := r.docker(ctx, nil, "info", "--format", "{{.SystemTime}}")
	if err != nil {
		return false, fmt.Errorf("runner: reading the daemon's time: %w", err)
	}
	now, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(out)))
	if err != nil {
		return false, fmt.Errorf("runner: the daemon's time is unreadable: %v", err)
	}
	until := finished.Add(oomEventAfter)
	if now.After(until) {
		until = now
	}
	out, err = r.docker(ctx, nil, "events",
		"--since", finished.Add(-oomEventBefore).UTC().Format(time.RFC3339Nano),
		"--until", until.UTC().Format(time.RFC3339Nano),
		"--filter", "container="+container, "--filter", "event=oom",
		"--format", "{{.Action}}")
	if err != nil {
		return false, fmt.Errorf("runner: reading the daemon's event log for the container: %w", err)
	}
	return slices.Contains(strings.Fields(string(out)), "oom"), nil
}

// deriveTier reads the tier off the daemon's record: Strong where the
// record shows a Linux container with no network, a read-only root,
// no privilege, every capability dropped and none added, no_new_privs,
// no host namespace shared, no device, bind, mount, tmpfs or sysctl
// pb never asked for, the daemon's built-in seccomp profile in force
// (daemonSeccomp is the profile the daemon names for its containers)
// and no security option relaxing anything, under a runtime the
// daemon names as its own or a stronger one. Anything less is not a
// tier this runner runs under — the daemon did not deliver what pb
// asked. The record is the daemon's own account, read and never
// audited (REQ-plugin-reported-tier): the native runner trusts the
// kernel's answers to its own calls the same way, and a daemon whose
// account is false is the substrate the user chose being broken, as
// a kernel's would be. Two facts of moby's account: it records the
// AppArmor profile from a probe of securityfs while applying the
// profile only where its host check also passes (the parser present,
// the kernel's parameter on, the daemon itself not contained), so a
// contained daemon records a profile it never loads; and it records
// "unconfined" only for a privileged container or one given a
// security option, both refused here on their own, so the
// unconfined rule serves a daemon other than moby.
func deriveTier(rec dockerRecord, daemonSeccomp string) (string, error) {
	hc := rec.HostConfig
	var lacks []string
	if hc.NetworkMode != "none" {
		lacks = append(lacks, fmt.Sprintf("network mode %q", hc.NetworkMode))
	}
	if !hc.ReadonlyRootfs {
		lacks = append(lacks, "a writable root")
	}
	if hc.Privileged {
		lacks = append(lacks, "privileged")
	}
	if hc.Isolation != "" && hc.Isolation != "default" {
		lacks = append(lacks, fmt.Sprintf("isolation %q", hc.Isolation))
	}
	if !slices.Contains(hc.CapDrop, "ALL") {
		lacks = append(lacks, "capabilities kept")
	}
	if len(hc.CapAdd) > 0 {
		lacks = append(lacks, fmt.Sprintf("capabilities added %v", hc.CapAdd))
	}
	if !slices.Contains(hc.SecurityOpt, "no-new-privileges") {
		lacks = append(lacks, "new privileges allowed")
	}
	for _, opt := range hc.SecurityOpt {
		if opt != "no-new-privileges" {
			lacks = append(lacks, fmt.Sprintf("security option %q", opt))
		}
	}
	// The daemon names the profile it runs containers under: its
	// built-in default is the filter this tier stands on; "unconfined"
	// is none, and a custom profile is one pb cannot judge.
	if daemonSeccomp != "builtin" && daemonSeccomp != "default" {
		lacks = append(lacks, fmt.Sprintf("daemon seccomp profile %q", daemonSeccomp))
	}
	if rec.AppArmorProfile == "unconfined" {
		lacks = append(lacks, "apparmor unconfined")
	}
	for _, ns := range []struct{ name, mode string }{{"pid", hc.PidMode}, {"ipc", hc.IpcMode}, {"user", hc.UsernsMode}, {"uts", hc.UTSMode}} {
		if ns.mode != "" && ns.mode != "private" {
			lacks = append(lacks, fmt.Sprintf("%s namespace %q", ns.name, ns.mode))
		}
	}
	if hc.CgroupnsMode == "host" {
		lacks = append(lacks, "the host's cgroup namespace")
	}
	if len(hc.Devices) > 0 || len(hc.Binds) > 0 || len(hc.Mounts) > 0 || len(hc.Tmpfs) > 0 || len(hc.Sysctls) > 0 {
		lacks = append(lacks, "devices, mounts or sysctls pb never asked for")
	}
	switch hc.Runtime {
	case "", "runc", "crun", "io.containerd.runc.v2", "runsc", "kata", "kata-runtime", "youki":
	default:
		lacks = append(lacks, fmt.Sprintf("runtime %q", hc.Runtime))
	}
	if len(lacks) > 0 {
		return "", fmt.Errorf("runner: the daemon's record of the container does not show the sandbox boundary (%s); refusing to run", strings.Join(lacks, ", "))
	}
	return plugin.TierStrong, nil
}

// deriveWorld checks the record's world against the intent whose
// delivery the tier does not grade: the fixed hostname and exactly
// the image's environment (REQ-plugin-runner-independence).
func deriveWorld(rec dockerRecord, env []string) error {
	if rec.Config.Hostname != pluginHostname {
		return fmt.Errorf("runner: the daemon's record of the container names hostname %q, not %q; refusing to run", rec.Config.Hostname, pluginHostname)
	}
	for _, kv := range env {
		if !slices.Contains(rec.Config.Env, kv) {
			return fmt.Errorf("runner: the daemon's record of the container lacks the image's environment entry %q; refusing to run", kv)
		}
	}
	return nil
}

// deriveBounds reads the accounting off the daemon's record: the
// memory and process limits are the container cgroup's, and must be
// the policy's exactly; the CPU-time limit is an rlimit in the
// container.
func deriveBounds(rec dockerRecord, l trust.Limits) (Accounting, error) {
	hc := rec.HostConfig
	var wrong []string
	if hc.Memory != int64(l.Memory) || hc.MemorySwap != int64(l.Memory) {
		wrong = append(wrong, fmt.Sprintf("memory %d with swap to %d", hc.Memory, hc.MemorySwap))
	}
	if hc.PidsLimit == nil || *hc.PidsLimit != int64(l.Pids) {
		wrong = append(wrong, "process count")
	}
	cpu := false
	for _, u := range hc.Ulimits {
		if u.Name == "cpu" && u.Hard == int64(cpuSeconds(l)) && u.Soft == u.Hard {
			cpu = true
		}
	}
	if !cpu {
		wrong = append(wrong, "CPU time")
	}
	if len(wrong) > 0 {
		return "", fmt.Errorf("runner: the daemon's record of the container does not carry the policy's bounds (%s); refusing to run", strings.Join(wrong, ", "))
	}
	return BoundsCgroups, nil
}

// dockerOutcome reads a finished container into its report, in the
// order the facts bind: the memory kill the daemon's event log
// places at the plugin's death (memoryKill, read for a death by kill
// alone — a kill the plugin outlived terminated nothing of it, and
// its response, or its own failure, stands); then a
// container that never ran to an exit — the run context ended before
// or during the start, the caller's cancellation or the wall clock,
// or else a daemon fault; then a status of 137, which the record
// cannot tell apart between the CPU-time bound, an external kill and
// the plugin's own exit 137; and nothing otherwise.
func dockerOutcome(rec dockerRecord, l trust.Limits, clock, parent error, memoryKill bool) error {
	if memoryKill {
		return fmt.Errorf("%w: memory (%d bytes) (enforced by %s)", ErrBoundExceeded, l.Memory, BoundsCgroups)
	}
	ended := clock != nil && (rec.State.ExitCode == 137 || rec.State.Status != "exited")
	if ended {
		if parent != nil {
			return fmt.Errorf("runner: plugin run cancelled: %w", parent)
		}
		return fmt.Errorf("%w: wall clock (%s, enforced by the runner)", ErrBoundExceeded, l.Timeout)
	}
	if rec.State.Status != "exited" {
		return fmt.Errorf("runner: the container did not run to an exit (status %q)", rec.State.Status)
	}
	if rec.State.ExitCode == 137 {
		return fmt.Errorf("runner: plugin ended with status 137: the CPU-time bound (%s over %g cores, enforced by rlimits), an external kill, or the plugin's own exit 137 — the daemon's record cannot tell them apart", l.Timeout, l.CPU)
	}
	return nil
}

// loadedID reads the daemon's report of a load: the image's ID,
// `Loaded image ID: sha256:...`, as the daemon prints it for an
// archive naming no tag.
func loadedID(report string) string {
	for _, line := range strings.Split(report, "\n") {
		if id, ok := strings.CutPrefix(strings.TrimSpace(line), "Loaded image ID: "); ok {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

// runReference is the reference a run gives the image it loaded:
// a repository of pb's own under a random tag, so two runs never
// share one and a user's own references are never touched.
func runReference() string {
	var b [8]byte
	rand.Read(b[:])
	return "pb-plugin-run:" + hex.EncodeToString(b[:])
}
