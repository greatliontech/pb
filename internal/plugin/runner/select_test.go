package runner

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/userconfig"
)

// withNative pins the native runner's availability for one test.
func withNative(t *testing.T, r Runner, err error) {
	t.Helper()
	prev := nativeRunner
	nativeRunner = func() (Runner, error) { return r, err }
	t.Cleanup(func() { nativeRunner = prev })
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

// fakeRunner stands in for the native runner in selection tests on
// every platform.
type fakeRunner struct{}

func (fakeRunner) Run(context.Context, Spec) (*Result, error) { return nil, errors.New("fake") }
func (fakeRunner) Platform() (string, string)                 { return "linux", "fake" }

// The flag, where given, wins over the runner setting, where stated
// (by the environment or the user configuration file, each named as
// it stated it), over the platform default; the default is native
// where the sandbox is available and docker otherwise; a layer naming
// no runner — the flag given empty, or the file's key stated empty,
// included — refuses naming the layer and the runners; a runner that
// is unavailable refuses naming it and its layer, the native runner's
// reason carried where the default turned on it; nothing is
// substituted.
func TestOpenSelects(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no docker here
	native := fakeRunner{}
	str := func(s string) *string { return &s }
	env := func(v string) userconfig.Value {
		return userconfig.Value{Value: v, From: "the PBRUNNER environment variable"}
	}
	file := func(v string) userconfig.Value {
		return userconfig.Value{Value: v, From: "the user configuration file /home/u/.config/pb/config.yaml"}
	}
	var unstated userconfig.Value
	cases := []struct {
		name    string
		native  error
		flag    *string
		setting userconfig.Value
		want    Runner
		text    string
	}{
		{"default native", nil, nil, unstated, native, ""},
		{"default docker", errors.New("no sandbox here"), nil, unstated, nil, "runner docker (from the platform default, the native runner being unavailable: no sandbox here) is unavailable: docker version"},
		{"flag over setting", nil, str("native"), env("docker"), native, ""},
		{"flag docker", nil, str("docker"), unstated, nil, "runner docker (from the --runner flag) is unavailable: docker version"},
		{"env docker", nil, nil, env("docker"), nil, "runner docker (from the PBRUNNER environment variable) is unavailable"},
		{"file docker", nil, nil, file("docker"), nil, "runner docker (from the user configuration file /home/u/.config/pb/config.yaml) is unavailable"},
		{"file native", nil, nil, file("native"), native, ""},
		{"file unknown", nil, nil, file("podman"), nil, `"podman" from the user configuration file /home/u/.config/pb/config.yaml names no runner`},
		{"file stated empty", nil, nil, file(""), nil, `"" from the user configuration file /home/u/.config/pb/config.yaml names no runner`},
		{"env native", errors.New("no sandbox here"), nil, env("native"), nil, "runner native (from the PBRUNNER environment variable) is unavailable: no sandbox here"},
		{"flag given empty", nil, str(""), env("native"), nil, `"" from the --runner flag names no runner (runners: native, docker)`},
		{"flag unknown", nil, str("podman"), unstated, nil, `"podman" from the --runner flag names no runner`},
		{"env unknown", nil, nil, env("Native"), nil, `"Native" from the PBRUNNER environment variable names no runner`},
		{"env untrimmed", nil, nil, env(" native"), nil, "names no runner"},
	}
	for _, c := range cases {
		withNative(t, native, c.native)
		got, err := Open(c.flag, c.setting)
		if c.text == "" {
			if err != nil || got != c.want {
				t.Errorf("%s: %v, %v", c.name, got, err)
			}
			continue
		}
		if err == nil || got != nil || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%s: %v, %v; want %q", c.name, got, err, c.text)
		}
	}
}

// Without injection the default is wired to the real native runner:
// on Linux an unadorned Open yields the sandbox runner.
func TestOpenDefaultIsWired(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no docker here
	r, err := Open(nil, userconfig.Value{})
	if runtime.GOOS != "linux" {
		if err == nil || !strings.Contains(err.Error(), "the native runner being unavailable") {
			t.Fatalf("off linux: %v", err)
		}
		return
	}
	if err != nil || r == nil {
		t.Fatalf("default: %v %v", r, err)
	}
	if os_, arch := r.Platform(); os_ != runtime.GOOS || arch != runtime.GOARCH {
		t.Fatalf("the default runner's platform = %s/%s, not the host's", os_, arch)
	}
}
