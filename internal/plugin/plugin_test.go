package plugin

import (
	"runtime"
	"strings"
	"testing"
)

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

// An environment entry that is not KEY=VALUE is refused: a bare name
// or a newline would mean different things on different substrates.
func TestCheckEnv(t *testing.T) {
	if err := CheckEnv([]string{"A=1", "B=", "C=x=y"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"SECRET", "=1", "A=1\nB=2", "A=\x00"} {
		if err := CheckEnv([]string{"A=1", bad}); err == nil || !strings.Contains(err.Error(), "not KEY=VALUE") {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}

// A platform spells itself "<os>/<arch>", the lockfile's key, and the
// host's is Go's own.
func TestPlatformSpelling(t *testing.T) {
	if got := (Platform{OS: "linux", Arch: "amd64"}).String(); got != "linux/amd64" {
		t.Fatalf("spelling = %q", got)
	}
	if h := HostPlatform(); h.OS != runtime.GOOS || h.Arch != runtime.GOARCH || h.String() != runtime.GOOS+"/"+runtime.GOARCH {
		t.Fatalf("host = %+v", h)
	}
}
