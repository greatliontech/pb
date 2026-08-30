// Package plugexec is the one home of plugin-execution vocabulary
// shared across pb's subsystems: the identity schemes a generation
// entry lives in and the sandbox tiers a run reports
// (plugin-execution.md). The values are wire facts — they appear in
// pb.gen.yaml keys, pb.lock plugin entries, and pb.trust.yaml's
// execution block — so every consumer names them from here and no two
// packages can drift on a spelling.
package plugexec

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
