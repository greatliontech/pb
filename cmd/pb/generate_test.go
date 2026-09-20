package main

import (
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/userconfig"
)

// generate selects its runner from the user configuration file
// beneath the environment, and a refusal names the layer the runner
// came from (REQ-plugin-runner-selection, REQ-uc-precedence).
func TestGenerateSelectsFromTheFile(t *testing.T) {
	p := plant(t, "runner: podman\n")
	run := func() error {
		cmd := rootCmd()
		cmd.SetArgs([]string{"generate"})
		return cmd.Execute()
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), `"podman" from the user configuration file `+p+` names no runner`) {
		t.Fatalf("the file's runner: %v", err)
	}
	t.Setenv(userconfig.Keys[userconfig.KeyRunner].Env, "docker")
	if err := run(); err == nil || !strings.Contains(err.Error(), "runner docker (from the PBRUNNER environment variable) is unavailable") {
		t.Fatalf("the environment's runner over the file's: %v", err)
	}
}
