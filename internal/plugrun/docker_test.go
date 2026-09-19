package plugrun

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/greatliontech/pb/internal/plugexec"
	"github.com/greatliontech/pb/internal/trust"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

func dockerSpec(t *testing.T, rootfs string, l trust.Limits) Spec {
	t.Helper()
	return Spec{
		Scheme:  plugexec.SchemeOCI,
		Rootfs:  rootfs,
		Process: plugexec.Process{Argv: []string{"/plugin", "--flag"}, Env: []string{"A=1", "B=two"}, WorkDir: "/w"},
		Stdin:   request(t, ""),
		Limits:  l,
		MinTier: plugexec.TierStrong,
	}
}

// exportFixture is a small tree shaped like an image export.
func exportFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "plugin"), []byte("#!/x\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "etc", "..", "note"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("bin/plugin", filepath.Join(dir, "plugin")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The protocol against the daemon, in order: the export imported as a
// tar, the container created with exactly the boundary and the
// bounds, its record read before it starts, the request on stdin,
// the record read after, the container and the image released; the
// result carries the derived tier and accounting and the plugin's
// bytes (REQ-plugin-sandboxed, REQ-plugin-resource-bounds,
// REQ-plugin-reported-tier).
func TestDockerProtocol(t *testing.T) {
	dir := fakeDaemon(t)
	rootfs := exportFixture(t)
	l := trust.Limits{Memory: 64 << 20, CPU: 2, Pids: 7, Timeout: 90 * time.Second}
	r, err := NewDockerRunner("")
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), dockerSpec(t, rootfs, l))
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != plugexec.TierStrong || res.Bounds != BoundsCgroups || res.ExitCode != 0 {
		t.Fatalf("result = %+v", res)
	}
	if got := content(t, res); got != "from the fake daemon" {
		t.Fatalf("stdout = %q", got)
	}
	verbs, argv := fakeLog(t, dir)
	if want := []string{"version", "info", "import", "create", "inspect", "start", "inspect", "rm", "rmi"}; !reflect.DeepEqual(verbs, want) {
		t.Fatalf("invocations = %v, want %v", verbs, want)
	}
	create := argv[3]
	flag := func(name string) []string {
		var vals []string
		for i := 0; i+1 < len(create); i++ {
			if create[i] == name {
				vals = append(vals, create[i+1])
			}
		}
		return vals
	}
	for name, want := range map[string][]string{"--network": {"none"}, "--hostname": {"pb-plugin"}, "--memory": {"67108864"}, "--memory-swap": {"67108864"}, "--pids-limit": {"7"}, "--ulimit": {"cpu=181"}, "--cap-drop": {"ALL"}, "--security-opt": {"no-new-privileges"}, "--workdir": {"/w"}, "--env": {"A=1", "B=two"}, "--entrypoint": {"/plugin"}} {
		if got := flag(name); !reflect.DeepEqual(got, want) {
			t.Errorf("create %s = %q, want %q", name, got, want)
		}
	}
	for _, bare := range []string{"--interactive", "--read-only"} {
		if !slices.Contains(create, bare) {
			t.Errorf("create lacks %s", bare)
		}
	}
	if slices.Contains(create, "--env-file") {
		t.Error("create reads an env file, whose grammar imports the client's environment")
	}
	image := create[len(create)-2]
	if !strings.HasPrefix(image, "sha256:") || create[len(create)-1] != "--flag" || create[len(create)-3] != "/plugin" {
		t.Fatalf("create tail = %q", create[len(create)-4:])
	}
	if argv[4][3] != "fakecontainer" || argv[5][3] != "fakecontainer" || argv[7][2] != "fakecontainer" || argv[8][1] != image {
		t.Fatalf("container and image not carried through: %q %q %q", argv[5], argv[7], argv[8])
	}
	if stdin, _ := os.ReadFile(filepath.Join(dir, "stdin")); !bytes.Equal(stdin, request(t, "")) {
		t.Fatal("the request did not reach the container's stdin")
	}
	if os_, arch := r.Platform(); os_ != "linux" || arch != "fakearch" {
		t.Fatalf("platform = %s/%s: the daemon's, not the host's", os_, arch)
	}
	tarBytes, err := os.ReadFile(filepath.Join(dir, "import.tar"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	tr := tar.NewReader(bytes.NewReader(tarBytes))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Name == "bin/plugin" && (h.Mode != 0o755 || h.Size != 5) {
			t.Fatalf("bin/plugin header = %+v", h)
		}
		if h.Name == "plugin" && (h.Typeflag != tar.TypeSymlink || h.Linkname != "bin/plugin") {
			t.Fatalf("symlink header = %+v", h)
		}
	}
	if want := []string{"bin/", "bin/plugin", "note", "plugin"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("tar entries = %v, want %v", names, want)
	}
}

// A record that does not show the boundary, or does not carry the
// policy's bounds, refuses the run before the container starts; a
// record whose tier sits below the floor is a tier refusal.
func TestDockerRecordRefuses(t *testing.T) {
	l := trust.Limits{Memory: 64 << 20, CPU: 2, Pids: 7, Timeout: 90 * time.Second}
	cases := []struct {
		name  string
		patch string
		floor string
		is    error
		text  string
	}{
		{"network", `{"NetworkMode":"bridge"}`, plugexec.TierStrong, nil, `does not show the sandbox boundary (network mode "bridge")`},
		{"writable root", `{"ReadonlyRootfs":false}`, plugexec.TierStrong, nil, "a writable root"},
		{"privileged", `{"Privileged":true}`, plugexec.TierStrong, nil, "privileged"},
		{"capabilities", `{"CapDrop":[]}`, plugexec.TierStrong, nil, "capabilities kept"},
		{"new privileges", `{"SecurityOpt":[]}`, plugexec.TierStrong, nil, "new privileges allowed"},
		{"seccomp off", `{"SecurityOpt":["no-new-privileges","seccomp=unconfined"]}`, plugexec.TierStrong, nil, "seccomp=unconfined"},
		{"isolation", `{"Isolation":"hyperv"}`, plugexec.TierStrong, nil, `isolation "hyperv"`},
		{"memory", `{"Memory":4096}`, plugexec.TierStrong, nil, "does not carry the policy's bounds (memory 4096 with swap to 67108864)"},
		{"swap", `{"MemorySwap":134217728}`, plugexec.TierStrong, nil, "with swap to 134217728"},
		{"apparmor", `{"SecurityOpt":["no-new-privileges","apparmor=unconfined"]}`, plugexec.TierStrong, nil, `security option "apparmor=unconfined"`},
		{"apparmor profile", `{"AppArmorProfile":"unconfined"}`, plugexec.TierStrong, nil, "apparmor unconfined"},
		{"cap added", `{"CapAdd":["SYS_ADMIN"]}`, plugexec.TierStrong, nil, "capabilities added [SYS_ADMIN]"},
		{"pid host", `{"PidMode":"host"}`, plugexec.TierStrong, nil, `pid namespace "host"`},
		{"userns host", `{"UsernsMode":"host"}`, plugexec.TierStrong, nil, `user namespace "host"`},
		{"cgroupns host", `{"CgroupnsMode":"host"}`, plugexec.TierStrong, nil, "the host's cgroup namespace"},
		{"binds", `{"Binds":["/:/host"]}`, plugexec.TierStrong, nil, "devices, mounts or sysctls"},
		{"sysctls", `{"Sysctls":{"net.ipv4.ip_forward":"1"}}`, plugexec.TierStrong, nil, "devices, mounts or sysctls"},
		{"runtime", `{"Runtime":"nvidia"}`, plugexec.TierStrong, nil, `runtime "nvidia"`},
		{"hostname", `{"Config":{"Hostname":"abc"}}`, plugexec.TierStrong, nil, `names hostname "abc"`},
		{"env dropped", `{"Config":{"Env":["A=1"]}}`, plugexec.TierStrong, nil, `lacks the image's environment entry "B=two"`},
		{"pids", `{"PidsLimit":null}`, plugexec.TierStrong, nil, "process count"},
		{"cpu", `{"Ulimits":[]}`, plugexec.TierStrong, nil, "CPU time"},
		{"daemon seccomp", `{}`, plugexec.TierStrong, nil, `daemon seccomp profile ""`},
		{"daemon unconfined", `{}`, plugexec.TierStrong, nil, `daemon seccomp profile "unconfined"`},
		{"daemon custom profile", `{}`, plugexec.TierStrong, nil, `daemon seccomp profile "/etc/docker/loose.json"`},
	}
	for _, c := range cases {
		dir := fakeDaemon(t)
		if err := os.WriteFile(filepath.Join(dir, "record.json"), []byte(c.patch), 0o644); err != nil {
			t.Fatal(err)
		}
		switch c.name {
		case "daemon seccomp":
			os.WriteFile(filepath.Join(dir, "info.json"), []byte(`["name=cgroupns"]`), 0o644)
		case "daemon unconfined":
			os.WriteFile(filepath.Join(dir, "info.json"), []byte(`["name=seccomp,profile=unconfined","name=cgroupns"]`), 0o644)
		case "daemon custom profile":
			os.WriteFile(filepath.Join(dir, "info.json"), []byte(`["name=seccomp,profile=/etc/docker/loose.json","name=cgroupns"]`), 0o644)
		}
		r, err := NewDockerRunner("")
		if err != nil {
			t.Fatal(err)
		}
		spec := dockerSpec(t, exportFixture(t), l)
		spec.MinTier = c.floor
		_, err = r.Run(context.Background(), spec)
		if err == nil || !strings.Contains(err.Error(), c.text) || errors.Is(err, ErrTierUnreachable) {
			t.Errorf("%s: %v, want a refusal naming %q", c.name, err, c.text)
		}
		verbs, _ := fakeLog(t, dir)
		if slices.Contains(verbs, "start") || !slices.Contains(verbs, "rm") || !slices.Contains(verbs, "rmi") {
			t.Errorf("%s: invocations %v: started, or not released", c.name, verbs)
		}
	}
}

// The floor is judged against the derived tier before the start.
func TestDockerFloor(t *testing.T) {
	fakeDaemon(t)
	r, err := NewDockerRunner("")
	if err != nil {
		t.Fatal(err)
	}
	l := trust.Limits{Memory: 64 << 20, CPU: 2, Pids: 7, Timeout: 90 * time.Second}
	for _, floor := range []string{plugexec.TierNone, plugexec.TierMinimal, plugexec.TierOS, plugexec.TierStrong} {
		spec := dockerSpec(t, exportFixture(t), l)
		spec.MinTier = floor
		if _, err := r.Run(context.Background(), spec); err != nil {
			t.Fatalf("floor %s under a Strong record: %v", floor, err)
		}
	}
	spec := dockerSpec(t, exportFixture(t), l)
	spec.MinTier = ""
	if _, err := r.Run(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "no sandbox tier floor") {
		t.Fatalf("no floor: %v", err)
	}
}

// The outcome follows the daemon's record: a memory kill it recorded
// is the memory bound; the wall clock kills the container through
// the daemon and names itself; a cancelled caller is a cancellation;
// a SIGKILL death it did not attribute is the CPU-time bound or an
// external kill; a plain exit is the plugin's own.
func TestDockerOutcome(t *testing.T) {
	l := trust.Limits{Memory: 64 << 20, CPU: 2, Pids: 7, Timeout: 90 * time.Second}
	cases := []struct {
		name   string
		state  string
		hang   bool
		cancel bool
		is     error
		text   string
		exit   int
	}{
		{"memory kill", `{"Status":"exited","ExitCode":137,"OOMKilled":true}`, false, false, ErrBoundExceeded, "memory (67108864 bytes) (enforced by cgroups)", 0},
		{"wall clock", "", true, false, ErrBoundExceeded, "wall clock (300ms, enforced by the runner)", 0},
		{"cancelled", "", true, true, context.Canceled, "cancelled", 0},
		{"status 137", `{"Status":"exited","ExitCode":137}`, false, false, nil, "the CPU-time bound (1m30s over 2 cores, enforced by rlimits), an external kill, or the plugin's own exit 137", 0},
		{"plugin exit", `{"Status":"exited","ExitCode":7}`, false, false, nil, "", 7},
		{"never ran", `{"Status":"created","ExitCode":0}`, false, false, nil, `did not run to an exit (status "created")`, 0},
		{"start error", `{"Status":"created","ExitCode":127,"Error":"exec: \"/nope\": no such file"}`, false, false, nil, `starting the plugin process: exec: "/nope": no such file`, 0},
		{"clean", `{"Status":"exited","ExitCode":0}`, false, false, nil, "", 0},
	}
	for _, c := range cases {
		dir := fakeDaemon(t)
		if c.state != "" {
			os.WriteFile(filepath.Join(dir, "state.json"), []byte(c.state), 0o644)
		}
		if c.hang {
			os.WriteFile(filepath.Join(dir, "start"), []byte("hang"), 0o644)
		}
		limits := l
		if c.hang {
			limits.Timeout = 300 * time.Millisecond
		}
		ctx, cancel := context.WithCancel(context.Background())
		if c.cancel {
			limits.Timeout = time.Minute
			go func() { time.Sleep(100 * time.Millisecond); cancel() }()
		}
		r, err := NewDockerRunner("")
		if err != nil {
			t.Fatal(err)
		}
		res, err := r.Run(ctx, dockerSpec(t, exportFixture(t), limits))
		cancel()
		verbs, _ := fakeLog(t, dir)
		if c.hang && !slices.Contains(verbs, "kill") {
			t.Errorf("%s: the container was not killed: %v", c.name, verbs)
		}
		if c.text == "" {
			if err != nil || res.ExitCode != c.exit {
				t.Errorf("%s: %v %+v", c.name, err, res)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.text) || (c.is != nil && !errors.Is(err, c.is)) || (c.is == nil && errors.Is(err, ErrBoundExceeded)) {
			t.Errorf("%s: %v, want %q (%v)", c.name, err, c.text, c.is)
		}
	}
}

// A daemon that cannot be reached is a runner that is unavailable,
// named as such from selection.
func TestDockerUnavailable(t *testing.T) {
	dir := fakeDaemon(t)
	os.WriteFile(filepath.Join(dir, "unavailable"), nil, 0o644)
	if _, err := NewDockerRunner(""); err == nil || !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Fatalf("NewDockerRunner: %v", err)
	}
	docker := RunnerDocker
	if _, err := Open(&docker, ""); err == nil || !strings.Contains(err.Error(), "runner docker (from the --runner flag) is unavailable: docker version") {
		t.Fatalf("Open: %v", err)
	}
	os.Remove(filepath.Join(dir, "unavailable"))
	r, err := Open(&docker, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*DockerRunner); !ok {
		t.Fatalf("runner = %T", r)
	}
}

// The export tar is deterministic and refuses what an export never
// holds.
func TestWriteTar(t *testing.T) {
	dir := exportFixture(t)
	var a, b bytes.Buffer
	if err := writeTar(&a, dir); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	os.Chtimes(filepath.Join(dir, "note"), time.Now(), time.Now())
	if err := writeTar(&b, dir); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("the same tree yielded different tars")
	}
	if err := os.Mkdir(filepath.Join(dir, "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unixSocketAt(filepath.Join(dir, "dev", "sock")); err != nil {
		t.Skip(err)
	}
	if err := writeTar(io.Discard, dir); err == nil || !strings.Contains(err.Error(), "neither a directory, a file nor a symlink") {
		t.Fatalf("a socket in the export: %v", err)
	}
}

// A response the daemon relays is judged exactly like the native
// runner's (REQ-plugin-response-authority).
func TestDockerResponseAuthority(t *testing.T) {
	dir := fakeDaemon(t)
	bad, _ := proto.Marshal(&pluginpb.CodeGeneratorResponse{Error: proto.String("declared failure")})
	os.WriteFile(filepath.Join(dir, "stdout"), bad, 0o644)
	r, _ := NewDockerRunner("")
	res, err := r.Run(context.Background(), dockerSpec(t, exportFixture(t), trust.Limits{Memory: 64 << 20, CPU: 2, Pids: 7, Timeout: time.Minute}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Respond(res); err == nil || !strings.Contains(err.Error(), "declared failure") {
		t.Fatalf("Respond: %v", err)
	}
}

// An environment entry that is not KEY=VALUE never reaches the
// daemon: its grammar would read a bare name from pb's own
// environment.
func TestDockerRefusesBareEnv(t *testing.T) {
	dir := fakeDaemon(t)
	r, err := NewDockerRunner("")
	if err != nil {
		t.Fatal(err)
	}
	spec := dockerSpec(t, exportFixture(t), trust.Limits{Memory: 64 << 20, CPU: 2, Pids: 7, Timeout: time.Minute})
	spec.Process.Env = []string{"A=1", "PB_SECRET"}
	_, err = r.Run(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), `"PB_SECRET" is not KEY=VALUE`) {
		t.Fatalf("bare env: %v", err)
	}
	if verbs, _ := fakeLog(t, dir); slices.Contains(verbs, "create") || slices.Contains(verbs, "import") {
		t.Fatalf("the daemon was reached: %v", verbs)
	}
}

// The docker runner runs images only: a local plugin is refused
// before the daemon is reached.
func TestDockerRefusesLocal(t *testing.T) {
	dir := fakeDaemon(t)
	r, err := NewDockerRunner("")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), Spec{Scheme: plugexec.SchemeLocal, Process: plugexec.Process{Argv: []string{"/usr/bin/gen"}}, Limits: trust.Limits{Memory: 64 << 20, CPU: 2, Pids: 7, Timeout: time.Minute}, MinTier: plugexec.TierNone})
	if err == nil || !strings.Contains(err.Error(), "runs images only") {
		t.Fatalf("local on docker: %v", err)
	}
	if verbs, _ := fakeLog(t, dir); slices.Contains(verbs, "import") {
		t.Fatalf("the daemon was reached: %v", verbs)
	}
}

// A daemon-local image runs as it is: no import, no release of the
// image, no entrypoint of pb's; the record checks still hold.
func TestDockerDaemonLocalImage(t *testing.T) {
	dir := fakeDaemon(t)
	r, err := NewDockerRunner("")
	if err != nil {
		t.Fatal(err)
	}
	l := trust.Limits{Memory: 64 << 20, CPU: 2, Pids: 7, Timeout: 90 * time.Second}
	res, err := r.Run(context.Background(), Spec{Scheme: plugexec.SchemeOCI, Image: "plugins/q:dev", Stdin: request(t, ""), Limits: l, MinTier: plugexec.TierStrong})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != plugexec.TierStrong || content(t, res) != "from the fake daemon" {
		t.Fatalf("result = %+v", res)
	}
	verbs, argv := fakeLog(t, dir)
	if want := []string{"version", "info", "create", "inspect", "start", "inspect", "rm"}; !reflect.DeepEqual(verbs, want) {
		t.Fatalf("invocations = %v, want %v", verbs, want)
	}
	create := argv[2]
	if slices.Contains(create, "--entrypoint") || create[len(create)-1] != "plugins/q:dev" || create[len(create)-3] != "--pull" || create[len(create)-2] != "never" {
		t.Fatalf("create = %q", create)
	}
	// A name that is no image reference, or a flag in its place, is
	// refused before the daemon is asked.
	for _, bad := range []string{"--privileged", "", "not a ref!"} {
		_, err := r.Run(context.Background(), Spec{Scheme: plugexec.SchemeOCI, Image: bad, Limits: l, MinTier: plugexec.TierStrong})
		if err == nil || !(strings.Contains(err.Error(), "does not name a daemon-local image") || strings.Contains(err.Error(), "exactly one of")) {
			t.Errorf("image %q: %v", bad, err)
		}
	}
	// Both worlds, or neither, refuse.
	for _, spec := range []Spec{
		{Scheme: plugexec.SchemeOCI, Image: "x", Rootfs: "/r", Process: plugexec.Process{Argv: []string{"/p"}}, Limits: l, MinTier: plugexec.TierStrong},
		{Scheme: plugexec.SchemeLocal, Image: "x", Process: plugexec.Process{Argv: []string{"/p"}}, Limits: l, MinTier: plugexec.TierNone},
	} {
		if _, err := r.Run(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "exactly one of") && !strings.Contains(err.Error(), "world of its own") {
			t.Errorf("%+v accepted: %v", spec, err)
		}
	}
}
