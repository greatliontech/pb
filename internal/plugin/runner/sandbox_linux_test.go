//go:build linux

package runner

import (
	"errors"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/plugin/runner/testdata/behavior"
	"github.com/greatliontech/sandbox"
)

// requireOSRow skips the OS row's arm where the host reaches another
// row — the sandbox selects its highest row, and no floor caps it —
// unless PB_TEST_REQUIRE_OS_ROW demands it: a host meant to reach the
// OS row, continuous integration with user namespaces closed again,
// fails instead.
func requireOSRow(t *testing.T) {
	t.Helper()
	requireRow(t, sandbox.OS, "PB_TEST_REQUIRE_OS_ROW")
}

// On the OS row — a host without user namespaces, the floor lowered
// to os — the plugin runs from the export at its host path with no
// hostname stated: the request reaches stdin and the response comes
// back, the run reports the os tier and an accounting, the root is
// not writable, the network is denied, and the Strong floor is
// refused naming the row reached (REQ-plugin-sandboxed,
// REQ-plugin-min-tier, REQ-plugin-reported-tier).
func TestRunOnOSRow(t *testing.T) {
	requireOSRow(t)
	res, err := runAt(t, "", limits(nil), plugin.TierOS)
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != plugin.TierOS || res.ExitCode != 0 {
		t.Fatalf("result = tier %s, exit %d", res.Tier, res.ExitCode)
	}
	if res.Bounds != BoundsCgroups && res.Bounds != BoundsRlimits {
		t.Fatalf("bounds = %q", res.Bounds)
	}
	if got := content(t, res); got != "files=2" {
		t.Fatalf("plugin saw %q", got)
	}
	res, err = runAt(t, behavior.Write, limits(nil), plugin.TierOS)
	if err != nil {
		t.Fatal(err)
	}
	if got := content(t, res); got != "write-err=true" {
		t.Fatalf("the world writable from the OS row: %q", got)
	}
	res, err = runAt(t, behavior.Net, limits(nil), plugin.TierOS)
	if err != nil {
		t.Fatal(err)
	}
	if got := content(t, res); !strings.HasSuffix(got, "dial-err=true") {
		t.Fatalf("network reachable from the OS row: %q", got)
	}
	_, err = run(t, "", limits(nil))
	var te *sandbox.TierError
	if !errors.Is(err, ErrTierUnreachable) || !errors.As(err, &te) || te.Reached != sandbox.OS {
		t.Fatalf("the Strong floor on an OS host: %v", err)
	}
}
