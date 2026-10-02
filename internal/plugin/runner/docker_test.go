package runner

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

	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/pb/internal/userconfig"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

func dockerSpec(t *testing.T, rootfs string, l trust.Limits) Spec {
	t.Helper()
	return Spec{
		Scheme:  plugin.SchemeOCI,
		Image:   &plugin.Export{Rootfs: rootfs},
		Process: plugin.Process{Argv: []string{"/plugin", "--flag"}, Env: []string{"A=1", "B=two"}, WorkDir: "/w"},
		Stdin:   request(t, ""),
		Limits:  l,
		MinTier: plugin.TierStrong,
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
	if res.Tier != plugin.TierStrong || res.Bounds != BoundsCgroups || res.ExitCode != 0 {
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
	for name, want := range map[string][]string{"--network": {"none"}, "--hostname": {"pb-plugin"}, "--memory": {"67108864"}, "--memory-swap": {"67108864"}, "--pids-limit": {"7"}, "--ulimit": {"cpu=181"}, "--cap-drop": {"ALL"}, "--security-opt": {"no-new-privileges"}, "--workdir": {"/w"}, "--env": {"A=1", "B=two"}, "--entrypoint": {"/plugin"}, "--platform": {"linux/fakearch"}} {
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
	if argv[4][3] != "fakecontainer" || argv[5][3] != "fakecontainer" || argv[7][3] != "fakecontainer" || argv[8][1] != image {
		t.Fatalf("container and image not carried through: %q %q %q", argv[5], argv[7], argv[8])
	}
	if stdin, _ := os.ReadFile(filepath.Join(dir, "stdin")); !bytes.Equal(stdin, request(t, "")) {
		t.Fatal("the request did not reach the container's stdin")
	}
	if p := r.Platform(); p.OS != "linux" || p.Arch != "fakearch" {
		t.Fatalf("platform = %s: the daemon's, not the host's", p)
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
		{"network", `{"NetworkMode":"bridge"}`, plugin.TierStrong, nil, `does not show the sandbox boundary (network mode "bridge")`},
		{"writable root", `{"ReadonlyRootfs":false}`, plugin.TierStrong, nil, "a writable root"},
		{"privileged", `{"Privileged":true}`, plugin.TierStrong, nil, "privileged"},
		{"capabilities", `{"CapDrop":[]}`, plugin.TierStrong, nil, "capabilities kept"},
		{"new privileges", `{"SecurityOpt":[]}`, plugin.TierStrong, nil, "new privileges allowed"},
		{"seccomp off", `{"SecurityOpt":["no-new-privileges","seccomp=unconfined"]}`, plugin.TierStrong, nil, "seccomp=unconfined"},
		{"isolation", `{"Isolation":"hyperv"}`, plugin.TierStrong, nil, `isolation "hyperv"`},
		{"memory", `{"Memory":4096}`, plugin.TierStrong, nil, "does not carry the policy's bounds (memory 4096 with swap to 67108864)"},
		{"swap", `{"MemorySwap":134217728}`, plugin.TierStrong, nil, "with swap to 134217728"},
		{"apparmor", `{"SecurityOpt":["no-new-privileges","apparmor=unconfined"]}`, plugin.TierStrong, nil, `security option "apparmor=unconfined"`},
		{"apparmor profile", `{"AppArmorProfile":"unconfined"}`, plugin.TierStrong, nil, "apparmor unconfined"},
		{"cap added", `{"CapAdd":["SYS_ADMIN"]}`, plugin.TierStrong, nil, "capabilities added [SYS_ADMIN]"},
		{"pid host", `{"PidMode":"host"}`, plugin.TierStrong, nil, `pid namespace "host"`},
		{"userns host", `{"UsernsMode":"host"}`, plugin.TierStrong, nil, `user namespace "host"`},
		{"cgroupns host", `{"CgroupnsMode":"host"}`, plugin.TierStrong, nil, "the host's cgroup namespace"},
		{"binds", `{"Binds":["/:/host"]}`, plugin.TierStrong, nil, "devices, mounts or sysctls"},
		{"sysctls", `{"Sysctls":{"net.ipv4.ip_forward":"1"}}`, plugin.TierStrong, nil, "devices, mounts or sysctls"},
		{"runtime", `{"Runtime":"nvidia"}`, plugin.TierStrong, nil, `runtime "nvidia"`},
		{"hostname", `{"Config":{"Hostname":"abc"}}`, plugin.TierStrong, nil, `names hostname "abc"`},
		{"env dropped", `{"Config":{"Env":["A=1"]}}`, plugin.TierStrong, nil, `lacks the image's environment entry "B=two"`},
		{"pids", `{"PidsLimit":null}`, plugin.TierStrong, nil, "process count"},
		{"cpu", `{"Ulimits":[]}`, plugin.TierStrong, nil, "CPU time"},
		{"daemon seccomp", `{}`, plugin.TierStrong, nil, `daemon seccomp profile ""`},
		{"daemon unconfined", `{}`, plugin.TierStrong, nil, `daemon seccomp profile "unconfined"`},
		{"daemon custom profile", `{}`, plugin.TierStrong, nil, `daemon seccomp profile "/etc/docker/loose.json"`},
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
	for _, floor := range []string{plugin.TierNone, plugin.TierMinimal, plugin.TierOS, plugin.TierStrong} {
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
		plant  string // a fake-daemon marker file, or an oom event's offset from the finish
		now    string // the daemon's clock past the finish at the read; a second when empty
		hang   bool
		cancel bool
		is     error
		text   string
		exit   int
		events bool // the daemon's event log consulted
	}{
		{"memory kill", `{"Status":"exited","ExitCode":137,"OOMKilled":true}`, "5ms", "", false, false, ErrBoundExceeded, "memory (67108864 bytes) (enforced by cgroups)", 0, true},
		{"memory kill in the event log alone", `{"Status":"exited","ExitCode":137}`, "5ms", "", false, false, ErrBoundExceeded, "memory (67108864 bytes) (enforced by cgroups)", 0, true},
		{"memory kill just before the exit", `{"Status":"exited","ExitCode":137}`, "-50ms", "", false, false, ErrBoundExceeded, "memory (67108864 bytes) (enforced by cgroups)", 0, true},
		{"memory kill outlived a second, then a death by another kill", `{"Status":"exited","ExitCode":137}`, "-1s", "", false, false, nil, "the CPU-time bound (1m30s over 2 cores, enforced by rlimits), an external kill, or the plugin's own exit 137", 0, true},
		{"memory kill logged late, before the read", `{"Status":"exited","ExitCode":137}`, "600ms", "", false, false, ErrBoundExceeded, "memory (67108864 bytes) (enforced by cgroups)", 0, true},
		{"memory kill logged after a quick read, within the floor", `{"Status":"exited","ExitCode":137}`, "100ms", "10ms", false, false, ErrBoundExceeded, "memory (67108864 bytes) (enforced by cgroups)", 0, true},
		{"memory kill logged after a quick read, past the floor", `{"Status":"exited","ExitCode":137}`, "300ms", "10ms", false, false, nil, "the CPU-time bound (1m30s over 2 cores, enforced by rlimits), an external kill, or the plugin's own exit 137", 0, true},
		{"flag alone, the log placing no kill at the death", `{"Status":"exited","ExitCode":137,"OOMKilled":true}`, "", "", false, false, nil, "the CPU-time bound (1m30s over 2 cores, enforced by rlimits), an external kill, or the plugin's own exit 137", 0, true},
		{"memory kill the plugin outlived", `{"Status":"exited","ExitCode":0,"OOMKilled":true}`, "-30s", "", false, false, nil, "", 0, false},
		{"memory kill outlived, then its own failure", `{"Status":"exited","ExitCode":7,"OOMKilled":true}`, "-30s", "", false, false, nil, "", 7, false},
		{"memory kill outlived, then the clock", "", "-30s", "", true, false, ErrBoundExceeded, "wall clock (300ms, enforced by the runner)", 0, true},
		{"event log unreadable", `{"Status":"exited","ExitCode":137}`, "events-fail", "", false, false, nil, "reading the daemon's event log for the container", 0, true},
		{"record without a finish stamp", `{"Status":"exited","ExitCode":137,"FinishedAt":"yesterday"}`, "", "", false, false, nil, "carries no finish time", 0, false},
		{"record with a zero finish stamp", `{"Status":"exited","ExitCode":137,"FinishedAt":"0001-01-01T00:00:00Z"}`, "", "", false, false, nil, "carries no finish time", 0, false},
		{"wall clock", "", "", "", true, false, ErrBoundExceeded, "wall clock (300ms, enforced by the runner)", 0, true},
		{"cancelled", "", "", "", true, true, context.Canceled, "cancelled", 0, true},
		{"status 137", `{"Status":"exited","ExitCode":137}`, "", "", false, false, nil, "the CPU-time bound (1m30s over 2 cores, enforced by rlimits), an external kill, or the plugin's own exit 137", 0, true},
		{"plugin exit", `{"Status":"exited","ExitCode":7}`, "", "", false, false, nil, "", 7, false},
		{"never ran", `{"Status":"created","ExitCode":0}`, "", "", false, false, nil, `did not run to an exit (status "created")`, 0, false},
		{"start error", `{"Status":"created","ExitCode":127,"Error":"exec: \"/nope\": no such file"}`, "", "", false, false, nil, `starting the plugin process: exec: "/nope": no such file`, 0, false},
		{"clean", `{"Status":"exited","ExitCode":0}`, "", "", false, false, nil, "", 0, false},
	}
	for _, c := range cases {
		dir := fakeDaemon(t)
		if c.state != "" {
			os.WriteFile(filepath.Join(dir, "state.json"), []byte(c.state), 0o644)
		}
		if c.now != "" {
			os.WriteFile(filepath.Join(dir, "daemon-now"), []byte(c.now), 0o644)
		}
		if c.plant == "events-fail" {
			os.WriteFile(filepath.Join(dir, c.plant), nil, 0o644)
		} else if c.plant != "" {
			// An oom event at an offset from the finish: the kill that
			// ended the plugin lands milliseconds around it; one the
			// plugin outlived lies further back.
			os.WriteFile(filepath.Join(dir, "oom-event"), []byte(c.plant), 0o644)
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
			// Cancel once the container has started, so the kill is
			// the cancellation's, not a start that never happened.
			go func() {
				for {
					if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				cancel()
			}()
		}
		r, err := NewDockerRunner("")
		if err != nil {
			t.Fatal(err)
		}
		res, err := r.Run(ctx, dockerSpec(t, exportFixture(t), limits))
		cancel()
		verbs, argv := fakeLog(t, dir)
		if c.hang && !slices.Contains(verbs, "kill") {
			t.Errorf("%s: the container was not killed: %v", c.name, verbs)
		}
		// The event log is consulted exactly where the record leaves
		// a 137 unattributed, for this container's oom events, and
		// before the container's release.
		if i := slices.Index(verbs, "events"); (i >= 0) != c.events {
			t.Errorf("%s: event log consulted %v: %v", c.name, i >= 0, verbs)
		} else if i >= 0 {
			if rm := slices.Index(verbs, "rm"); rm >= 0 && rm < i {
				t.Errorf("%s: the event log read after the release: %v", c.name, verbs)
			}
			for _, want := range []string{"container=fakecontainer", "event=oom"} {
				if !slices.Contains(argv[i], want) {
					t.Errorf("%s: events %q lacks %s", c.name, argv[i], want)
				}
			}
			if !slices.Contains(argv[i], "--until") || !slices.Contains(argv[i], "--since") {
				t.Errorf("%s: an event read without a window: %q", c.name, argv[i])
			}
			if j := slices.IndexFunc(argv, func(a []string) bool { return a[0] == "info" && slices.Contains(a, "{{.SystemTime}}") }); j < 0 || j > i {
				t.Errorf("%s: the daemon's time not read before its log: %v", c.name, verbs)
			}
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
	if _, err := Open(&docker, userconfig.Value{}); err == nil || !strings.Contains(err.Error(), "runner docker (from the --runner flag) is unavailable: docker version") {
		t.Fatalf("Open: %v", err)
	}
	os.Remove(filepath.Join(dir, "unavailable"))
	sel, err := Open(&docker, userconfig.Value{})
	if err != nil {
		t.Fatal(err)
	}
	if cands, _ := sel.Candidates(context.Background(), plugin.TierStrong); len(cands) != 1 || cands[0].Name != RunnerDocker {
		t.Fatalf("candidates = %+v", cands)
	} else if _, ok := cands[0].Runner.(*DockerRunner); !ok {
		t.Fatalf("runner = %T", cands[0].Runner)
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
	_, err = r.Run(context.Background(), Spec{Scheme: plugin.SchemeLocal, Process: plugin.Process{Argv: []string{"/usr/bin/gen"}}, Limits: trust.Limits{Memory: 64 << 20, CPU: 2, Pids: 7, Timeout: time.Minute}, MinTier: plugin.TierNone})
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
	res, err := r.Run(context.Background(), Spec{Scheme: plugin.SchemeOCI, Image: &plugin.DaemonLocal{Reference: "plugins/q:dev"}, Stdin: request(t, ""), Limits: l, MinTier: plugin.TierStrong})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != plugin.TierStrong || content(t, res) != "from the fake daemon" {
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
	// pb selects nothing of a daemon-local image, its platform
	// included (REQ-plugin-override).
	if slices.Contains(create, "--platform") {
		t.Fatalf("a daemon-local image was created for a platform: %q", create)
	}
	// A name that is no image reference, or a flag in its place, is
	// refused before the daemon is asked.
	for _, bad := range []string{"--privileged", "", "not a ref!"} {
		_, err := r.Run(context.Background(), Spec{Scheme: plugin.SchemeOCI, Image: &plugin.DaemonLocal{Reference: bad}, Limits: l, MinTier: plugin.TierStrong})
		if err == nil || !(strings.Contains(err.Error(), "does not name a daemon image") || strings.Contains(err.Error(), "names no reference")) {
			t.Errorf("image %q: %v", bad, err)
		}
	}
	// A world where the scheme has none, or none where it has one,
	// refuses; two worlds at once the type cannot spell.
	for _, spec := range []Spec{
		{Scheme: plugin.SchemeOCI, Process: plugin.Process{Argv: []string{"/p"}}, Limits: l, MinTier: plugin.TierStrong},
		{Scheme: plugin.SchemeLocal, Image: &plugin.DaemonLocal{Reference: "x"}, Process: plugin.Process{Argv: []string{"/p"}}, Limits: l, MinTier: plugin.TierNone},
		{Scheme: plugin.SchemeLocal, Image: &plugin.Pulled{Repository: "x", Digest: "sha256:" + strings.Repeat("ab", 32), Entry: "linux/amd64"}, Process: plugin.Process{Argv: []string{"/p"}}, Limits: l, MinTier: plugin.TierNone},
	} {
		if _, err := r.Run(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "has a world") && !strings.Contains(err.Error(), "world of its own") {
			t.Errorf("%+v accepted: %v", spec, err)
		}
	}
}

// A caller's cancellation on the way to the start — before the
// container exists, or before it runs — is reported as the
// cancellation, never as the step it ended.
func TestDockerCancelledBeforeStart(t *testing.T) {
	fakeDaemon(t)
	r, err := NewDockerRunner("")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = r.Run(ctx, dockerSpec(t, exportFixture(t), trust.Limits{Memory: 64 << 20, CPU: 2, Pids: 7, Timeout: time.Minute}))
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "plugin run cancelled") {
		t.Fatalf("cancelled before start: %v", err)
	}
}

// A release that fails after a clean run fails the run; one that fails
// after a refusal never masks the refusal.
func TestDockerReleaseFailure(t *testing.T) {
	l := trust.Limits{Memory: 64 << 20, CPU: 2, Pids: 7, Timeout: time.Minute}
	dir := fakeDaemon(t)
	os.WriteFile(filepath.Join(dir, "release-fails"), nil, 0o644)
	r, err := NewDockerRunner("")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), dockerSpec(t, exportFixture(t), l))
	if err == nil || !strings.Contains(err.Error(), "releasing the run's container: docker rm") {
		t.Fatalf("clean run, release failing: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "record.json"), []byte(`{"NetworkMode":"bridge"}`), 0o644)
	_, err = r.Run(context.Background(), dockerSpec(t, exportFixture(t), l))
	if err == nil || !strings.Contains(err.Error(), "does not show the sandbox boundary") || strings.Contains(err.Error(), "releasing") {
		t.Fatalf("refusal, release failing: %v", err)
	}
}

// A record the daemon cannot give is reported as that, and the run
// releases what it created.
func TestDockerRecordUnreadable(t *testing.T) {
	dir := fakeDaemon(t)
	os.WriteFile(filepath.Join(dir, "inspect-fails"), nil, 0o644)
	r, err := NewDockerRunner("")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), dockerSpec(t, exportFixture(t), trust.Limits{Memory: 64 << 20, CPU: 2, Pids: 7, Timeout: time.Minute}))
	if err == nil || !strings.Contains(err.Error(), "reading the daemon's record of the container: docker inspect") {
		t.Fatalf("unreadable record: %v", err)
	}
	verbs, _ := fakeLog(t, dir)
	if slices.Contains(verbs, "start") || !slices.Contains(verbs, "rm") || !slices.Contains(verbs, "rmi") {
		t.Fatalf("invocations = %v", verbs)
	}
}

// A pulled image: the daemon pulls the verified digest before the
// create, which runs it as a daemon image (its own configuration,
// never fetched again), and the image stays in the daemon afterwards;
// a pull the daemon refuses ends the run before any container, and an
// image to pull that names no digest is refused before the daemon is
// asked (REQ-plugin-core-verifies).
func TestDockerPullsVerifiedDigest(t *testing.T) {
	dir := fakeDaemon(t)
	r, err := NewDockerRunner("")
	if err != nil {
		t.Fatal(err)
	}
	l := trust.Limits{Memory: 64 << 20, CPU: 2, Pids: 7, Timeout: 90 * time.Second}
	repo, digest := "ghcr.io/o/p", "sha256:"+strings.Repeat("ab", 32)
	image := repo + "@" + digest
	pulled := func(entry string) *plugin.Pulled {
		return &plugin.Pulled{Repository: repo, Digest: digest, Entry: entry}
	}
	// The admitted entry's platform, variant included, is what the
	// daemon is told — not the daemon's own os/arch.
	res, err := r.Run(context.Background(), Spec{Scheme: plugin.SchemeOCI, Image: pulled("linux/arm/v6"), Stdin: request(t, ""), Limits: l, MinTier: plugin.TierStrong})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != plugin.TierStrong || content(t, res) != "from the fake daemon" {
		t.Fatalf("result = %+v", res)
	}
	verbs, argv := fakeLog(t, dir)
	if want := []string{"version", "info", "pull", "create", "inspect", "start", "inspect", "rm"}; !reflect.DeepEqual(verbs, want) {
		t.Fatalf("invocations = %v, want %v", verbs, want)
	}
	// The platform pb checked is named on the pull and the create,
	// so the daemon's own default never picks another child of the
	// verified index.
	if !reflect.DeepEqual(argv[2], []string{"pull", "--platform", "linux/arm/v6", image}) {
		t.Fatalf("pull = %q", argv[2])
	}
	create := argv[3]
	if slices.Contains(create, "--entrypoint") || create[len(create)-1] != image || create[len(create)-3] != "--pull" || create[len(create)-2] != "never" || create[len(create)-5] != "--platform" || create[len(create)-4] != "linux/arm/v6" {
		t.Fatalf("create = %q", create)
	}
	// A pulled image names its digest and its platform; an export
	// its rootfs; a daemon image its reference.
	for _, c := range []struct {
		spec Spec
		text string
	}{
		{Spec{Scheme: plugin.SchemeOCI, Image: pulled(""), Limits: l, MinTier: plugin.TierStrong}, "entry, and none is named"},
		{Spec{Scheme: plugin.SchemeOCI, Image: &plugin.Pulled{Repository: repo, Entry: "linux/arm/v6"}, Limits: l, MinTier: plugin.TierStrong}, "digest, and none is named"},
		{Spec{Scheme: plugin.SchemeOCI, Image: &plugin.Export{}, Process: plugin.Process{Argv: []string{"/p"}}, Limits: l, MinTier: plugin.TierStrong}, "names no rootfs"},
	} {
		if _, err := r.Run(context.Background(), c.spec); err == nil || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%+v: %v, want %q", c.spec, err, c.text)
		}
	}
	// The container goes with the anonymous volumes an image
	// declares; the pulled image stays.
	if !reflect.DeepEqual(argv[7], []string{"rm", "--force", "--volumes", "fakecontainer"}) {
		t.Fatalf("rm = %q", argv[7])
	}
	if _, err := r.Run(context.Background(), Spec{Scheme: plugin.SchemeOCI, Image: &plugin.Pulled{Repository: "ghcr.io/o/p", Entry: "linux/fakearch"}, Limits: l, MinTier: plugin.TierStrong}); err == nil || !strings.Contains(err.Error(), "digest, and none is named") {
		t.Fatalf("a repository with no digest to pull: %v", err)
	}
	dir = fakeDaemon(t)
	if err := os.WriteFile(filepath.Join(dir, "pull-fails"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = r.Run(context.Background(), Spec{Scheme: plugin.SchemeOCI, Image: pulled("linux/fakearch"), Stdin: request(t, ""), Limits: l, MinTier: plugin.TierStrong})
	if err == nil || !strings.Contains(err.Error(), "the daemon pulling "+image) || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("a refused pull: %v", err)
	}
	if verbs, _ := fakeLog(t, dir); slices.Contains(verbs, "create") {
		t.Fatalf("a container was created after a refused pull: %v", verbs)
	}
}

// An export's admitted entry, which the world carries whole, is not
// the daemon's to be told: the import is created for the daemon's
// own platform, the one that stamped the image.
func TestDockerImportIgnoresExportEntry(t *testing.T) {
	dir := fakeDaemon(t)
	rootfs := exportFixture(t)
	l := trust.Limits{Memory: 64 << 20, CPU: 2, Pids: 7, Timeout: 90 * time.Second}
	r, err := NewDockerRunner("")
	if err != nil {
		t.Fatal(err)
	}
	spec := dockerSpec(t, rootfs, l)
	spec.Image = &plugin.Export{Rootfs: rootfs, Entry: "linux/arm/v6"}
	if _, err := r.Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	_, argv := fakeLog(t, dir)
	create := argv[3]
	for i := 0; i+1 < len(create); i++ {
		if create[i] == "--platform" && create[i+1] != "linux/fakearch" {
			t.Fatalf("the import created for %q, not the daemon's platform", create[i+1])
		}
	}
	if slices.Contains(create, "linux/arm/v6") {
		t.Fatalf("the export's entry reached the daemon: %q", create)
	}
}
