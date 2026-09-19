//go:build linux

package plugrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/greatliontech/pb/internal/plugexec"
	"github.com/greatliontech/pb/internal/trust"
	"github.com/greatliontech/sandbox"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

var (
	sandboxUnavailable string // non-empty when the host reaches no Strong row
	harnessErr         error  // a probe failure that is not the host's tier: the live tests fail on it
)

// setupSandbox probes the host through sandbox directly — not
// through SandboxRunner — so a host below the Strong row skips the
// live tests, a broken harness fails them, and the pure tests run
// either way.
func setupSandbox(m *testing.M) int {
	if rootfsErr != nil {
		harnessErr = rootfsErr
	} else {
		sandboxUnavailable, harnessErr = probeSandbox()
	}
	return m.Run()
}

// probeSandbox runs the plugin once on the Strong row with the rootfs
// as its Root: a host that reaches no Strong row is the one condition
// the live tests skip for; any other failure is a broken harness.
func probeSandbox() (unavailable string, err error) {
	req, _ := proto.Marshal(&pluginpb.CodeGeneratorRequest{})
	var stderr strings.Builder
	sb, err := sandbox.New(sandbox.Spec{
		Exec:    "/plugin",
		Root:    rootfsDir,
		Limits:  sandbox.Limits{CPUSeconds: 60},
		MinTier: sandbox.Strong,
		Stdin:   strings.NewReader(string(req)),
		Stdout:  &strings.Builder{},
		Stderr:  &stderr,
	})
	if err != nil {
		return "", fmt.Errorf("sandbox probe: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := sb.Start(ctx); err != nil {
		if errors.Is(err, sandbox.ErrWeakerThanRequired) {
			return err.Error(), nil
		}
		return "", fmt.Errorf("sandbox probe failed for a reason other than the tier: %v (stderr: %s)", err, stderr.String())
	}
	if es, err := sb.Wait(); err != nil || es.Code != 0 {
		return "", fmt.Errorf("sandbox probe: %+v %v (stderr: %s)", es, err, stderr.String())
	}
	return "", nil
}

func requireSandbox(t *testing.T) {
	t.Helper()
	if harnessErr != nil {
		t.Fatal(harnessErr)
	}
	if sandboxUnavailable != "" {
		t.Skipf("the Strong row is unavailable on this host: %s", sandboxUnavailable)
	}
}



func run(t *testing.T, param string, l trust.Limits) (*Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return (&SandboxRunner{}).Run(ctx, Spec{
		Rootfs:  rootfsDir,
		Process: plugexec.Process{Argv: []string{"/plugin"}},
		Stdin:   request(t, ""),
		Limits:  l,
		MinTier: plugexec.TierStrong,
	}.withParam(t, param))
}

// withParam swaps the request for one carrying param.
func (s Spec) withParam(t *testing.T, param string) Spec {
	t.Helper()
	s.Stdin = request(t, param)
	return s
}


// accounting reports which bounds accounting this host affords the
// runner — the sandbox's choice, not the runner's — and fails where
// PB_TEST_REQUIRE_CGROUPS demands cgroups the host does not give
// (docs/issues/plugrun-cgroups-live-coverage.md).
func accounting(t *testing.T) Accounting {
	t.Helper()
	requireSandbox(t)
	res, err := run(t, "", limits(nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Bounds != BoundsCgroups && os.Getenv("PB_TEST_REQUIRE_CGROUPS") != "" {
		t.Fatalf("PB_TEST_REQUIRE_CGROUPS is set and the run was accounted by %s", res.Bounds)
	}
	if res.Bounds != BoundsCgroups {
		t.Logf("cgroup placement unavailable here: the cgroups accounting is not exercised")
	}
	return res.Bounds
}

// The happy path: the request reaches stdin, the response comes back
// on stdout, and the run reports Strong and the accounting the
// sandbox actually chose (REQ-plugin-sandboxed,
// REQ-plugin-reported-tier, REQ-plugin-resource-bounds).
func TestRunHappyPath(t *testing.T) {
	requireSandbox(t)
	res, err := run(t, "", limits(nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != plugexec.TierStrong || res.ExitCode != 0 {
		t.Fatalf("result = tier %s, exit %d", res.Tier, res.ExitCode)
	}
	if res.Bounds != BoundsCgroups && res.Bounds != BoundsRlimits {
		t.Fatalf("bounds = %q", res.Bounds)
	}
	if got := content(t, res); got != "files=2" {
		t.Fatalf("plugin saw %q", got)
	}
}

// The native runner is the sandbox runner on Linux.
func TestNativeRunnerIsSandbox(t *testing.T) {
	r, err := NativeRunner()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*SandboxRunner); !ok {
		t.Fatalf("native runner = %T", r)
	}
}

// The root is read-only: a plugin writing anywhere in it fails
// (REQ-plugin-sandboxed).
func TestRunReadonlyRoot(t *testing.T) {
	requireSandbox(t)
	res, err := run(t, "write", limits(nil))
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
	res, err := run(t, "net", limits(nil))
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
	res, err := run(t, "exit7", limits(nil))
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
	start := time.Now()
	_, err := run(t, "sleep", limits(func(l *trust.Limits) { l.Timeout = 2 * time.Second }))
	if !errors.Is(err, ErrBoundExceeded) || !strings.Contains(err.Error(), "wall clock (2s") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("the kill did not reap the plugin promptly")
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
	_, err := (&SandboxRunner{}).Run(ctx, Spec{
		Rootfs:  rootfsDir,
		Process: plugexec.Process{Argv: []string{"/plugin"}},
		Stdin:   request(t, "sleep"),
		Limits:  limits(nil),
		MinTier: plugexec.TierStrong,
	})
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrBoundExceeded) || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v", err)
	}
}

// The memory bound stops a runaway allocator — 16GiB attempted under
// a 1GiB cap (REQ-plugin-resource-bounds). Under cgroups the kernel
// kills the group and the run names the bound; under rlimits the
// allocation is refused and the plugin fails on its own terms — the
// runtime's own out-of-memory death, so the plugin ran and was
// refused rather than never starting.
func TestRunMemoryBound(t *testing.T) {
	acc := accounting(t)
	res, err := run(t, "hog", limits(func(l *trust.Limits) { l.Memory = 1 << 30; l.Timeout = time.Minute }))
	switch acc {
	case BoundsCgroups:
		if !errors.Is(err, ErrBoundExceeded) || !strings.Contains(err.Error(), "memory (1073741824 bytes) (enforced by cgroups") {
			t.Fatalf("err = %v", err)
		}
	case BoundsRlimits:
		if err != nil {
			t.Fatal(err)
		}
		if res.ExitCode == 0 || !strings.Contains(string(res.Stderr), "out of memory") {
			t.Fatalf("hog under the memory bound: exit %d, stderr %q", res.ExitCode, tailBytes(res.Stderr))
		}
	}
}

// A plugin exhausting its CPU time is killed unlabeled at the hard
// limit — RLIMIT_CPU under every accounting — and the run reports
// the two possible causes rather than claiming one
// (REQ-plugin-resource-bounds).
func TestRunCPUBound(t *testing.T) {
	requireSandbox(t)
	if runtime.NumCPU() < 2 {
		t.Skip("CPU time must outrun the wall clock: two cores at least")
	}
	// CPU 1 over 10s allows 11 CPU-seconds; spinning every core burns
	// them well inside the wall clock.
	start := time.Now()
	_, err := run(t, "spin", limits(func(l *trust.Limits) { l.CPU = 1; l.Timeout = 10 * time.Second }))
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

// An unbounded Spec, one without argv, or one without a tier floor
// never starts a process.
func TestRunRefusesIncomplete(t *testing.T) {
	r := &SandboxRunner{}
	_, err := r.Run(context.Background(), Spec{Rootfs: "/nonexistent", Process: plugexec.Process{Argv: []string{"/plugin"}}, MinTier: plugexec.TierStrong})
	if err == nil || !strings.Contains(err.Error(), "unbounded") {
		t.Fatalf("err = %v", err)
	}
	_, err = r.Run(context.Background(), Spec{Rootfs: "/nonexistent", Limits: limits(nil), MinTier: plugexec.TierStrong})
	if err == nil || !strings.Contains(err.Error(), "no argv") {
		t.Fatalf("err = %v", err)
	}
	_, err = r.Run(context.Background(), Spec{Rootfs: "/nonexistent", Process: plugexec.Process{Argv: []string{"/plugin"}}, Limits: limits(nil)})
	if err == nil || !strings.Contains(err.Error(), "no sandbox tier floor") {
		t.Fatalf("err = %v", err)
	}
}

// A host below the floor is reported as ErrTierUnreachable carrying
// the sandbox's statement; any other Start failure is a start failure
// with the stderr so far.
func TestStartError(t *testing.T) {
	err := startError(fmt.Errorf("%w: this host reaches the minimal row (namespaces: EPERM); strong required", sandbox.ErrWeakerThanRequired), nil)
	if !errors.Is(err, ErrTierUnreachable) || !strings.Contains(err.Error(), "reaches the minimal row") {
		t.Fatalf("tier refusal: %v", err)
	}
	err = startError(fmt.Errorf("%w: the minimal row cannot restrict the world to a Root", sandbox.ErrUndeliverable), nil)
	if errors.Is(err, ErrTierUnreachable) || !strings.Contains(err.Error(), "lowering the tier floor cannot help") || !strings.Contains(err.Error(), "cannot restrict the world") {
		t.Fatalf("undeliverable intent: %v", err)
	}
	err = startError(errors.New("sandbox: start: fork/exec: too many open files"), []byte("boom"))
	if errors.Is(err, ErrTierUnreachable) || !strings.Contains(err.Error(), "starting the plugin process") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("start failure: %v", err)
	}
}

// The whole of pb's intent for a run is the export as the Root, the
// image's process, no network, a fixed hostname, the policy's limits
// and floor, and the request on stdin — nothing else, nothing of the
// host (REQ-plugin-sandboxed, REQ-plugin-runner-independence,
// REQ-plugin-min-tier).
func TestSpecOf(t *testing.T) {
	var out, errs strings.Builder
	got := specOf(Spec{
		Rootfs:  "/export",
		Process: plugexec.Process{Argv: []string{"/bin/plugin", "--x"}, Env: []string{"A=1"}, WorkDir: "/w"},
		Stdin:   []byte("req"),
		Limits:  trust.Limits{Memory: 1 << 20, CPU: 2, Pids: 7, Timeout: 90 * time.Second},
	}, sandbox.OS, &out, &errs)
	stdin, _ := io.ReadAll(got.Stdin)
	got.Stdin = nil
	want := sandbox.Spec{
		Exec: "/bin/plugin", Args: []string{"--x"}, Env: []string{"A=1"}, WorkDir: "/w", Root: "/export",
		Network: false, Hostname: "pb-plugin",
		Limits:  sandbox.Limits{MemoryBytes: 1 << 20, CPUSeconds: 181, MaxProcs: 7},
		MinTier: sandbox.OS, Stdout: &out, Stderr: &errs,
	}
	if string(stdin) != "req" || !reflect.DeepEqual(got, want) {
		t.Fatalf("specOf = %+v (stdin %q), want %+v", got, stdin, want)
	}
}

// The tier vocabularies map both ways, and an absent or unknown floor
// is refused.
func TestTierMapping(t *testing.T) {
	for _, c := range []struct {
		iso  sandbox.Isolation
		tier string
	}{{sandbox.None, plugexec.TierNone}, {sandbox.Minimal, plugexec.TierMinimal}, {sandbox.OS, plugexec.TierOS}, {sandbox.Strong, plugexec.TierStrong}} {
		if got, ok := tierOf(c.iso); !ok || got != c.tier {
			t.Errorf("tierOf(%v) = %q %v", c.iso, got, ok)
		}
		if got, err := isolationOf(c.tier); err != nil || got != c.iso {
			t.Errorf("isolationOf(%q) = %v %v", c.tier, got, err)
		}
	}
	if _, ok := tierOf(sandbox.Isolation(99)); ok {
		t.Error("an unknown isolation mapped to a tier")
	}
	for _, bad := range []string{"", "VM", "strong"} {
		if _, err := isolationOf(bad); err == nil {
			t.Errorf("isolationOf(%q) accepted", bad)
		}
	}
	if accountingOf(sandbox.AccountingCgroups) != BoundsCgroups || accountingOf(sandbox.AccountingRlimits) != BoundsRlimits {
		t.Fatal("the sandbox's accountings do not map onto the seam's")
	}
}

// The CPU-time bound is the wall clock over the cores, plus one.
func TestCPUSeconds(t *testing.T) {
	if got := cpuSeconds(trust.Limits{CPU: 2, Timeout: 90 * time.Second}); got != 181 {
		t.Fatalf("cpuSeconds = %d", got)
	}
	if got := cpuSeconds(trust.Limits{CPU: 0.5, Timeout: 3 * time.Second}); got != 3 {
		t.Fatalf("cpuSeconds = %d", got)
	}
}

// A wait error that is the run context's own end is an outcome, not a
// failure to wait; any other wait error is.
func TestWaitFailed(t *testing.T) {
	cases := []struct {
		werr, clock error
		want        bool
	}{
		{nil, nil, false},
		{nil, context.DeadlineExceeded, false},
		{context.DeadlineExceeded, context.DeadlineExceeded, false},
		{fmt.Errorf("wrapped: %w", context.Canceled), context.Canceled, false},
		{context.DeadlineExceeded, nil, true},
		{context.Canceled, context.DeadlineExceeded, true},
		{errors.New("wait4: no child"), context.DeadlineExceeded, true},
	}
	for _, c := range cases {
		if got := waitFailed(c.werr, c.clock); got != c.want {
			t.Errorf("waitFailed(%v, %v) = %v", c.werr, c.clock, got)
		}
	}
}

// The report follows the facts in binding order: a counted bound
// first, whatever else happened; then the run context's end as the
// caller's cancellation or the wall clock — a kill that landed on a
// payload already exiting included; then an uncounted SIGKILL as the
// CPU-time bound or an external kill; and nothing otherwise.
func TestOutcome(t *testing.T) {
	l := trust.Limits{Memory: 4096, CPU: 1, Pids: 3, Timeout: 10 * time.Second}
	killed := sandbox.ExitStatus{Code: 137, Signaled: true, Signal: syscall.SIGKILL}
	cg := func(kills, forks uint64) sandbox.Stats {
		return sandbox.Stats{Accounting: sandbox.AccountingCgroups, MemoryKills: kills, ForksRefused: forks}
	}
	rl := sandbox.Stats{Accounting: sandbox.AccountingRlimits}
	deadline, cancelled := context.DeadlineExceeded, context.Canceled
	cases := []struct {
		name          string
		es            sandbox.ExitStatus
		st            sandbox.Stats
		clock, parent error
		werr          error
		is            error
		text          string
	}{
		{"clean", sandbox.ExitStatus{}, cg(0, 0), nil, nil, nil, nil, ""},
		{"plugin failure", sandbox.ExitStatus{Code: 7}, cg(0, 0), nil, nil, nil, nil, ""},
		{"memory kill", killed, cg(1, 0), nil, nil, nil, ErrBoundExceeded, "memory (4096 bytes) (enforced by cgroups)"},
		{"memory kill, exit 0", sandbox.ExitStatus{}, cg(2, 0), nil, nil, nil, ErrBoundExceeded, "memory (4096 bytes)"},
		{"memory kill at the wall clock", killed, cg(1, 0), deadline, nil, nil, ErrBoundExceeded, "memory (4096 bytes)"},
		{"fork refused, absorbed", sandbox.ExitStatus{}, cg(0, 1), nil, nil, nil, nil, ""},
		{"fork refused, failed", sandbox.ExitStatus{Code: 1}, cg(0, 1), nil, nil, nil, ErrBoundExceeded, "process count (3) (enforced by cgroups)"},
		{"memory before forks", sandbox.ExitStatus{Code: 1}, cg(1, 1), nil, nil, nil, ErrBoundExceeded, "memory (4096 bytes)"},
		{"wall clock", killed, rl, deadline, nil, nil, ErrBoundExceeded, "wall clock (10s, enforced by the runner)"},
		{"wall clock on an exiting payload", sandbox.ExitStatus{}, rl, deadline, nil, deadline, ErrBoundExceeded, "wall clock"},
		{"cancelled", killed, rl, cancelled, cancelled, nil, cancelled, "cancelled"},
		{"cancelled on an exiting payload", sandbox.ExitStatus{}, rl, cancelled, cancelled, cancelled, cancelled, "cancelled"},
		{"exited before the clock", sandbox.ExitStatus{Code: 0}, rl, deadline, nil, nil, nil, ""},
		{"uncounted SIGKILL, cgroups", killed, cg(0, 0), nil, nil, nil, nil, "the CPU-time bound (10s over 1 cores, enforced by rlimits) or an external kill"},
		{"uncounted SIGKILL, rlimits", killed, rl, nil, nil, nil, nil, "or an external kill"},
		{"other signal", sandbox.ExitStatus{Code: 143, Signaled: true, Signal: syscall.SIGTERM}, rl, nil, nil, nil, nil, ""},
	}
	for _, c := range cases {
		err := outcome(c.es, c.st, l, c.clock, c.parent, c.werr)
		if c.text == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%s: %v, want %q", c.name, err, c.text)
			continue
		}
		if c.is != nil && !errors.Is(err, c.is) {
			t.Errorf("%s: %v is not %v", c.name, err, c.is)
		}
		if c.is == nil && errors.Is(err, ErrBoundExceeded) {
			t.Errorf("%s: %v claims a bound", c.name, err)
		}
	}
}

// fakeSandbox is a sandbox whose run ends the way the test says, once
// the Start context ends or at once: it drives Run's post-wait reading
// without a kernel.
type fakeSandbox struct {
	ctx     context.Context
	es      sandbox.ExitStatus
	werr    func(ctx context.Context) error
	st      sandbox.Stats
	waitCtx bool // Wait blocks until the Start context ends
}

func (f *fakeSandbox) Start(ctx context.Context) error { f.ctx = ctx; return nil }
func (f *fakeSandbox) Wait() (sandbox.ExitStatus, error) {
	if f.waitCtx {
		<-f.ctx.Done()
	}
	var err error
	if f.werr != nil {
		err = f.werr(f.ctx)
	}
	return f.es, err
}
func (f *fakeSandbox) Signal(os.Signal) error        { return nil }
func (f *fakeSandbox) Destroy() error                { return nil }
func (f *fakeSandbox) Stats() (sandbox.Stats, error) { return f.st, nil }
func (f *fakeSandbox) Tier() sandbox.Isolation       { return sandbox.Strong }

// Run feeds the post-wait reading the run context's end, the caller's,
// and the wait error: a wait that returns the context's own error at
// the deadline is the wall clock; a kill at the deadline is the wall
// clock; a kill after the caller's cancellation is the cancellation.
func TestRunReadsContextEnds(t *testing.T) {
	killed := sandbox.ExitStatus{Code: 137, Signaled: true, Signal: syscall.SIGKILL}
	rl := sandbox.Stats{Accounting: sandbox.AccountingRlimits}
	ctxErr := func(ctx context.Context) error { return ctx.Err() }
	cases := []struct {
		name   string
		fake   *fakeSandbox
		cancel bool
		is     error
		text   string
	}{
		{"wait reports the deadline", &fakeSandbox{waitCtx: true, werr: ctxErr, st: rl}, false, ErrBoundExceeded, "wall clock"},
		{"killed at the deadline", &fakeSandbox{waitCtx: true, es: killed, st: rl}, false, ErrBoundExceeded, "wall clock"},
		{"killed after cancellation", &fakeSandbox{waitCtx: true, es: killed, st: rl}, true, context.Canceled, "cancelled"},
		{"clean exit", &fakeSandbox{st: rl}, false, nil, ""},
	}
	for _, c := range cases {
		ctx, cancel := context.WithCancel(context.Background())
		if c.cancel {
			go func() { time.Sleep(100 * time.Millisecond); cancel() }()
		}
		r := &SandboxRunner{create: func(sandbox.Spec) (sandbox.Sandbox, error) { return c.fake, nil }}
		res, err := r.Run(ctx, Spec{
			Rootfs:  "/export",
			Process: plugexec.Process{Argv: []string{"/plugin"}},
			Limits:  limits(func(l *trust.Limits) { l.Timeout = 300 * time.Millisecond }),
			MinTier: plugexec.TierStrong,
		})
		cancel()
		if c.text == "" {
			if err != nil || res == nil || res.Bounds != BoundsRlimits {
				t.Errorf("%s: %v %+v", c.name, err, res)
			}
			continue
		}
		if err == nil || !errors.Is(err, c.is) || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%s: %v, want %v naming %q", c.name, err, c.is, c.text)
		}
	}
}
