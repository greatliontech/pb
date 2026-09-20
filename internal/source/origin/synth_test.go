package origin

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/greatliontech/pb/internal/module/version"
)

func TestSynthesizedVersionsTakesRepoTags(t *testing.T) {
	rs := refs(t,
		"refs/heads/main", "h0",
		"refs/tags/v1.0.0", "t1",
		"refs/tags/protos/v2.0.0", "t2", // subtree tag: not a repo-level release
	)
	head := Commit{Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Time: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)}
	tags, err := SynthesizedVersions(rs, head)
	if err != nil || len(tags) != 1 || tags[0].Version.String() != "v1.0.0" || tags[0].Hash != "t1" {
		t.Fatalf("tags=%+v err=%v, want the v1.0.0 repo tag alone", tags, err)
	}
}

func TestSynthesizedVersionsFallsBackToPseudo(t *testing.T) {
	rs := refs(t,
		"refs/heads/main", "h0",
		"refs/tags/protos/v2.0.0", "t2", // subtree tags do not avert the fallback
		"refs/tags/not-a-version", "t3",
		// A pseudo-version-shaped tag is not a release
		// (REQ-resolve-pseudo-base) and must not avert the fallback
		// either — as a "release" it would be unresolvable.
		"refs/tags/v9.0.1-0.20260101000000-bbbbbbbbbbbb", "t4",
	)
	head := Commit{Hash: "0123456789abcdef0123456789abcdef01234567", Time: time.Date(2026, 8, 8, 12, 30, 15, 0, time.UTC)}
	tags, err := SynthesizedVersions(rs, head)
	if err != nil || len(tags) != 1 {
		t.Fatalf("tags=%+v err=%v", tags, err)
	}
	if got, want := tags[0].Version.String(), "v0.0.0-20260808123015-0123456789ab"; got != want {
		t.Fatalf("pseudo = %s, want %s", got, want)
	}
	if tags[0].Hash != head.Hash {
		t.Fatalf("pseudo tag names %q, want the head commit", tags[0].Hash)
	}
}

func TestSynthesizedVersionsBadHead(t *testing.T) {
	for name, hash := range map[string]string{
		"short":            "short",
		"abbreviated12hex": "0123456789ab", // enough for a pseudo, not a resolvable Tag.Hash
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := SynthesizedVersions(nil, Commit{Hash: hash, Time: time.Unix(0, 0)}); err == nil {
				t.Fatal("degenerate head commit accepted")
			}
		})
	}
}

// With any repository-level release tag present, the synthesized module's
// releases are exactly the repository-level tags; with none, exactly one
// pseudo-version of the head commit that verifies against it.
func TestSynthesizedVersionsProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		versions := []string{"v0.1.0", "v1.0.0", "v1.2.3-rc.1", "v2.0.0"}
		n := rapid.IntRange(0, 4).Draw(t, "n")
		var rs []Ref
		for i := range n {
			rs = append(rs, Ref{Name: "refs/tags/" + versions[i], Hash: fmt.Sprint("t", i)})
		}
		rs = append(rs, Ref{Name: "refs/heads/main", Hash: "h0"})
		hexRunes := []rune("0123456789abcdef")
		hash := string(rapid.SliceOfN(rapid.SampledFrom(hexRunes), 40, 40).Draw(t, "hash"))
		sec := rapid.Int64Range(0, 4102444800).Draw(t, "sec") // through 2100
		head := Commit{Hash: hash, Time: time.Unix(sec, 0).UTC()}

		tags, err := SynthesizedVersions(rs, head)
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if n > 0 {
			want := ReleaseTags(rs, "")
			if fmt.Sprintf("%+v", tags) != fmt.Sprintf("%+v", want) {
				t.Fatalf("with repo tags present, releases = %+v, want %+v", tags, want)
			}
			return
		}
		if len(tags) != 1 || !tags[0].Version.IsPseudo() || tags[0].Hash != hash {
			t.Fatalf("no repo tags: releases = %+v, want one head pseudo", tags)
		}
		if err := VerifyPseudo(tags[0].Version, head); err != nil {
			t.Fatalf("synthesized pseudo does not verify against its own head: %v", err)
		}
	})
}

func TestVerifyPseudoGolden(t *testing.T) {
	v, err := version.Parse("v0.0.0-20260808123015-0123456789ab")
	if err != nil {
		t.Fatal(err)
	}
	c := Commit{Hash: "0123456789abcdef0123456789abcdef01234567", Time: time.Date(2026, 8, 8, 12, 30, 15, 0, time.UTC)}
	if err := VerifyPseudo(v, c); err != nil {
		t.Fatalf("matching commit rejected: %v", err)
	}
	// Commit time in a non-UTC zone still matches: the binding is on the
	// UTC rendering.
	off := c
	off.Time = c.Time.In(time.FixedZone("plus2", 2*3600))
	if err := VerifyPseudo(v, off); err != nil {
		t.Fatalf("zone-shifted equal commit time rejected: %v", err)
	}
	wrongHash := c
	wrongHash.Hash = "ffff56789abcdef0123456789abcdef012345678"
	if err := VerifyPseudo(v, wrongHash); !errors.Is(err, ErrPseudoMismatch) {
		t.Fatalf("wrong hash: err=%v, want ErrPseudoMismatch", err)
	}
	wrongTime := c
	wrongTime.Time = c.Time.Add(time.Second)
	if err := VerifyPseudo(v, wrongTime); !errors.Is(err, ErrPseudoMismatch) {
		t.Fatalf("wrong time: err=%v, want ErrPseudoMismatch", err)
	}
	release, err := version.Parse("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPseudo(release, c); !errors.Is(err, ErrPseudoMismatch) ||
		!strings.Contains(err.Error(), "not a pseudo-version") {
		t.Fatalf("non-pseudo: err=%v, want ErrPseudoMismatch naming the misuse", err)
	}
	// A SHA-256 origin verifies identically.
	sha256c := Commit{Hash: "0123456789ab" + strings.Repeat("0", 52), Time: c.Time}
	if err := VerifyPseudo(v, sha256c); err != nil {
		t.Fatalf("64-digit commit hash rejected: %v", err)
	}
}

// The commit passed to VerifyPseudo must be a full hash: the version's
// own embedded prefix (or any abbreviation) cannot stand in for an
// origin lookup, so a Commit fabricated from the version never passes.
func TestVerifyPseudoRequiresFullHash(t *testing.T) {
	v, err := version.Parse("v0.0.0-20260808123015-0123456789ab")
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 8, 8, 12, 30, 15, 0, time.UTC)
	for name, hash := range map[string]string{
		"embedded prefix":       "0123456789ab",
		"39 digits":             strings.Repeat("a", 39),
		"41 digits":             strings.Repeat("a", 41),
		"uppercase":             "0123456789AB" + strings.Repeat("C", 28),
		"non-hex":               "0123456789ab" + strings.Repeat("g", 28),
		"bad first digit only":  "G" + strings.Repeat("a", 39),
		"digit-letter gap char": "0123456789ab:" + strings.Repeat("a", 27),
		"below-digit char":      "0123456789ab/" + strings.Repeat("a", 27),
		"empty":                 "",
	} {
		t.Run(name, func(t *testing.T) {
			err := VerifyPseudo(v, Commit{Hash: hash, Time: when})
			if !errors.Is(err, ErrPseudoMismatch) || !strings.Contains(err.Error(), "full lowercase-hex commit hash") {
				t.Fatalf("hash %q: err=%v, want full-hash rejection", hash, err)
			}
		})
	}
}

// A pseudo-version verifies against exactly the commit it was synthesized
// from: the round trip always passes, and perturbing the bound identity —
// the embedded hash prefix or the commit time — always fails.
func TestVerifyPseudoRoundTripProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		hexRunes := []rune("0123456789abcdef")
		hash := string(rapid.SliceOfN(rapid.SampledFrom(hexRunes), 40, 40).Draw(t, "hash"))
		sec := rapid.Int64Range(0, 4102444800).Draw(t, "sec")
		c := Commit{Hash: hash, Time: time.Unix(sec, 0).UTC()}
		v, err := version.PseudoVersion(nil, c.Time, c.Hash)
		if err != nil {
			t.Fatalf("PseudoVersion: %v", err)
		}
		if err := VerifyPseudo(v, c); err != nil {
			t.Fatalf("round trip failed: %v", err)
		}
		// Flip one digit inside the bound 12-digit prefix: must fail.
		pos := rapid.IntRange(0, 11).Draw(t, "pos")
		flipped := []rune(hash)
		if flipped[pos] == 'f' {
			flipped[pos] = '0'
		} else {
			flipped[pos]++
			if flipped[pos] == ':' { // '9'+1: continue into hex letters
				flipped[pos] = 'a'
			}
		}
		if err := VerifyPseudo(v, Commit{Hash: string(flipped), Time: c.Time}); !errors.Is(err, ErrPseudoMismatch) {
			t.Fatalf("prefix-perturbed hash accepted: %v", err)
		}
		// Shift the commit time by a nonzero number of seconds: must fail.
		delta := rapid.Int64Range(1, 1<<20).Draw(t, "delta")
		if rapid.Bool().Draw(t, "neg") && sec-delta >= 0 {
			delta = -delta
		}
		if err := VerifyPseudo(v, Commit{Hash: hash, Time: time.Unix(sec+delta, 0).UTC()}); !errors.Is(err, ErrPseudoMismatch) {
			t.Fatalf("time-perturbed commit accepted: %v", err)
		}
		// A digit beyond the 12-digit prefix is not part of the binding.
		beyond := []rune(hash)
		if beyond[20] == 'f' {
			beyond[20] = '0'
		} else {
			beyond[20]++
			if beyond[20] == ':' {
				beyond[20] = 'a'
			}
		}
		if err := VerifyPseudo(v, Commit{Hash: string(beyond), Time: c.Time}); err != nil {
			t.Fatalf("perturbation beyond the bound prefix rejected: %v", err)
		}
	})
}

// The hash binding is a prefix match on the full hash — a commit whose
// hash merely contains the embedded digits elsewhere does not verify.
func TestVerifyPseudoPrefixNotSubstring(t *testing.T) {
	v, err := version.Parse("v0.0.0-20260808123015-0123456789ab")
	if err != nil {
		t.Fatal(err)
	}
	c := Commit{
		Hash: "ff0123456789ab0123456789ab0123456789ab01",
		Time: time.Date(2026, 8, 8, 12, 30, 15, 0, time.UTC),
	}
	if err := VerifyPseudo(v, c); !errors.Is(err, ErrPseudoMismatch) {
		t.Fatalf("substring-only hash accepted: %v", err)
	}
	if !strings.Contains(c.Hash, "0123456789ab") {
		t.Fatal("test fixture broken: hash must contain the digits off-prefix")
	}
}
