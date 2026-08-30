package plugexec

import "testing"

// The vocabulary is closed: exactly the defined schemes and tiers
// validate; the reserved `remote` scheme and every other spelling do
// not.
func TestVocabularyClosed(t *testing.T) {
	for _, s := range []string{SchemeOCI, SchemeLocal} {
		if !ValidScheme(s) {
			t.Errorf("scheme %q rejected", s)
		}
	}
	for _, s := range []string{"", "remote", "OCI", "Local", "oci "} {
		if ValidScheme(s) {
			t.Errorf("scheme %q accepted", s)
		}
	}
	for _, tier := range []string{TierNone, TierMinimal, TierOS, TierStrong} {
		if !ValidTier(tier) {
			t.Errorf("tier %q rejected", tier)
		}
	}
	for _, s := range []string{"", "strong", "none", "Os", "VM"} {
		if ValidTier(s) {
			t.Errorf("tier %q accepted", s)
		}
	}
}
