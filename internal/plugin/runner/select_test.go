package runner

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/userconfig"
)

// withRunners pins the native and docker runners' availability for
// one test.
func withRunners(t *testing.T, native Native, nativeErr error, docker Runner, dockerErr error) {
	t.Helper()
	prevNative, prevDocker := nativeRunner, dockerRunner
	nativeRunner = func() (Native, error) { return native, nativeErr }
	dockerRunner = func() (Runner, error) { return docker, dockerErr }
	t.Cleanup(func() { nativeRunner, dockerRunner = prevNative, prevDocker })
}

// The contract spellings are the requirement's.
func TestRunnerSpellings(t *testing.T) {
	if FlagRunner != "runner" || RunnerNative != "native" || RunnerDocker != "docker" {
		t.Fatalf("spellings: %q %q %q", FlagRunner, RunnerNative, RunnerDocker)
	}
	if fromFlag != "the --runner flag" || userconfig.FromDefault != "the platform default" {
		t.Fatalf("layer names: %q %q", fromFlag, userconfig.FromDefault)
	}
}

// fakeRunner stands in for a runner in selection tests on every
// platform: the native one reaches the tier it is given.
type fakeRunner struct {
	name  string
	tier  string
	reach error
}

func (f fakeRunner) Run(context.Context, Spec) (*Result, error) { return nil, errors.New("fake") }
func (f fakeRunner) Platform() plugin.Platform                  { return plugin.Platform{OS: "linux", Arch: f.name} }
func (f fakeRunner) Reach(context.Context) (string, error)      { return f.tier, f.reach }

// fakeDaemonRunner is a fake that runs daemon images.
type fakeDaemonRunner struct{ fakeRunner }

func (fakeDaemonRunner) RunsDaemonImages() {}

// The flag, where given, wins over the runner setting, where stated
// (by the environment or the user configuration file, each named as
// it stated it), over the platform default; a layer naming no runner
// — the flag given empty, or the file's key stated empty, included —
// refuses naming the layer and the runners; a named runner that is
// unavailable refuses naming it and its layer; a named runner binds
// every entry: it is the one candidate, whatever the floor; nothing
// is substituted.
func TestOpenSelects(t *testing.T) {
	native, docker := fakeRunner{name: "native", tier: plugin.TierStrong}, fakeDaemonRunner{fakeRunner{name: "docker"}}
	str := func(s string) *string { return &s }
	env := func(v string) userconfig.Value {
		return userconfig.Value{Value: v, From: "the PBRUNNER environment variable"}
	}
	file := func(v string) userconfig.Value {
		return userconfig.Value{Value: v, From: "the user configuration file /home/u/.config/pb/config.yaml"}
	}
	var unstated userconfig.Value
	noDaemon := errors.New("docker version: no daemon")
	cases := []struct {
		name      string
		nativeErr error
		dockerErr error
		flag      *string
		setting   userconfig.Value
		want      string // the one candidate's name
		text      string
	}{
		{"flag over setting", nil, nil, str("native"), env("docker"), "native", ""},
		{"flag docker", nil, noDaemon, str("docker"), unstated, "", "runner docker (from the --runner flag) is unavailable: docker version"},
		{"flag docker reachable", nil, nil, str("docker"), unstated, "docker", ""},
		{"env docker", nil, noDaemon, nil, env("docker"), "", "runner docker (from the PBRUNNER environment variable) is unavailable"},
		{"file docker", nil, noDaemon, nil, file("docker"), "", "runner docker (from the user configuration file /home/u/.config/pb/config.yaml) is unavailable"},
		{"file native", nil, nil, nil, file("native"), "native", ""},
		{"file unknown", nil, nil, nil, file("podman"), "", `"podman" from the user configuration file /home/u/.config/pb/config.yaml names no runner`},
		{"file stated empty", nil, nil, nil, file(""), "", `"" from the user configuration file /home/u/.config/pb/config.yaml names no runner`},
		{"env native", errors.New("no sandbox here"), nil, nil, env("native"), "", "runner native (from the PBRUNNER environment variable) is unavailable: no sandbox here"},
		{"flag given empty", nil, nil, str(""), env("native"), "", `"" from the --runner flag names no runner (runners: native, docker)`},
		{"flag unknown", nil, nil, str("podman"), unstated, "", `"podman" from the --runner flag names no runner`},
		{"env unknown", nil, nil, nil, env("Native"), "", `"Native" from the PBRUNNER environment variable names no runner`},
		{"env untrimmed", nil, nil, nil, env(" native"), "", "names no runner"},
	}
	for _, c := range cases {
		withRunners(t, native, c.nativeErr, docker, c.dockerErr)
		sel, err := Open(c.flag, c.setting)
		if c.text != "" {
			if err == nil || sel != nil || !strings.Contains(err.Error(), c.text) {
				t.Errorf("%s: %v, %v; want %q", c.name, sel, err, c.text)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		// A named runner is the one candidate under any floor, the
		// native one's row unasked.
		for _, floor := range []string{plugin.TierStrong, plugin.TierOS} {
			cands, absent := sel.Candidates(context.Background(), floor)
			if len(cands) != 1 || cands[0].Name != c.want || !strings.HasSuffix(absent, " names runner "+c.want+" for every entry") {
				t.Errorf("%s under %s: candidates %+v (absent %q), want %s alone, its layer named", c.name, floor, cands, absent, c.want)
			}
		}
		if daemon := sel.RunsDaemonImages(); daemon != (c.want == "docker") {
			t.Errorf("%s: runs daemon images = %v", c.name, daemon)
		}
	}
}

// The platform default chooses per entry by capability: the native
// runner first where its row reaches the floor, the docker runner
// where a daemon is reachable; its account states the row, the
// floor and the daemon's reachability whichever way they fell, so
// an entry no candidate serves is refused naming them; the default
// refuses nothing at Open.
func TestDefaultCandidates(t *testing.T) {
	docker := fakeDaemonRunner{fakeRunner{name: "docker"}}
	noDaemon := errors.New("docker version: no daemon")
	noSandbox := errors.New("the native runner reaches no sandbox row on plan9/mips")
	for _, c := range []struct {
		name      string
		native    Native
		nativeErr error
		dockerErr error
		floor     string
		want      []string
		absent    string
	}{
		{"both, the row meeting the floor", fakeRunner{name: "native", tier: plugin.TierStrong}, nil, nil, plugin.TierStrong, []string{"native", "docker"}, "the floor Strong; the native runner's row reaches tier Strong, meeting it; a daemon is reachable for the docker runner"},
		{"the row below the floor", fakeRunner{name: "native", tier: plugin.TierOS}, nil, nil, plugin.TierStrong, []string{"docker"}, "the floor Strong; the native runner's row reaches tier OS, below it; a daemon is reachable for the docker runner"},
		{"the floor lowered to the row", fakeRunner{name: "native", tier: plugin.TierOS}, nil, nil, plugin.TierOS, []string{"native", "docker"}, "the floor OS; the native runner's row reaches tier OS, meeting it; a daemon is reachable for the docker runner"},
		{"no daemon", fakeRunner{name: "native", tier: plugin.TierStrong}, nil, noDaemon, plugin.TierStrong, []string{"native"}, "the floor Strong; the native runner's row reaches tier Strong, meeting it; no daemon is reachable for the docker runner (docker version: no daemon)"},
		{"no native runner", nil, noSandbox, nil, plugin.TierStrong, []string{"docker"}, "the floor Strong; the native runner is unavailable (the native runner reaches no sandbox row on plan9/mips); a daemon is reachable for the docker runner"},
		{"neither", fakeRunner{name: "native", tier: plugin.TierMinimal}, nil, noDaemon, plugin.TierOS, nil, "the floor OS; the native runner's row reaches tier Minimal, below it; no daemon is reachable for the docker runner (docker version: no daemon)"},
		{"the row unreadable", fakeRunner{name: "native", reach: errors.New("probe failed")}, nil, nil, plugin.TierStrong, []string{"docker"}, "the floor Strong; the native runner reaches no row (probe failed); a daemon is reachable for the docker runner"},
		{"no floor, the row whatever it is", fakeRunner{name: "native", tier: plugin.TierMinimal}, nil, noDaemon, plugin.TierNone, []string{"native"}, "the native runner's row reaches tier Minimal; no daemon is reachable for the docker runner (docker version: no daemon)"},
	} {
		withRunners(t, c.native, c.nativeErr, docker, c.dockerErr)
		sel, err := Open(nil, userconfig.Value{})
		if err != nil {
			t.Fatalf("%s: the default refused at Open: %v", c.name, err)
		}
		cands, account := sel.Candidates(context.Background(), c.floor)
		var names []string
		for _, cand := range cands {
			names = append(names, cand.Name)
		}
		if strings.Join(names, ",") != strings.Join(c.want, ",") || account != c.absent {
			t.Errorf("%s: candidates %v (account %q), want %v (account %q)", c.name, names, account, c.want, c.absent)
		}
		if daemon := sel.RunsDaemonImages(); daemon != (c.dockerErr == nil) {
			t.Errorf("%s: runs daemon images = %v", c.name, daemon)
		}
	}
}

// Without injection the default is wired to the real runners: where
// the platform has a sandbox row an unadorned Open offers the sandbox
// runner first, whose platform is the host's; where it has none the
// native runner is absent and says so.
func TestOpenDefaultIsWired(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no docker here
	sel, err := Open(nil, userconfig.Value{})
	if err != nil {
		t.Fatal(err)
	}
	cands, account := sel.Candidates(context.Background(), plugin.TierMinimal)
	if _, err := NativeRunner(); err != nil {
		if len(cands) != 0 || !strings.Contains(account, "the native runner is unavailable") || !strings.Contains(account, "no daemon is reachable") {
			t.Fatalf("no sandbox row here: %+v %q", cands, account)
		}
		return
	}
	if len(cands) != 1 || cands[0].Name != RunnerNative || !strings.Contains(account, "no daemon is reachable") {
		t.Fatalf("default: %+v %q", cands, account)
	}
	if p := cands[0].Runner.Platform(); p.OS != runtime.GOOS || p.Arch != runtime.GOARCH {
		t.Fatalf("the default runner's platform = %s, not the host's", p)
	}
}
