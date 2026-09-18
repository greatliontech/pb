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

// The tier order is total over the declared tiers, and an unrecognized
// tier ranks with TierNone so any floor above None refuses it.
func TestTierBelow(t *testing.T) {
	order := []string{TierNone, TierMinimal, TierOS, TierStrong}
	for i, lo := range order {
		for j, hi := range order {
			if got := TierBelow(lo, hi); got != (i < j) {
				t.Errorf("TierBelow(%s, %s) = %v", lo, hi, got)
			}
		}
	}
	if !TierBelow("VM", TierMinimal) || !TierBelow("", TierStrong) {
		t.Error("unrecognized tier did not rank below a raised floor")
	}
	if TierBelow("VM", TierNone) {
		t.Error("unrecognized tier ranked below TierNone")
	}
}
