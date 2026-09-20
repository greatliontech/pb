package version

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/semver"
	"pgregory.net/rapid"
)

func mustParse(t testing.TB, s string) Version {
	t.Helper()
	v, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return v
}

func TestParseGolden(t *testing.T) {
	accept := []string{
		"v0.0.0",
		"v1.2.3",
		"v10.20.30",
		"v1.0.0-rc.1",
		"v1.0.0-alpha-2.x.0",
		"v1.2.3-za",
		"v1.2.3-Zebra9.AZ",
		"v0.0.0-20260808115925-abcdef123456",
		"v1.2.4-0.20260808115925-abcdef123456",
		"v1.2.3-rc.1.0.20260808115925-abcdef123456",
		"v1234567890123456.0.0",
	}
	for _, s := range accept {
		v := mustParse(t, s)
		if v.String() != s {
			t.Errorf("Parse(%q).String() = %q", s, v.String())
		}
	}
	reject := []string{
		"",
		"1.2.3",
		"v1.2",
		"v1.2.3.4",
		"v01.2.3",
		"v1.02.3",
		"v1.2.03",
		"v1.2.3-",
		"v1.2.3-rc..1",
		"v1.2.3-rc.01",
		"v1.2.3-rc_1",
		"v1.2.3+meta",
		"v1.2.3-rc.1+meta",
		"v-1.2.3",
		"v1.2.3 ",
		"V1.2.3",
		"v1..3",
		"v.1.2",
		"v1.2.",
		"va.0.0",
		"v1:2.0.0",
		"v1.2.3-r@c",
		"v1.2.3-r`c",
		"v1.2.3-r{c",
		"v1.2.3-r[c",
		"v1.2.3-@rc",
		"v1.2.3-r:c",
	}
	for _, s := range reject {
		if _, err := Parse(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) = %v, want ErrInvalid", s, err)
		}
	}
}

// The rejection diagnostics name the offending component class.
func TestParseRejectionMessages(t *testing.T) {
	cases := map[string]string{
		"v1..3":        "empty number",
		"v01.2.3":      "has a leading zero",
		"va.0.0":       "has a non-digit",
		"v1.2.3-":      "empty prerelease identifier",
		"v1.2.3-r@c":   "has an invalid character",
		"v1.2.3-rc.01": "numeric prerelease identifier",
		"1.2.3":        "does not start with v",
		"v1.2":         "core is not MAJOR.MINOR.PATCH",
	}
	for in, msg := range cases {
		_, err := Parse(in)
		if err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("Parse(%q) = %v, want message naming %q", in, err, msg)
		}
	}
}

// Major exposes canonical digits; CompareMajor orders numerically, not
// lexically.
func TestMajorAccessors(t *testing.T) {
	if got := mustParse(t, "v10.2.3").Major(); got != "10" {
		t.Errorf("Major() = %q", got)
	}
	nine, ten := mustParse(t, "v9.0.0"), mustParse(t, "v10.0.0")
	if CompareMajor(nine, ten) >= 0 || CompareMajor(ten, nine) <= 0 {
		t.Error("CompareMajor ordered v9/v10 lexically")
	}
	if CompareMajor(nine, mustParse(t, "v9.9.9")) != 0 {
		t.Error("CompareMajor not zero for equal majors")
	}
}

func TestCompareGolden(t *testing.T) {
	// Strictly ascending per semantic-version precedence with
	// pseudo-versions interleaved chronologically.
	asc := []string{
		"v0.0.0-20200101000000-abcdef123456",
		"v0.0.0-20260808115925-abcdef123456",
		"v0.0.0",
		"v0.1.0",
		"v1.0.0-0",
		"v1.0.0-0.20200101000000-abcdef123456",
		"v1.0.0-0.20260808115925-abcdef123456",
		"v1.0.0-9",
		"v1.0.0-10",
		"v1.0.0-99",
		"v1.0.0--1",
		"v1.0.0-a",
		"v1.0.0-alpha",
		"v1.0.0-alpha.1",
		"v1.0.0-alpha.beta",
		"v1.0.0-beta",
		"v1.0.0-rc.1",
		"v1.0.0-rc.1.0.20260808115925-abcdef123456",
		"v1.0.0-rc.2",
		"v1.0.0-rc.9",
		"v1.0.0-rc.10",
		"v1.0.0-rc.1a",
		"v1.0.0",
		"v1.0.1",
		"v1.10.0",
		"v2.0.0",
		"v10.0.0",
		"v1234567890123456.0.0",
	}
	for i := range asc {
		for j := range asc {
			a, b := mustParse(t, asc[i]), mustParse(t, asc[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := Compare(a, b); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", a, b, got, want)
			}
		}
	}
}

// genVersion draws a valid version, biased toward collisions and
// interesting prerelease shapes so ordering properties see equal cores.
func genVersion(t *rapid.T, label string) Version {
	core := fmt.Sprintf("v%d.%d.%d",
		rapid.IntRange(0, 3).Draw(t, label+"Maj"),
		rapid.IntRange(0, 3).Draw(t, label+"Min"),
		rapid.IntRange(0, 3).Draw(t, label+"Pat"))
	n := rapid.IntRange(0, 3).Draw(t, label+"PreN")
	if n == 0 {
		v, err := Parse(core)
		if err != nil {
			t.Fatalf("generator produced invalid %q: %v", core, err)
		}
		return v
	}
	ids := make([]string, n)
	for i := range ids {
		if rapid.Bool().Draw(t, label+"Num") {
			ids[i] = fmt.Sprint(rapid.IntRange(0, 20).Draw(t, label+"NumV"))
		} else {
			ids[i] = string(rapid.SliceOfN(rapid.SampledFrom([]rune("abz-190")), 1, 4).Draw(t, label+"AlnV"))
			if isNumericID(ids[i]) && len(ids[i]) > 1 && ids[i][0] == '0' {
				ids[i] = "x" + ids[i]
			}
		}
	}
	s := core + "-" + strings.Join(ids, ".")
	v, err := Parse(s)
	if err != nil {
		t.Fatalf("generator produced invalid %q: %v", s, err)
	}
	return v
}

func TestCompareTotalOrderProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := genVersion(t, "a")
		b := genVersion(t, "b")
		c := genVersion(t, "c")
		// Antisymmetry with equality exactly on string equality: without
		// build metadata, equal precedence means equal spelling.
		ab, ba := Compare(a, b), Compare(b, a)
		if ab != -ba {
			t.Fatalf("Compare(%s,%s)=%d but Compare(%s,%s)=%d", a, b, ab, b, a, ba)
		}
		if (ab == 0) != (a.String() == b.String()) {
			t.Fatalf("Compare(%s,%s)=0 iff equal strings violated", a, b)
		}
		// Transitivity over every ordering of the triple.
		if ab <= 0 && Compare(b, c) <= 0 && Compare(a, c) > 0 {
			t.Fatalf("transitivity violated: %s <= %s <= %s but %s > %s", a, b, c, a, c)
		}
		// Max agrees with Compare.
		m := Max(a, b)
		if Compare(m, a) < 0 || Compare(m, b) < 0 {
			t.Fatalf("Max(%s,%s)=%s is below an argument", a, b, m)
		}
	})
}

func TestPseudoRoundTripProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var precedent *Version
		switch rapid.IntRange(0, 2).Draw(t, "form") {
		case 1:
			// Component draws include carry-shaped values: a precedent
			// patch of 9/19/99 exercises PseudoVersion's increment carry.
			v := mustParseRapid(t, fmt.Sprintf("v%d.%d.%d",
				rapid.SampledFrom([]int{0, 1, 5, 9, 19, 99}).Draw(t, "maj"),
				rapid.SampledFrom([]int{0, 1, 5, 9, 19, 99}).Draw(t, "min"),
				rapid.SampledFrom([]int{0, 1, 5, 9, 19, 99}).Draw(t, "pat")))
			precedent = &v
		case 2:
			// Prerelease precedents across the identifier surface:
			// alphanumeric with hyphens, numerics including a bare 0, and
			// multi-identifier lists ending in 0.
			n := rapid.IntRange(1, 3).Draw(t, "preN")
			ids := make([]string, n)
			for i := range ids {
				ids[i] = rapid.SampledFrom([]string{"rc", "alpha-3", "0", "7", "x-y", "beta"}).Draw(t, fmt.Sprint("preID", i))
			}
			v := mustParseRapid(t, fmt.Sprintf("v%d.%d.%d-%s",
				rapid.IntRange(0, 5).Draw(t, "maj"),
				rapid.IntRange(0, 5).Draw(t, "min"),
				rapid.IntRange(0, 5).Draw(t, "pat"),
				strings.Join(ids, ".")))
			precedent = &v
		}
		sec := rapid.Int64Range(0, 4102444799).Draw(t, "sec") // through 2099
		when := time.Unix(sec, 0).UTC()
		hash := fmt.Sprintf("%012x", rapid.Int64Range(0, 1<<47).Draw(t, "hash"))
		p, err := PseudoVersion(precedent, when, hash)
		if err != nil {
			t.Fatalf("PseudoVersion: %v", err)
		}
		ts, h, ok := p.Pseudo()
		if !ok {
			t.Fatalf("synthesized %s not recognized as pseudo", p)
		}
		if want := when.Format("20060102150405"); ts != want || h != hash[:12] {
			t.Fatalf("%s round-tripped to (%s, %s), want (%s, %s)", p, ts, h, want, hash[:12])
		}
		// A pseudo-version orders above its precedent and below the next
		// release, and later commits order above earlier ones.
		if precedent != nil && Compare(p, *precedent) <= 0 {
			t.Fatalf("%s does not order above precedent %s", p, precedent)
		}
		later, err := PseudoVersion(precedent, when.Add(time.Second), hash)
		if err != nil {
			t.Fatal(err)
		}
		if Compare(p, later) >= 0 {
			t.Fatalf("chronology violated: %s !< %s", p, later)
		}
	})
}

func mustParseRapid(t *rapid.T, s string) Version {
	v, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return v
}

func TestPseudoRecognitionGolden(t *testing.T) {
	pseudo := map[string][2]string{
		"v0.0.0-20260808115925-abcdef123456":        {"20260808115925", "abcdef123456"},
		"v1.2.4-0.20260808115925-abcdef123456":      {"20260808115925", "abcdef123456"},
		"v1.2.3-rc.1.0.20260808115925-abcdef123456": {"20260808115925", "abcdef123456"},
	}
	for s, want := range pseudo {
		ts, h, ok := mustParse(t, s).Pseudo()
		if !ok || ts != want[0] || h != want[1] {
			t.Errorf("%s: Pseudo() = (%s, %s, %v), want (%s, %s, true)", s, ts, h, ok, want[0], want[1])
		}
	}
	notPseudo := []string{
		"v1.2.3",
		"v1.2.3-rc.1",
		"v0.0.1-20260808115925-abcdef123456",      // form 1 requires the zero core
		"v0.1.0-20260808115925-abcdef123456",      // form 1: nonzero minor
		"v1.0.0-20260808115925-abcdef123456",      // form 1: nonzero major
		"v1.2.4-1.20260808115925-abcdef123456",    // separator identifier must be 0
		"v1.2.4-0.20260808115925-ABCDEF123456",    // hash must be lowercase hex
		"v1.2.4-0.20260808115925-g23456789012",    // hash first digit above hex range
		"v1.2.4-0.20260808115925-abcdef12345g",    // hash last digit above hex range
		"v1.2.4-0.20260808115925-abcdef1234567",   // hash is exactly 12 digits
		"v1.2.4-0.2026080811592-abcdef123456",     // timestamp is exactly 14 digits
		"v1.2.4-0.2026080811592x-abcdef123456",    // timestamp digits only
		"v1.2.3-rc.1.20260808115925-abcdef123456", // missing the 0 separator
		"v1.2.4-0.20260808115925x-bcdef123456",    // malformed separator position
		"v1.2.0-0.20260808115925-abcdef123456",    // patch 0 has no release precedent
		"v1.2.4-0.202608081159259abcdef123456",    // separator must be a dash
		"v1.2.4-0.20260808115925-abcde-123456",    // hash digits only, no dash
		"v1.2.4-0.x0260808115925-abcdef123456",    // timestamp first digit
	}
	for _, s := range notPseudo {
		ts, h, ok := mustParse(t, s).Pseudo()
		if ok {
			t.Errorf("%s recognized as pseudo", s)
		}
		// Rejection returns zero values, never partial extractions.
		if ts != "" || h != "" {
			t.Errorf("%s: rejection carried (%q, %q)", s, ts, h)
		}
	}
}

// The patch increment carries: a release precedent ending in 9 yields the
// next decimal patch, not a corrupted digit.
func TestPseudoVersionCarryGolden(t *testing.T) {
	now := time.Date(2026, 8, 8, 11, 59, 25, 0, time.UTC)
	cases := map[string]string{
		"v1.2.9":  "v1.2.10-0.20260808115925-abcdef123456",
		"v1.2.99": "v1.2.100-0.20260808115925-abcdef123456",
		"v0.0.0":  "v0.0.1-0.20260808115925-abcdef123456",
	}
	for precedent, want := range cases {
		p := mustParse(t, precedent)
		got, err := PseudoVersion(&p, now, "abcdef123456")
		if err != nil || got.String() != want {
			t.Errorf("PseudoVersion(%s) = %s, %v; want %s", precedent, got, err, want)
		}
	}
}

func TestPseudoVersionRejections(t *testing.T) {
	now := time.Date(2026, 8, 8, 11, 59, 25, 0, time.UTC)
	if _, err := PseudoVersion(nil, now, "abc"); !errors.Is(err, ErrInvalid) {
		t.Errorf("short hash: %v", err)
	}
	pseudo := mustParse(t, "v0.0.0-20260808115925-abcdef123456")
	if _, err := PseudoVersion(&pseudo, now, "abcdef123456"); !errors.Is(err, ErrInvalid) {
		t.Errorf("pseudo precedent must be refused, got %v", err)
	}
	// The hash is caller-supplied and unconstrained: a non-hex byte in the
	// used prefix is rejected at the hash check with its own diagnostic,
	// never smuggled into the synthesized string for Parse to trip over.
	if _, err := PseudoVersion(nil, now, "abcde:123456"); err == nil || !strings.Contains(err.Error(), "not lowercase hex") {
		t.Errorf("colon hash: %v", err)
	}
	if _, err := PseudoVersion(nil, now, "ABCDEF123456"); !errors.Is(err, ErrInvalid) {
		t.Errorf("uppercase hash: %v", err)
	}
}

// The ordering is deterministic under any input permutation: sorting a
// shuffled slice always lands the same result (REQ-resolve-determinism's
// version-ordering half).
func TestSortDeterminismProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(2, 8).Draw(t, "n")
		vs := make([]Version, n)
		for i := range vs {
			vs[i] = genVersion(t, fmt.Sprint("v", i))
		}
		a := append([]Version(nil), vs...)
		b := append([]Version(nil), vs...)
		rand.New(rand.NewSource(int64(rapid.IntRange(0, 1000).Draw(t, "seed")))).Shuffle(len(b), func(i, j int) { b[i], b[j] = b[j], b[i] })
		sortVersions(a)
		sortVersions(b)
		for i := range a {
			if a[i].String() != b[i].String() {
				t.Fatalf("sort order depends on input order at %d: %s vs %s", i, a[i], b[i])
			}
		}
	})
}

func sortVersions(vs []Version) {
	for i := 1; i < len(vs); i++ {
		for j := i; j > 0 && Compare(vs[j-1], vs[j]) > 0; j-- {
			vs[j-1], vs[j] = vs[j], vs[j-1]
		}
	}
}

// The accepted grammar is exactly canonical semver minus build metadata,
// pinned differentially against golang.org/x/mod/semver — the oracle the
// module-file checker used before version parsing collapsed here.
func TestGrammarAgreesWithCanonicalSemver(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := string(rapid.SliceOfN(rapid.SampledFrom([]rune("v0123456789.-+abcXZ")), 0, 24).Draw(t, "s"))
		_, err := Parse(s)
		ours := err == nil
		xmod := semver.IsValid(s) && s == semver.Canonical(s) && !strings.Contains(s, "+")
		if ours != xmod {
			t.Fatalf("divergence on %q: Parse ok=%v, canonical x/mod semver=%v", s, ours, xmod)
		}
	})
}

// The ordering agrees with x/mod semver precedence on every accepted
// pair — the grammar differential's counterpart for Compare.
func TestOrderingAgreesWithSemver(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := genVersion(t, "a")
		b := genVersion(t, "b")
		ours := Compare(a, b)
		xmod := semver.Compare(a.String(), b.String())
		if ours != xmod {
			t.Fatalf("ordering divergence: Compare(%s, %s) = %d, semver.Compare = %d", a, b, ours, xmod)
		}
	})
}

func FuzzParse(f *testing.F) {
	f.Add("v1.2.3")
	f.Add("v1.0.0-rc.1")
	f.Add("v0.0.0-20260808115925-abcdef123456")
	f.Add("v01.2.3")
	f.Add("v1.2.3+meta")
	f.Fuzz(func(t *testing.T, s string) {
		v, err := Parse(s)
		if err != nil {
			return
		}
		if v.String() != s {
			t.Fatalf("accepted %q but String() = %q", s, v.String())
		}
		if v2, err := Parse(v.String()); err != nil || Compare(v, v2) != 0 {
			t.Fatalf("re-parse of %q: %v", v.String(), err)
		}
	})
}
