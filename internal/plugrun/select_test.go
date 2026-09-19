package plugrun

import (
	"errors"
	"runtime"
	"strings"
	"testing"
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
	if FlagRunner != "runner" || EnvRunner != "PBRUNNER" || RunnerNative != "native" || RunnerDocker != "docker" {
		t.Fatalf("spellings: %q %q %q %q", FlagRunner, EnvRunner, RunnerNative, RunnerDocker)
	}
	if layerFlag.String() != "the --runner flag" || layerEnvironment.String() != "the PBRUNNER environment variable" || layerDefault.String() != "the platform default" {
		t.Fatalf("layer names: %q %q %q", layerFlag, layerEnvironment, layerDefault)
	}
}

// The flag, where given, wins over the environment, where set, over
// the platform default; the default is native where the sandbox is
// available and docker otherwise; a layer naming no runner — the
// flag given empty included — refuses naming the layer and the
// runners; a runner that is unavailable refuses naming it and its
// layer, the native runner's reason carried where the default turned
// on it; nothing is substituted.
func TestOpenSelects(t *testing.T) {
	native := &SandboxRunner{}
	str := func(s string) *string { return &s }
	cases := []struct {
		name   string
		native error
		flag   *string
		env    string
		want   Runner
		text   string
	}{
		{"default native", nil, nil, "", native, ""},
		{"default docker", errors.New("no sandbox here"), nil, "", nil, "runner docker (from the platform default, the native runner being unavailable: no sandbox here) is not implemented"},
		{"flag over env", nil, str("native"), "docker", native, ""},
		{"flag docker", nil, str("docker"), "", nil, "runner docker (from the --runner flag) is not implemented"},
		{"env docker", nil, nil, "docker", nil, "runner docker (from the PBRUNNER environment variable) is not implemented"},
		{"env native", errors.New("no sandbox here"), nil, "native", nil, "runner native (from the PBRUNNER environment variable) is unavailable: no sandbox here"},
		{"flag given empty", nil, str(""), "native", nil, `"" from the --runner flag names no runner (runners: native, docker)`},
		{"flag unknown", nil, str("podman"), "", nil, `"podman" from the --runner flag names no runner`},
		{"env unknown", nil, nil, "Native", nil, `"Native" from the PBRUNNER environment variable names no runner`},
		{"env untrimmed", nil, nil, " native", nil, "names no runner"},
	}
	for _, c := range cases {
		withNative(t, native, c.native)
		got, err := Open(c.flag, c.env)
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
	r, err := Open(nil, "")
	if runtime.GOOS != "linux" {
		if err == nil || !strings.Contains(err.Error(), "the native runner being unavailable") {
			t.Fatalf("off linux: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*SandboxRunner); !ok {
		t.Fatalf("default runner = %T", r)
	}
}
