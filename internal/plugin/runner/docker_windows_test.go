package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/provenance/trust"
)

// On windows the docker runner refuses the store byte path before
// touching a daemon: an export here carries no file modes, so the
// daemon's pull is the one byte path (platforms.md
// REQ-plat-oci-substrate, REQ-plugin-core-verifies).
func TestDockerRefusesExportStream(t *testing.T) {
	r := &DockerRunner{CLI: "no-such-docker"}
	_, err := r.Run(context.Background(), Spec{Scheme: plugin.SchemeOCI, Image: &plugin.Export{Rootfs: t.TempDir(), Entry: "linux/amd64"}, Process: plugin.Process{Argv: []string{"/plugin"}}, Limits: trust.Limits{Memory: 1 << 20, CPU: 1, Pids: 1, Timeout: 1}, MinTier: plugin.TierOS})
	if err == nil || !strings.Contains(err.Error(), "carries no file modes on this platform") || !strings.Contains(err.Error(), "plugin-pull docker") {
		t.Fatalf("an export on windows: %v", err)
	}
}
