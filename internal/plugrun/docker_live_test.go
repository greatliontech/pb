package plugrun

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/greatliontech/pb/internal/plugexec"
	"github.com/greatliontech/pb/internal/trust"
)

// requireDaemon skips the live docker tests where no daemon answers,
// unless PB_TEST_REQUIRE_DOCKER demands them.
func requireDaemon(t *testing.T) *DockerRunner {
	t.Helper()
	if rootfsErr != nil {
		t.Fatal(rootfsErr)
	}
	if err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Run(); err != nil {
		if os.Getenv("PB_TEST_REQUIRE_DOCKER") != "" {
			t.Fatalf("PB_TEST_REQUIRE_DOCKER is set and no daemon answers: %v", err)
		}
		t.Skipf("no docker daemon: %v", err)
	}
	r, err := NewDockerRunner("")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func liveRun(t *testing.T, r Runner, param string, l trust.Limits) (*Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	return r.Run(ctx, Spec{Rootfs: rootfsDir, Process: plugexec.Process{Argv: []string{"/plugin"}, Env: []string{"PB_PLUGIN_TEST_ENV=from-the-image"}}, Stdin: request(t, param), Limits: l, MinTier: plugexec.TierStrong})
}

// The docker runner against a real daemon: the request reaches the
// plugin and the response comes back, at the derived tier; the root
// is read-only; there is no network; a non-zero exit is the
// plugin's; the wall clock kills the container; the memory bound
// is the daemon's recorded kill.
func TestDockerLive(t *testing.T) {
	r := requireDaemon(t)
	res, err := liveRun(t, r, "", limits(nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != plugexec.TierStrong || res.Bounds != BoundsCgroups || content(t, res) != "files=2" {
		t.Fatalf("result = %+v (%s)", res, content(t, res))
	}
	if res, err := liveRun(t, r, "write", limits(nil)); err != nil || content(t, res) != "write-err=true" {
		t.Fatalf("read-only root: %v %q", err, res.Stdout)
	}
	if res, err := liveRun(t, r, "net", limits(nil)); err != nil || content(t, res) != "ifaces=lo dial-err=true" {
		t.Fatalf("network: %v %q", err, res.Stdout)
	}
	if res, err := liveRun(t, r, "exit7", limits(nil)); err != nil || res.ExitCode != 7 || !strings.Contains(string(res.Stderr), "deliberate failure") {
		t.Fatalf("exit code: %v %+v", err, res)
	}
	// stdout and stderr stay apart without a terminal: the response
	// parses while stderr carries the line.
	if res, err := liveRun(t, r, "both", limits(nil)); err != nil || content(t, res) != "stdout-with-stderr" || !strings.Contains(string(res.Stderr), "a line on stderr") {
		t.Fatalf("streams: %v %+v", err, res)
	}
	if res, err := liveRun(t, r, "host", limits(nil)); err != nil || content(t, res) != "hostname=pb-plugin" {
		t.Fatalf("hostname: %v %q", err, res.Stdout)
	}
	if res, err := liveRun(t, r, "env", limits(nil)); err != nil || content(t, res) != "PB_PLUGIN_TEST_ENV=from-the-image" {
		t.Fatalf("environment: %v %q", err, res.Stdout)
	}
	start := time.Now()
	if _, err := liveRun(t, r, "sleep", limits(func(l *trust.Limits) { l.Timeout = 2 * time.Second })); !errors.Is(err, ErrBoundExceeded) || !strings.Contains(err.Error(), "wall clock (2s") {
		t.Fatalf("wall clock: %v", err)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("the kill did not end the run promptly")
	}
	if _, err := liveRun(t, r, "hog", limits(func(l *trust.Limits) { l.Memory = 64 << 20; l.Timeout = time.Minute })); !errors.Is(err, ErrBoundExceeded) || !strings.Contains(err.Error(), "memory (67108864 bytes) (enforced by cgroups)") {
		t.Fatalf("memory bound: %v", err)
	}
}

// Runner independence, falsified or not: the same pinned content and
// request yield the same response bytes under the native runner and
// the docker runner (REQ-plugin-runner-independence).
func TestRunnerIndependence(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the native runner is Linux-only")
	}
	docker := requireDaemon(t)
	native, err := NativeRunner()
	if err != nil {
		t.Fatal(err)
	}
	requireSandbox(t)
	for _, param := range []string{"", "net", "write", "host", "both", "env", "exit7"} {
		a, err := liveRun(t, native, param, limits(nil))
		if err != nil {
			t.Fatal(err)
		}
		b, err := liveRun(t, docker, param, limits(nil))
		if err != nil {
			t.Fatal(err)
		}
		if string(a.Stdout) != string(b.Stdout) || a.ExitCode != b.ExitCode || string(a.Stderr) != string(b.Stderr) {
			t.Fatalf("param %q: native %q %q (%d) vs docker %q %q (%d)", param, a.Stdout, a.Stderr, a.ExitCode, b.Stdout, b.Stderr, b.ExitCode)
		}
	}
}
