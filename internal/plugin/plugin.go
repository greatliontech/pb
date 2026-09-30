// Package plugin is the plugin domain's root: the one home of the
// plugin-execution vocabulary shared across pb's subsystems — the
// identity schemes a generation entry lives in, the sandbox tiers a
// run reports, the platform a plugin runs on, the process a plugin
// is (plugin-execution.md). The values are wire facts — they
// appear in pb.gen.yaml keys, pb.lock plugin entries, and
// pb.trust.yaml's execution block — so every consumer names them from
// here and no two packages can drift on a spelling. The domain's
// mechanisms — local binaries, oci images, the runner, the generation
// file and the request built from a compiled build — are its
// subpackages.
package plugin

import (
	"fmt"
	"runtime"
	"strings"
)

// Platform is the platform a plugin runs on: an operating system and
// an architecture in Go's spelling. Its string form, "<os>/<arch>",
// is the lockfile's key for a local binary's pin (module-lockfile.md)
// and the spelling every report of a platform shares.
type Platform struct {
	OS   string
	Arch string
}

// String is the platform's "<os>/<arch>" spelling.
func (p Platform) String() string { return p.OS + "/" + p.Arch }

// HostPlatform is the running host's platform.
func HostPlatform() Platform {
	return Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// Identity schemes (plugin-execution.md, the identity scheme term).
// `remote` is reserved by the spec and deliberately absent: a value
// no consumer can name is a value no consumer can accept.
const (
	SchemeOCI   = "oci"
	SchemeLocal = "local"
)

// ValidScheme reports whether s names a defined identity scheme.
func ValidScheme(s string) bool {
	return s == SchemeOCI || s == SchemeLocal
}

// Sandbox tiers (plugin-execution.md, the sandbox tier term), ordered
// weakest to strongest by declaration.
const (
	TierNone    = "None"
	TierMinimal = "Minimal"
	TierOS      = "OS"
	TierStrong  = "Strong"
)

// ValidTier reports whether s names a defined sandbox tier.
func ValidTier(s string) bool {
	switch s {
	case TierNone, TierMinimal, TierOS, TierStrong:
		return true
	}
	return false
}

// TierBelow reports whether got ranks below floor in the declaration
// order. An unrecognized tier ranks with TierNone — below every other
// tier — so a comparison against any floor fails closed.
func TierBelow(got, floor string) bool {
	return tierRank(got) < tierRank(floor)
}

func tierRank(t string) int {
	switch t {
	case TierMinimal:
		return 1
	case TierOS:
		return 2
	case TierStrong:
		return 3
	}
	return 0
}

// Process is a plugin's process description as OCI image configuration
// composes it: argv (Entrypoint followed by Cmd), environment, and
// working directory ("" = the root). It is the one shape acquisition
// produces and a runner consumes, whichever identity scheme produced
// it.
type Process struct {
	Argv    []string
	Env     []string
	WorkDir string
}

// CheckEnv refuses an environment entry that is not KEY=VALUE, as
// the OCI image configuration requires: a bare name would be read
// by some substrates from the host's own environment, and a
// newline would split one entry into two. The plugin's environment
// is exactly what the image states, on every runner.
func CheckEnv(env []string) error {
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || key == "" || strings.ContainsAny(kv, "\n\x00") {
			return fmt.Errorf("environment entry %q is not KEY=VALUE", kv)
		}
	}
	return nil
}

// Image is what an acquired image is run from, one of three worlds:
// an Export, the image's root filesystem materialized; a Pulled, a
// repository at a verified digest for the daemon to pull; a
// DaemonLocal, an image the daemon holds already. A host binary has
// none. The sum is the shape: a run is in exactly one world by
// construction, never a union some check holds to one arm.
type Image interface {
	// world marks the arms; nothing outside the package is an Image.
	world()
}

// Export is an exported root filesystem — the store's shared
// export-cache entry; treat it as read-only — and the manifest-list
// entry the seam admitted for the platform, the one child of the
// verified index the export is.
type Export struct {
	Rootfs string
	// Entry is the admitted entry — os/arch, with its variant where
	// the entry states one — not the platform itself: the entry the
	// seam admitted for it.
	Entry string
}

// Pulled is an image the daemon pulls itself: the repository at the
// digest pb verified, and the manifest-list entry the seam admitted,
// which the daemon is told so it pulls and runs that child and no
// other of the verified index. The process is the image's own
// configuration, which the daemon applies.
type Pulled struct {
	Repository string
	Digest     string
	Entry      string
}

// Reference is the image as the daemon is told it: the repository at
// the digest.
func (p *Pulled) Reference() string { return p.Repository + "@" + p.Digest }

// DaemonLocal is an image the daemon holds already, run as it is: an
// override, which pb verifies nothing of. The process is the image's
// own configuration, which the daemon applies.
type DaemonLocal struct {
	Reference string
}

func (*Export) world()      {}
func (*Pulled) world()      {}
func (*DaemonLocal) world() {}

// Acquired is one plugin ready to run, whichever scheme yielded it:
// the process, and — for an image — the world it runs in, which a
// host binary leaves nil, so an absent image is never taken for an
// empty one. The pin an acquisition records is the lockfile's to
// read, not the result's to carry.
type Acquired struct {
	// Process is the process to run: argv, environment and working
	// directory. For an export it is the image config's process; zero
	// where the daemon applies the image's own configuration; for a
	// host binary, the binary's absolute path.
	Process Process
	// Image is the image's world; nil for a host binary.
	Image Image
}

// Platforms are the platforms pb is built for, in the platform term's
// spelling order (platforms.md).
var Platforms = []Platform{
	{OS: "darwin", Arch: "amd64"}, {OS: "darwin", Arch: "arm64"},
	{OS: "linux", Arch: "amd64"}, {OS: "linux", Arch: "arm64"},
	{OS: "windows", Arch: "amd64"}, {OS: "windows", Arch: "arm64"},
}

// ParsePlatform reads a platform's `<os>/<arch>` spelling, refusing
// one pb is not built for.
func ParsePlatform(s string) (Platform, error) {
	for _, p := range Platforms {
		if p.String() == s {
			return p, nil
		}
	}
	return Platform{}, fmt.Errorf("%q names no platform pb builds for (platforms.md: %s)", s, spelledPlatforms())
}

func spelledPlatforms() string {
	names := make([]string, len(Platforms))
	for i, p := range Platforms {
		names[i] = p.String()
	}
	return strings.Join(names, ", ")
}
