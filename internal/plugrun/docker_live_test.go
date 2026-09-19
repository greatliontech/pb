package plugrun

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/greatliontech/pb/internal/plugexec"
	"github.com/greatliontech/pb/internal/plugrun/testdata/behavior"
	"github.com/greatliontech/pb/internal/trust"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
	"pgregory.net/rapid"
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
	return r.Run(ctx, Spec{Scheme: plugexec.SchemeOCI, Rootfs: rootfsDir, Process: plugexec.Process{Argv: []string{"/plugin"}, Env: []string{"PB_PLUGIN_TEST_ENV=from-the-image"}}, Stdin: request(t, param), Limits: l, MinTier: plugexec.TierStrong})
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
	if res, err := liveRun(t, r, behavior.Write, limits(nil)); err != nil || content(t, res) != "write-err=true" {
		t.Fatalf("read-only root: %v %q", err, res.Stdout)
	}
	if res, err := liveRun(t, r, behavior.Net, limits(nil)); err != nil || content(t, res) != "ifaces=lo dial-err=true" {
		t.Fatalf("network: %v %q", err, res.Stdout)
	}
	if res, err := liveRun(t, r, behavior.Exit7, limits(nil)); err != nil || res.ExitCode != 7 || !strings.Contains(string(res.Stderr), "deliberate failure") {
		t.Fatalf("exit code: %v %+v", err, res)
	}
	// stdout and stderr stay apart without a terminal: the response
	// parses while stderr carries the line.
	if res, err := liveRun(t, r, behavior.Both, limits(nil)); err != nil || content(t, res) != "stdout-with-stderr" || !strings.Contains(string(res.Stderr), "a line on stderr") {
		t.Fatalf("streams: %v %+v", err, res)
	}
	if res, err := liveRun(t, r, behavior.Host, limits(nil)); err != nil || content(t, res) != "hostname=pb-plugin" {
		t.Fatalf("hostname: %v %q", err, res.Stdout)
	}
	if res, err := liveRun(t, r, behavior.Env, limits(nil)); err != nil || content(t, res) != "PB_PLUGIN_TEST_ENV=from-the-image" {
		t.Fatalf("environment: %v %q", err, res.Stdout)
	}
	start := time.Now()
	if _, err := liveRun(t, r, behavior.Sleep, limits(func(l *trust.Limits) { l.Timeout = 2 * time.Second })); !errors.Is(err, ErrBoundExceeded) || !strings.Contains(err.Error(), "wall clock (2s") {
		t.Fatalf("wall clock: %v", err)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("the kill did not end the run promptly")
	}
	if _, err := liveRun(t, r, behavior.Hog, limits(func(l *trust.Limits) { l.Memory = 64 << 20; l.Timeout = time.Minute })); !errors.Is(err, ErrBoundExceeded) || !strings.Contains(err.Error(), "memory (67108864 bytes) (enforced by cgroups)") {
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
	for _, param := range behavior.Compared {
		a, err := liveRun(t, native, param, limits(nil))
		if err != nil {
			t.Fatal(err)
		}
		b, err := liveRun(t, docker, param, limits(nil))
		if err != nil {
			t.Fatal(err)
		}
		if diff := responsesDiffer(a, b); diff != "" {
			t.Fatalf("param %q: %s", param, diff)
		}
	}
}

// responsesDiffer describes how two runs' responses differ, or says
// nothing when they are the same bytes, exit status and stderr.
func responsesDiffer(a, b *Result) string {
	if string(a.Stdout) != string(b.Stdout) || a.ExitCode != b.ExitCode || string(a.Stderr) != string(b.Stderr) {
		return fmt.Sprintf("native %q %q (%d) vs docker %q %q (%d)", a.Stdout, a.Stderr, a.ExitCode, b.Stdout, b.Stderr, b.ExitCode)
	}
	return ""
}

// The fixture answers every compared behavior and every probe by
// that behavior, so the lists the tests draw from name nothing the
// fixture would answer with its default.
func TestFixtureAnswersEveryBehavior(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the native runner is Linux-only")
	}
	native, err := NativeRunner()
	if err != nil {
		t.Fatal(err)
	}
	requireSandbox(t)
	for _, param := range slices.Concat(behavior.Compared, behavior.Probes) {
		res, err := liveRun(t, native, param, limits(nil))
		if err != nil {
			t.Fatal(err)
		}
		if param == behavior.Exit7 {
			if res.ExitCode != 7 {
				t.Errorf("behavior %q exited %d", param, res.ExitCode)
			}
			continue
		}
		if got := content(t, res); (param == behavior.Default) != strings.HasPrefix(got, "files=") {
			t.Errorf("behavior %q answered %q", param, got)
		}
	}
}

// The docker byte path against a real daemon: the daemon pulls the
// repository at the verified digest from a registry of the test's,
// for the platform pb checked, creates the container from it under
// the image's own configuration — its entrypoint and environment,
// which pb never passed — and the run is judged as any other
// (REQ-plugin-core-verifies).
func TestDockerLivePull(t *testing.T) {
	r := requireDaemon(t)
	// The registry is the test process's loopback, which only a
	// daemon on this host reaches: a remote daemon, or one in a
	// virtual machine (Docker Desktop), is skipped, not failed.
	if host := os.Getenv("DOCKER_HOST"); runtime.GOOS != "linux" || (host != "" && !strings.HasPrefix(host, "unix://")) {
		t.Skipf("the daemon is not on this host's loopback (GOOS %s, DOCKER_HOST %q)", runtime.GOOS, host)
	}
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer srv.Close()
	// The daemon reaches a loopback registry over plain HTTP: the
	// loopback range is insecure by the daemon's default.
	host := strings.TrimPrefix(srv.URL, "http://")
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(writeTar(pw, rootfsDir)) }()
		return pr, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	os_, arch := r.Platform()
	img, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{OS: os_, Architecture: arch, Config: v1.Config{Entrypoint: []string{"/plugin"}, Env: []string{"PB_PLUGIN_TEST_ENV=from-the-image"}}})
	if err != nil {
		t.Fatal(err)
	}
	img, err = mutate.AppendLayers(img, layer)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.ParseReference(host + "/live/plugin:v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	image := host + "/live/plugin@" + digest.String()
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "rmi", image).CombinedOutput(); err != nil {
			t.Errorf("releasing the pulled image: %v\n%s", err, out)
		}
	})
	run := func(param string) (*Result, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		return r.Run(ctx, Spec{Scheme: plugexec.SchemeOCI, Image: image, Pull: true, Stdin: request(t, param), Limits: limits(nil), MinTier: plugexec.TierStrong})
	}
	res, err := run("")
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != plugexec.TierStrong || res.Bounds != BoundsCgroups || content(t, res) != "files=2" {
		t.Fatalf("result = %+v (%s)", res, content(t, res))
	}
	if res, err := run(behavior.Env); err != nil || content(t, res) != "PB_PLUGIN_TEST_ENV=from-the-image" {
		t.Fatalf("the image's own environment: %v %q", err, res.Stdout)
	}
	if res, err := run(behavior.Write); err != nil || content(t, res) != "write-err=true" {
		t.Fatalf("read-only root: %v %q", err, res.Stdout)
	}
	// A digest the registry does not hold is the daemon's refusal.
	unknown := host + "/live/plugin@sha256:" + strings.Repeat("1", 64)
	if _, err := r.Run(context.Background(), Spec{Scheme: plugexec.SchemeOCI, Image: unknown, Pull: true, Stdin: request(t, ""), Limits: limits(nil), MinTier: plugexec.TierStrong}); err == nil || !strings.Contains(err.Error(), "the daemon pulling "+unknown) {
		t.Fatalf("an unknown digest: %v", err)
	}
}

// Runner independence as a property: for any request — a parameter
// the fixture answers by a behavior or any other string, over any
// file list — the native runner and the docker runner return the same
// response bytes, exit status and stderr
// (REQ-plugin-runner-independence). The draw count and the shrink
// time are bounded here, over the operator's -rapid flags, because
// each case runs a container.
func TestPropertyRunnerIndependence(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the native runner is Linux-only")
	}
	docker := requireDaemon(t)
	native, err := NativeRunner()
	if err != nil {
		t.Fatal(err)
	}
	requireSandbox(t)
	for name, value := range map[string]string{"rapid.checks": "12", "rapid.shrinktime": "20s"} {
		prev := flag.Lookup(name).Value.String()
		if err := flag.Set(name, value); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := flag.Set(name, prev); err != nil {
				t.Error(err)
			}
		})
	}
	rapid.Check(t, func(rt *rapid.T) {
		param := rapid.OneOf(
			rapid.SampledFrom(behavior.Compared),
			rapid.StringMatching(`[a-z0-9=,]{0,12}`),
		).Draw(rt, "param")
		files := rapid.SliceOfN(rapid.StringMatching(`[a-z]{1,8}\.proto`), 0, 8).Draw(rt, "files")
		req, err := proto.Marshal(&pluginpb.CodeGeneratorRequest{FileToGenerate: files, Parameter: &param})
		if err != nil {
			rt.Fatal(err)
		}
		run := func(r Runner) *Result {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			res, err := r.Run(ctx, Spec{Scheme: plugexec.SchemeOCI, Rootfs: rootfsDir, Process: plugexec.Process{Argv: []string{"/plugin"}, Env: []string{"PB_PLUGIN_TEST_ENV=from-the-image"}}, Stdin: req, Limits: limits(nil), MinTier: plugexec.TierStrong})
			if err != nil {
				rt.Fatalf("%T: %v", r, err)
			}
			return res
		}
		if diff := responsesDiffer(run(native), run(docker)); diff != "" {
			rt.Fatalf("param %q files %v: %s", param, files, diff)
		}
	})
}
