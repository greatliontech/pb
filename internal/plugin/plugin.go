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
