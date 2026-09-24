package runner

import (
	"strings"
	"testing"
	"time"

	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/provenance/trust"
)

// The spec check refuses, where the spec is built, every world whose
// facts are incomplete — an export with no rootfs, a pulled image
// with no repository, no digest or no entry, a daemon image with no
// reference, a typed nil in any arm's place — an oci run with no
// world, and a local run with one; each complete world stands.
func TestCheckSpecArms(t *testing.T) {
	l := trust.Limits{Memory: 64 << 20, CPU: 2, Pids: 7, Timeout: 90 * time.Second}
	argv := plugin.Process{Argv: []string{"/p"}}
	digest := "sha256:" + strings.Repeat("ab", 32)
	for _, c := range []struct {
		name string
		spec Spec
		want string
	}{
		{"export without rootfs", Spec{Scheme: plugin.SchemeOCI, Image: &plugin.Export{}, Process: argv, Limits: l, MinTier: plugin.TierStrong}, "names no rootfs"},
		{"export typed nil", Spec{Scheme: plugin.SchemeOCI, Image: (*plugin.Export)(nil), Process: argv, Limits: l, MinTier: plugin.TierStrong}, "names no rootfs"},
		{"pulled without repository", Spec{Scheme: plugin.SchemeOCI, Image: &plugin.Pulled{Digest: digest, Entry: "linux/amd64"}, Limits: l, MinTier: plugin.TierStrong}, "digest, and none is named"},
		{"pulled without digest", Spec{Scheme: plugin.SchemeOCI, Image: &plugin.Pulled{Repository: "r", Entry: "linux/amd64"}, Limits: l, MinTier: plugin.TierStrong}, "digest, and none is named"},
		{"pulled without entry", Spec{Scheme: plugin.SchemeOCI, Image: &plugin.Pulled{Repository: "r", Digest: digest}, Limits: l, MinTier: plugin.TierStrong}, "entry, and none is named"},
		{"pulled typed nil", Spec{Scheme: plugin.SchemeOCI, Image: (*plugin.Pulled)(nil), Limits: l, MinTier: plugin.TierStrong}, "digest, and none is named"},
		{"daemon image without reference", Spec{Scheme: plugin.SchemeOCI, Image: &plugin.DaemonLocal{}, Limits: l, MinTier: plugin.TierStrong}, "names no reference"},
		{"daemon image typed nil", Spec{Scheme: plugin.SchemeOCI, Image: (*plugin.DaemonLocal)(nil), Limits: l, MinTier: plugin.TierStrong}, "names no reference"},
		{"oci without a world", Spec{Scheme: plugin.SchemeOCI, Process: argv, Limits: l, MinTier: plugin.TierStrong}, "has a world"},
		{"local with a world", Spec{Scheme: plugin.SchemeLocal, Image: &plugin.Export{Rootfs: "/r"}, Process: argv, Limits: l, MinTier: plugin.TierNone}, "world of its own"},
		{"typed nil without argv", Spec{Scheme: plugin.SchemeOCI, Image: (*plugin.DaemonLocal)(nil), Limits: l, MinTier: plugin.TierStrong}, "names no reference"},
	} {
		if err := CheckSpec(c.spec); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	for _, c := range []struct {
		name string
		spec Spec
	}{
		{"export", Spec{Scheme: plugin.SchemeOCI, Image: &plugin.Export{Rootfs: "/r"}, Process: argv, Limits: l, MinTier: plugin.TierStrong}},
		{"export with its entry", Spec{Scheme: plugin.SchemeOCI, Image: &plugin.Export{Rootfs: "/r", Entry: "linux/arm/v6"}, Process: argv, Limits: l, MinTier: plugin.TierStrong}},
		{"pulled, the daemon supplying the process", Spec{Scheme: plugin.SchemeOCI, Image: &plugin.Pulled{Repository: "r", Digest: digest, Entry: "linux/amd64"}, Limits: l, MinTier: plugin.TierStrong}},
		{"daemon image, the daemon supplying the process", Spec{Scheme: plugin.SchemeOCI, Image: &plugin.DaemonLocal{Reference: "plugins/q:dev"}, Limits: l, MinTier: plugin.TierStrong}},
		{"local", Spec{Scheme: plugin.SchemeLocal, Process: argv, Limits: l, MinTier: plugin.TierNone}},
	} {
		if err := CheckSpec(c.spec); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
