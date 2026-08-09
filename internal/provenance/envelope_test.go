package provenance

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/archive"
	"pgregory.net/rapid"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func envelope(evidence ...string) []byte {
	return []byte(fmt.Sprintf(`{"formatVersion":1,"evidence":[%s]}`, strings.Join(evidence, ",")))
}

func signedTagEntry(format string, tag, commit []byte, trees ...[]byte) string {
	tp := make([]string, len(trees))
	for i, tr := range trees {
		tp[i] = fmt.Sprintf("%q", b64(tr))
	}
	return fmt.Sprintf(`{"type":"git-signed-tag","objectFormat":%q,"tag":%q,"commit":%q,"treePath":[%s]}`,
		format, b64(tag), b64(commit), strings.Join(tp, ","))
}

func TestParseEnvelope(t *testing.T) {
	tag, commit, tree := []byte("tag-bytes"), []byte("commit-bytes"), []byte("tree-bytes")

	t.Run("happy: one evidence, unknown type skipped", func(t *testing.T) {
		data := envelope(
			`{"type":"web-of-trust","voucher":"alice"}`,
			signedTagEntry("sha1", tag, commit, tree),
		)
		evs, err := ParseEnvelope(data)
		if err != nil {
			t.Fatalf("ParseEnvelope = %v, want nil", err)
		}
		if len(evs) != 1 {
			t.Fatalf("got %d evidence, want 1 (unknown type skipped)", len(evs))
		}
		ev := evs[0]
		if ev.Format != archive.SHA1 || !bytes.Equal(ev.Tag, tag) || !bytes.Equal(ev.Commit, commit) {
			t.Fatalf("evidence = %+v", ev)
		}
		if len(ev.TreePath) != 1 || !bytes.Equal(ev.TreePath[0], tree) {
			t.Fatalf("treePath = %v", ev.TreePath)
		}
	})

	t.Run("happy: empty treePath, sha256", func(t *testing.T) {
		evs, err := ParseEnvelope(envelope(signedTagEntry("sha256", tag, commit)))
		if err != nil {
			t.Fatalf("ParseEnvelope = %v, want nil", err)
		}
		if evs[0].Format != archive.SHA256 || len(evs[0].TreePath) != 0 {
			t.Fatalf("evidence = %+v", evs[0])
		}
	})

	t.Run("happy: no evidence at all", func(t *testing.T) {
		evs, err := ParseEnvelope([]byte(`{"formatVersion":1,"evidence":[]}`))
		if err != nil || len(evs) != 0 {
			t.Fatalf("ParseEnvelope = (%v, %v), want empty", evs, err)
		}
	})

	// Each malformed case asserts the failing stage's own message: a
	// guard must report its own failure, not lean on a later stage.
	malformed := map[string]struct {
		data    []byte
		wantSub string
	}{
		"not JSON":               {[]byte(`{`), "malformed envelope"},
		"missing formatVersion":  {[]byte(`{"evidence":[]}`), "missing formatVersion"},
		"non-integer version":    {[]byte(`{"formatVersion":"1","evidence":[]}`), "malformed envelope"},
		"entry not an object":    {envelope(`"git-signed-tag"`), "string type"},
		"entry without type":     {envelope(`{"objectFormat":"sha1"}`), "string type"},
		"entry type not string":  {envelope(`{"type":7}`), "string type"},
		"bad objectFormat":       {envelope(`{"type":"git-signed-tag","objectFormat":"sha512","tag":"dA==","commit":"dA==","treePath":[]}`), "sha512"},
		"missing tag":            {envelope(`{"type":"git-signed-tag","objectFormat":"sha1","commit":"dA==","treePath":[]}`), "tag:"},
		"empty tag object":       {envelope(`{"type":"git-signed-tag","objectFormat":"sha1","tag":"","commit":"dA==","treePath":[]}`), "tag: empty object"},
		"unpadded base64":        {envelope(`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dA","commit":"dA==","treePath":[]}`), "tag:"},
		"whitespace in base64":   {envelope(`{"type":"git-signed-tag","objectFormat":"sha1","tag":"d\nA==","commit":"dA==","treePath":[]}`), "tag:"},
		"url alphabet base64":    {envelope(`{"type":"git-signed-tag","objectFormat":"sha1","tag":"__--","commit":"dA==","treePath":[]}`), "tag:"},
		"nonzero spare bits":     {envelope(`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dB==","commit":"dA==","treePath":[]}`), "tag:"},
		"bad commit":             {envelope(`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dA==","commit":"x","treePath":[]}`), "commit:"},
		"missing treePath":       {envelope(`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dA==","commit":"dA=="}`), "missing treePath"},
		"null treePath":          {envelope(`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dA==","commit":"dA==","treePath":null}`), "missing treePath"},
		"treePath not a list":    {envelope(`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dA==","commit":"dA==","treePath":7}`), "evidence[0]"},
		"bad treePath element":   {envelope(`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dA==","commit":"dA==","treePath":["x"]}`), "treePath[0]"},
		"empty treePath element": {envelope(`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dA==","commit":"dA==","treePath":[""]}`), "treePath[0]"},
	}
	for name, c := range malformed {
		t.Run("malformed: "+name, func(t *testing.T) {
			_, err := ParseEnvelope(c.data)
			if !errors.Is(err, ErrEnvelopeMalformed) {
				t.Fatalf("ParseEnvelope = %v, want ErrEnvelopeMalformed", err)
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("ParseEnvelope = %v, want message containing %q", err, c.wantSub)
			}
		})
	}

	t.Run("unsupported formatVersion is its own failure", func(t *testing.T) {
		_, err := ParseEnvelope([]byte(`{"formatVersion":2,"evidence":[]}`))
		if !errors.Is(err, ErrFormatVersion) {
			t.Fatalf("ParseEnvelope = %v, want ErrFormatVersion", err)
		}
		if errors.Is(err, ErrEnvelopeMalformed) {
			t.Fatal("version failure must be distinct from malformed")
		}
	})
}

// TestDecodeCanonicalB64 pins the one-spelling decoder directly: each
// non-canonical spelling class is rejected by its own stage — the
// strict decoder, the re-encoding equality, or the emptiness check —
// with the field's label on the error.
func TestDecodeCanonicalB64(t *testing.T) {
	if b, err := decodeCanonicalB64("f", "dA=="); err != nil || string(b) != "t" {
		t.Fatalf("decodeCanonicalB64(canonical) = (%q, %v), want (t, nil)", b, err)
	}
	for name, c := range map[string]struct{ in, wantSub string }{
		"unpadded":       {"dA", "illegal base64"},
		"whitespace":     {"d\nA==", "non-canonical base64"},
		"url alphabet":   {"__--", "illegal base64"},
		"spare bits":     {"dB==", "illegal base64"},
		"empty":          {"", "empty object"},
		"trailing junk":  {"dA==!", "illegal base64"},
		"double padding": {"d===", "illegal base64"},
	} {
		t.Run(name, func(t *testing.T) {
			err := func() error { _, err := decodeCanonicalB64("field", c.in); return err }()
			if err == nil || !strings.Contains(err.Error(), "field") ||
				!strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("decodeCanonicalB64(%q) = %v, want field-labeled %q error", c.in, err, c.wantSub)
			}
		})
	}
}

// TestParseEnvelopeRoundTripsCanonicalWire proves the wire discipline
// as a for-all property: any evidence's canonical encoding parses back
// byte-identically, and re-marshaling what was parsed reproduces the
// same canonical strings — one spelling per object.
func TestParseEnvelopeRoundTripsCanonicalWire(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		format := rapid.SampledFrom([]string{"sha1", "sha256"}).Draw(rt, "format")
		tag := rapid.SliceOfN(rapid.Byte(), 1, 64).Draw(rt, "tag")
		commit := rapid.SliceOfN(rapid.Byte(), 1, 64).Draw(rt, "commit")
		var trees [][]byte
		for i, n := 0, rapid.IntRange(0, 3).Draw(rt, "ntrees"); i < n; i++ {
			trees = append(trees, rapid.SliceOfN(rapid.Byte(), 1, 32).Draw(rt, "tree"))
		}
		evs, err := ParseEnvelope(envelope(signedTagEntry(format, tag, commit, trees...)))
		if err != nil {
			t.Fatalf("ParseEnvelope(canonical) = %v, want nil", err)
		}
		ev := evs[0]
		if string(ev.Format) != format || !bytes.Equal(ev.Tag, tag) || !bytes.Equal(ev.Commit, commit) || len(ev.TreePath) != len(trees) {
			t.Fatalf("round-trip mismatch: %+v", ev)
		}
		for i := range trees {
			if !bytes.Equal(ev.TreePath[i], trees[i]) {
				t.Fatalf("treePath[%d] mismatch", i)
			}
		}
		// One spelling: the canonical encoding of what was parsed is the
		// wire string that produced it.
		var w gitSignedTagWire
		if err := json.Unmarshal([]byte(signedTagEntry(format, tag, commit, trees...)), &w); err != nil {
			t.Fatal(err)
		}
		if w.Tag != b64(ev.Tag) || w.Commit != b64(ev.Commit) {
			t.Fatal("canonical re-encoding differs from the wire spelling")
		}
	})
}
