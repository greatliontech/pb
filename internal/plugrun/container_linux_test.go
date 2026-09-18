//go:build linux

package plugrun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/greatliontech/container"
	"github.com/greatliontech/pb/internal/plugexec"
	"github.com/greatliontech/pb/internal/trust"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

var (
	rootfsDir          string
	sandboxUnavailable string // non-empty when the host cannot create the sandbox at all
)

// TestMain builds the fake plugin statically into a bare rootfs once
// and probes the host's sandbox capability through container directly
// — not through ContainerRunner — so an unavailable kernel feature
// skips the suite while a runner regression fails it.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pb-rootfs-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "plugin"), "testdata/fakeplugin.go")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building the fake plugin: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	rootfsDir = dir
	sandboxUnavailable = probeSandbox()
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// probeSandbox runs the plugin once under container's default create
// path with the rootfs bind-mounted: namespaces, pivot, and exec
// working is the capability the suite needs.
func probeSandbox() string {
	target, err := os.MkdirTemp("", "pb-probe-*")
	if err != nil {
		return err.Error()
	}
	defer os.RemoveAll(target)
	cfg := container.DefaultConfig()
	cfg.Root = target
	cfg.SetupDev = false
	cfg.Devices = nil
	cfg.MaskPaths = nil
	cfg.ReadonlyPaths = nil
	cfg.Mounts = []container.Mount{{Source: rootfsDir, Target: target, Type: "none", Flags: syscall.MS_BIND}}
	cont := container.New(fmt.Sprintf("pb-probe-%d", os.Getpid()), cfg)
	req, _ := proto.Marshal(&pluginpb.CodeGeneratorRequest{})
	var stderr strings.Builder
	if err := cont.Run(&container.Process{Cmd: "/plugin", Stdin: strings.NewReader(string(req)), Stdout: &strings.Builder{}, Stderr: &stderr}); err != nil {
		return fmt.Sprintf("%v (stderr: %s)", err, stderr.String())
	}
	defer cont.Destroy()
	if err := cont.Wait(); err != nil {
		return fmt.Sprintf("%v (stderr: %s)", err, stderr.String())
	}
	return ""
}

func requireSandbox(t *testing.T) {
	t.Helper()
	if sandboxUnavailable != "" {
		t.Skipf("sandbox unavailable on this host: %s", sandboxUnavailable)
	}
}

// mechanisms lists the bound mechanisms this host can exercise: rlimits
// always, cgroups where placement is available. The cap is logged,
// and fails the test where PB_TEST_REQUIRE_CGROUPS demands the
// coverage (docs/issues/plugrun-cgroups-live-coverage.md).
func mechanisms(t *testing.T) []string {
	t.Helper()
	ms := []string{BoundsRlimits}
	if container.CgroupsAvailable() {
		return append(ms, BoundsCgroups)
	}
	if os.Getenv("PB_TEST_REQUIRE_CGROUPS") != "" {
		t.Fatal("PB_TEST_REQUIRE_CGROUPS is set and cgroup placement is unavailable here")
	}
	t.Log("cgroup placement unavailable here: the cgroups mechanism is not exercised")
	return ms
}

func runnerFor(mechanism string) *ContainerRunner {
	return &ContainerRunner{cgroupsAvailable: func() bool { return mechanism == BoundsCgroups }}
}

func request(t *testing.T, param string) []byte {
	t.Helper()
	b, err := proto.Marshal(&pluginpb.CodeGeneratorRequest{FileToGenerate: []string{"a.proto", "b.proto"}, Parameter: &param})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func limits(mod func(*trust.Limits)) trust.Limits {
	l := (&trust.Execution{}).EffectiveLimits()
	if mod != nil {
		mod(&l)
	}
	return l
}

func run(t *testing.T, r *ContainerRunner, param string, l trust.Limits) (*Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return r.Run(ctx, Spec{
		Rootfs:  rootfsDir,
		Process: plugexec.Process{Argv: []string{"/plugin"}},
		Stdin:   request(t, param),
		Limits:  l,
	})
}

func content(t *testing.T, res *Result) string {
	t.Helper()
	resp, err := Respond(res)
	if err != nil {
		t.Fatal(err)
	}
	return resp.GetFile()[0].GetContent()
}

// The happy path under each mechanism: the request reaches stdin, the
// response comes back on stdout, and the run reports Strong and the
// mechanism actually configured (REQ-plugin-sandboxed,
// REQ-plugin-reported-tier, REQ-plugin-resource-bounds).
func TestRunHappyPath(t *testing.T) {
	requireSandbox(t)
	for _, m := range mechanisms(t) {
		t.Run(m, func(t *testing.T) {
			res, err := run(t, runnerFor(m), "", limits(nil))
			if err != nil {
				t.Fatal(err)
			}
			if res.Tier != plugexec.TierStrong || res.Bounds != m || res.ExitCode != 0 {
				t.Fatalf("result = tier %s, bounds %s, exit %d", res.Tier, res.Bounds, res.ExitCode)
			}
			if got := content(t, res); got != "files=2" {
				t.Fatalf("plugin saw %q", got)
			}
		})
	}
}

// The native runner is the container runner on Linux, deciding its
// mechanism by container's own probe.
func TestNativeRunnerIsContainer(t *testing.T) {
	r, err := NativeRunner()
	if err != nil {
		t.Fatal(err)
	}
	cr, ok := r.(*ContainerRunner)
	if !ok || cr.cgroupsAvailable != nil {
		t.Fatalf("native runner = %T (%+v)", r, r)
	}
	if cr.cgroups() != container.CgroupsAvailable() {
		t.Fatal("mechanism choice diverges from container's probe")
	}
}

// The pivoted root is read-only: a plugin writing anywhere in it
// fails (REQ-plugin-sandboxed).
func TestRunReadonlyRoot(t *testing.T) {
	requireSandbox(t)
	res, err := run(t, runnerFor(BoundsRlimits), "write", limits(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := content(t, res); got != "write-err=true" {
		t.Fatalf("root writable from the sandbox: %q", got)
	}
}

// The plugin has no network: a fresh namespace holding only a
// loopback, so the assertion holds on an offline host too
// (REQ-plugin-sandboxed).
func TestRunNoNetwork(t *testing.T) {
	requireSandbox(t)
	res, err := run(t, runnerFor(BoundsRlimits), "net", limits(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := content(t, res); got != "ifaces=lo dial-err=true" {
		t.Fatalf("network reachable from the sandbox: %q", got)
	}
}

// A non-zero exit surfaces as the plugin's failure with stderr
// attached (REQ-plugin-response-authority's transport half).
func TestRunExitCode(t *testing.T) {
	requireSandbox(t)
	res, err := run(t, runnerFor(BoundsRlimits), "exit7", limits(nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 || !strings.Contains(string(res.Stderr), "deliberate failure") {
		t.Fatalf("exit=%d stderr=%s", res.ExitCode, res.Stderr)
	}
	if _, err := Respond(res); err == nil || !strings.Contains(err.Error(), "exited 7") {
		t.Fatalf("Respond: %v", err)
	}
}

// The wall-clock bound kills a hung plugin naming the bound and its
// enforcement (REQ-plugin-resource-bounds).
func TestRunWallClock(t *testing.T) {
	requireSandbox(t)
	for _, m := range mechanisms(t) {
		t.Run(m, func(t *testing.T) {
			start := time.Now()
			_, err := run(t, runnerFor(m), "sleep", limits(func(l *trust.Limits) { l.Timeout = 2 * time.Second }))
			if !errors.Is(err, ErrBoundExceeded) || !strings.Contains(err.Error(), "wall clock (2s") {
				t.Fatalf("err = %v", err)
			}
			if time.Since(start) > 30*time.Second {
				t.Fatal("the kill did not reap the plugin promptly")
			}
		})
	}
}

// A cancelled parent context stops the run and is reported as a
// cancellation, never as the wall-clock bound.
func TestRunCancelled(t *testing.T) {
	requireSandbox(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	_, err := runnerFor(BoundsRlimits).Run(ctx, Spec{
		Rootfs:  rootfsDir,
		Process: plugexec.Process{Argv: []string{"/plugin"}},
		Stdin:   request(t, "sleep"),
		Limits:  limits(nil),
	})
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrBoundExceeded) || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v", err)
	}
}

// The memory bound stops a runaway allocator — 16GiB attempted under a
// 64MiB cap (REQ-plugin-resource-bounds). Under cgroups the kernel
// kills the group and the run names the bound; under rlimits the
// allocation is refused and the plugin fails on its own terms.
func TestRunMemoryBound(t *testing.T) {
	requireSandbox(t)
	for _, m := range mechanisms(t) {
		t.Run(m, func(t *testing.T) {
			res, err := run(t, runnerFor(m), "hog", limits(func(l *trust.Limits) { l.Memory = 64 << 20; l.Timeout = time.Minute }))
			switch m {
			case BoundsCgroups:
				if !errors.Is(err, ErrBoundExceeded) || !strings.Contains(err.Error(), "memory (67108864 bytes)") {
					t.Fatalf("err = %v", err)
				}
			case BoundsRlimits:
				if err != nil {
					t.Fatal(err)
				}
				if res.ExitCode == 0 {
					t.Fatalf("hog survived the memory bound: %s", res.Stdout)
				}
			}
		})
	}
}

// Under rlimits a plugin exhausting its CPU time is killed unlabeled
// at the hard limit, and the run reports the two possible causes
// rather than claiming one (REQ-plugin-resource-bounds).
func TestRunCPUBoundRlimits(t *testing.T) {
	requireSandbox(t)
	if runtime.NumCPU() < 2 {
		t.Skip("CPU time must outrun the wall clock: two cores at least")
	}
	// CPU 1 over 10s allows 11 CPU-seconds; spinning every core burns
	// them well inside the wall clock.
	start := time.Now()
	_, err := run(t, runnerFor(BoundsRlimits), "spin", limits(func(l *trust.Limits) { l.CPU = 1; l.Timeout = 10 * time.Second }))
	if err == nil || errors.Is(err, ErrBoundExceeded) || !strings.Contains(err.Error(), "killed by SIGKILL: the CPU-time bound (10s over 1 cores, enforced by rlimits) or an external kill") {
		t.Fatalf("hard-limit death: %v", err)
	}
	if time.Since(start) >= 10*time.Second {
		// The message above already excludes the wall clock; reaching
		// it after ten seconds means the host could not spare the CPU
		// to make CPU time outrun the clock — a harness cap, stated.
		t.Skip("the host could not sustain the CPU throughput this case needs")
	}
}

// The cgroup2 mount is read from mountinfo: mount point and root, with
// octal escapes decoded; the run's cgroup joins under the mount point
// relative to that root.
func TestCgroup2Mount(t *testing.T) {
	info := []byte(strings.Join([]string{
		"22 1 0:21 / /proc rw,nosuid - proc proc rw",
		"45 43 0:29 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime shared:7 - cgroup2 cgroup2 rw,nsdelegate",
		"90 43 0:30 / /sys/fs/cgroup/unified rw - cgroup cgroup rw,name=systemd",
	}, "\n"))
	mp, root, ok := cgroup2Mount(info)
	if !ok || mp != "/sys/fs/cgroup" || root != "/" {
		t.Fatalf("mount = %q %q %v", mp, root, ok)
	}
	nested := []byte("50 40 0:29 /user.slice/app.scope /mnt/cg\\040v2 rw - cgroup2 cgroup2 rw\n")
	mp, root, ok = cgroup2Mount(nested)
	if !ok || mp != "/mnt/cg v2" || root != "/user.slice/app.scope" {
		t.Fatalf("nested mount = %q %q %v", mp, root, ok)
	}
	if _, _, ok := cgroup2Mount([]byte("22 1 0:21 / /proc rw - proc proc rw\n")); ok {
		t.Fatal("a listing without cgroup2 yielded a mount")
	}
}

// An unbounded Spec never starts a process.
func TestRunRefusesUnbounded(t *testing.T) {
	_, err := runnerFor(BoundsRlimits).Run(context.Background(), Spec{Rootfs: "/nonexistent", Process: plugexec.Process{Argv: []string{"/plugin"}}})
	if err == nil || !strings.Contains(err.Error(), "unbounded") {
		t.Fatalf("err = %v", err)
	}
	_, err = runnerFor(BoundsRlimits).Run(context.Background(), Spec{Rootfs: "/nonexistent", Limits: limits(nil)})
	if err == nil || !strings.Contains(err.Error(), "no argv") {
		t.Fatalf("err = %v", err)
	}
}

// The cgroup mapping: memory.max with group OOM, pids.max, and a CPU
// quota of the permitted cores per period.
func TestCgroupResources(t *testing.T) {
	r := cgroupResources(trust.Limits{Memory: 1 << 20, CPU: 1.5, Pids: 7, Timeout: time.Second})
	if r.Memory.Max != 1<<20 || !r.Memory.DisableOOMKiller {
		t.Fatalf("memory = %+v", r.Memory)
	}
	if r.Pids.Max != 7 {
		t.Fatalf("pids = %+v", r.Pids)
	}
	if r.CPU.Quota != 150000 || r.CPU.Period != 100000 {
		t.Fatalf("cpu = %+v", r.CPU)
	}
}

// The rlimit mapping: address space and process count exactly, CPU
// seconds as the wall clock over the cores with the soft limit one
// second under the hard one.
func TestRlimits(t *testing.T) {
	rl := rlimits(trust.Limits{Memory: 1 << 20, CPU: 2, Pids: 7, Timeout: 90 * time.Second})
	want := []container.Rlimit{
		{Type: unix.RLIMIT_AS, Soft: 1 << 20, Hard: 1 << 20},
		{Type: unix.RLIMIT_NPROC, Soft: 7, Hard: 7},
		{Type: unix.RLIMIT_CPU, Soft: 181, Hard: 181},
	}
	if len(rl) != len(want) {
		t.Fatalf("rlimits = %+v", rl)
	}
	for i := range want {
		if rl[i] != want[i] {
			t.Fatalf("rlimit %d = %+v, want %+v", i, rl[i], want[i])
		}
	}
}

// exitStatus reads both of container's outcome forms: real wait
// statuses (a clean exit, an exit code, a signal death as 128 plus
// the signal) and container's text reports with the recorded code;
// anything else is not a process outcome.
func TestExitStatus(t *testing.T) {
	if code, sig, ok := exitStatus(nil, 0); code != 0 || sig != 0 || !ok {
		t.Fatalf("nil = %d %v %v", code, sig, ok)
	}
	if _, _, ok := exitStatus(errors.New("container not started"), 0); ok {
		t.Fatal("a non-process error read as a status")
	}
	err := exec.Command("sh", "-c", "exit 7").Run()
	if code, sig, ok := exitStatus(err, 7); code != 7 || sig != 0 || !ok {
		t.Fatalf("exit 7 = %d %v %v", code, sig, ok)
	}
	err = exec.Command("sh", "-c", "kill -TERM $$").Run()
	if code, sig, ok := exitStatus(err, 143); code != 128+int(syscall.SIGTERM) || sig != syscall.SIGTERM || !ok {
		t.Fatalf("SIGTERM = %d %v %v", code, sig, ok)
	}
	if code, sig, ok := exitStatus(errors.New(waitExited+"7"), 7); code != 7 || sig != 0 || !ok {
		t.Fatalf("text exit 7 = %d %v %v", code, sig, ok)
	}
	if _, _, ok := exitStatus(errors.New(waitExited+"7"), 9); ok {
		t.Fatal("a text report disagreeing with the recorded code read as a status")
	}
	if _, _, ok := exitStatus(errors.New(waitExited+"x"), 0); ok {
		t.Fatal("garbage status read as a status")
	}
	if code, sig, ok := exitStatus(errors.New(waitSignaled+"24"), 152); code != 152 || sig != unix.SIGXCPU || !ok {
		t.Fatalf("text SIGXCPU = %d %v %v", code, sig, ok)
	}
}

// The event counters attribute a failure: a memory kill always, a
// refused fork only when the plugin then failed.
func TestCgroupEventsExceeded(t *testing.T) {
	l := trust.Limits{Memory: 4096, Pids: 3}
	cases := []struct {
		ev     cgroupEvents
		failed bool
		want   string
	}{
		{cgroupEvents{}, true, ""},
		{cgroupEvents{oomKill: 1}, false, "memory (4096 bytes)"},
		{cgroupEvents{oomKill: 2}, true, "memory (4096 bytes)"},
		{cgroupEvents{pidsMax: 1}, false, ""},
		{cgroupEvents{pidsMax: 1}, true, "process count (3)"},
		{cgroupEvents{oomKill: 1, pidsMax: 1}, true, "memory (4096 bytes)"},
	}
	for _, c := range cases {
		if got := c.ev.exceeded(l, c.failed); got != c.want {
			t.Errorf("%+v failed=%v: %q, want %q", c.ev, c.failed, got, c.want)
		}
	}
	mem := []byte("low 0\nhigh 0\nmax 12\noom 3\noom_kill 2\noom_group_kill 1\n")
	if got := cgroupCounter(mem, "oom_kill"); got != 2 {
		t.Fatalf("oom_kill = %d", got)
	}
	if got := cgroupCounter(mem, "max"); got != 12 {
		t.Fatalf("max = %d", got)
	}
	if got := cgroupCounter(mem, "absent"); got != 0 {
		t.Fatalf("absent = %d", got)
	}
	if got := cgroupCounter([]byte("oom_kill x\n"), "oom_kill"); got != 0 {
		t.Fatalf("garbage = %d", got)
	}
}

// The procfs cgroup listing yields the unified-hierarchy path; the
// running test process is its own anchor on a cgroup v2 host.
func TestCgroupDirOf(t *testing.T) {
	if p, ok := cgroupV2Path([]byte("1:name=systemd:/x\n0::/user.slice/app.scope\n")); !ok || p != "/user.slice/app.scope" {
		t.Fatalf("v2 path = %q %v", p, ok)
	}
	if _, ok := cgroupV2Path([]byte("1:name=systemd:/x\n")); ok {
		t.Fatal("v1-only listing yielded a path")
	}
	self, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Skip(err)
	}
	if _, ok := cgroupV2Path(self); !ok {
		t.Skip("no cgroup v2 entry for this process")
	}
	dir, err := cgroupDirOf(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("resolved cgroup dir %s: %v", dir, err)
	}
	if _, err := cgroupDirOf(1 << 30); err == nil {
		t.Fatal("nonexistent pid resolved")
	}
}
