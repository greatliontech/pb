package plugrun

import (
	"fmt"
)

// Runner names, and the spellings of the layers that select one
// (plugin-execution.md, REQ-plugin-runner-selection).
const (
	RunnerNative = "native"
	RunnerDocker = "docker"

	FlagRunner = "runner"   // the --runner flag of generate
	EnvRunner  = "PBRUNNER" // the environment variable
)

// layer is where a runner name came from, in precedence order.
type layer int

const (
	layerFlag layer = iota
	layerEnvironment
	layerDefault
)

func (l layer) String() string {
	switch l {
	case layerFlag:
		return "the --" + FlagRunner + " flag"
	case layerEnvironment:
		return "the " + EnvRunner + " environment variable"
	}
	return "the platform default"
}

// selection is the runner a generation chose and the layer that
// chose it, so a refusal names both; the platform default carries
// the native runner it found, or why it found none.
type selection struct {
	name   string
	from   layer
	runner Runner // the native runner, where the default found it
	native error  // the native runner's unavailability, where the default turned on it
}

// nativeRunner is the capability the platform default turns on;
// tests point it elsewhere to exercise both defaults on one host.
var nativeRunner = NativeRunner

// Open applies REQ-plugin-runner-selection and returns the runner: the
// flag, where given (nil is not given; an empty string is given and
// names no runner), over the environment, where non-empty, over the
// platform default — native where pb's sandbox is available, docker
// otherwise. A layer that names no runner refuses naming the layer
// and the runners; a runner that is unavailable refuses naming it
// and its layer; nothing is ever substituted. The user-configuration
// layer the requirement ranks between the environment and the
// default has no home yet (docs/issues/runner-user-configuration.md).
func Open(flag *string, env string) (Runner, error) {
	sel := platformDefault()
	switch {
	case flag != nil:
		sel = selection{name: *flag, from: layerFlag}
	case env != "":
		sel = selection{name: env, from: layerEnvironment}
	}
	switch sel.name {
	case RunnerNative:
		if sel.runner != nil {
			return sel.runner, nil
		}
		r, err := nativeRunner()
		if err != nil {
			return nil, fmt.Errorf("plugrun: runner %s (from %s) is unavailable: %w", sel.name, sel.from, err)
		}
		return r, nil
	case RunnerDocker:
		r, err := NewDockerRunner("")
		if err != nil {
			if sel.native != nil {
				return nil, fmt.Errorf("plugrun: runner %s (from %s, the native runner being unavailable: %v) is unavailable: %w", sel.name, sel.from, sel.native, err)
			}
			return nil, fmt.Errorf("plugrun: runner %s (from %s) is unavailable: %w", sel.name, sel.from, err)
		}
		return r, nil
	}
	return nil, fmt.Errorf("plugrun: %q from %s names no runner (runners: %s, %s)", sel.name, sel.from, RunnerNative, RunnerDocker)
}

func platformDefault() selection {
	r, err := nativeRunner()
	if err != nil {
		return selection{name: RunnerDocker, from: layerDefault, native: err}
	}
	return selection{name: RunnerNative, from: layerDefault, runner: r}
}
