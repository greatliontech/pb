package runner

import (
	"fmt"

	"github.com/greatliontech/pb/internal/userconfig"
)

// Runner names, and the flag that selects one
// (plugin-execution.md, REQ-plugin-runner-selection).
const (
	RunnerNative = "native"
	RunnerDocker = "docker"

	FlagRunner = "runner" // the --runner flag of generate
)

// The flag layer as a refusal names it; the other layers name
// themselves (userconfig.Value.From, userconfig.FromDefault).
const fromFlag = "the --" + FlagRunner + " flag"

// selection is the runner a generation chose and the layer that
// chose it, so a refusal names both; the platform default carries
// the native runner it found, or why it found none.
type selection struct {
	name   string
	from   string
	runner Runner // the native runner, where the default found it
	native error  // the native runner's unavailability, where the default turned on it
}

// nativeRunner is the capability the platform default turns on;
// tests point it elsewhere to exercise both defaults on one host.
var nativeRunner = NativeRunner

// Open applies REQ-plugin-runner-selection and returns the runner: the
// flag, where given (nil is not given; an empty string is given and
// names no runner), over the runner setting where the environment or
// the user configuration file stated it (user-config.md), over the
// platform default — native where pb's sandbox is available, docker
// otherwise. A layer that names no runner refuses naming the layer
// and the runners; a runner that is unavailable refuses naming it and
// its layer; nothing is ever substituted.
func Open(flag *string, setting userconfig.Value) (Runner, error) {
	sel := platformDefault()
	switch {
	case flag != nil:
		sel = selection{name: *flag, from: fromFlag}
	case setting.Stated():
		sel = selection{name: setting.Value, from: setting.From}
	}
	switch sel.name {
	case RunnerNative:
		if sel.runner != nil {
			return sel.runner, nil
		}
		r, err := nativeRunner()
		if err != nil {
			return nil, fmt.Errorf("runner: runner %s (from %s) is unavailable: %w", sel.name, sel.from, err)
		}
		return r, nil
	case RunnerDocker:
		r, err := NewDockerRunner("")
		if err != nil {
			if sel.native != nil {
				return nil, fmt.Errorf("runner: runner %s (from %s, the native runner being unavailable: %v) is unavailable: %w", sel.name, sel.from, sel.native, err)
			}
			return nil, fmt.Errorf("runner: runner %s (from %s) is unavailable: %w", sel.name, sel.from, err)
		}
		return r, nil
	}
	return nil, fmt.Errorf("runner: %q from %s names no runner (runners: %s, %s)", sel.name, sel.from, RunnerNative, RunnerDocker)
}

func platformDefault() selection {
	r, err := nativeRunner()
	if err != nil {
		return selection{name: RunnerDocker, from: userconfig.FromDefault, native: err}
	}
	return selection{name: RunnerNative, from: userconfig.FromDefault, runner: r}
}
