package proxy

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/greatliontech/pb/internal/version"
)

func TestEscapeGolden(t *testing.T) {
	for in, want := range map[string]string{
		"example.com/protos":  "example.com/protos",
		"example.com/Protos":  "example.com/!protos",
		"example.com/ALLCAPS": "example.com/!a!l!l!c!a!p!s",
		"bang!path":           "bang!!path",
		"Mixed!And!UPPER":     "!mixed!!!and!!!u!p!p!e!r",
		"v1.0.0-RC.1":         "v1.0.0-!r!c.1",
		"":                    "",
		"Zebra":               "!zebra",
		// Neighbors of the A-Z range pass through untouched.
		"@[`{": "@[`{",
	} {
		if got := Escape(in); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
		back, err := Unescape(Escape(in))
		if err != nil || back != in {
			t.Errorf("Unescape(Escape(%q)) = (%q, %v)", in, back, err)
		}
	}
}

func TestUnescapeRejections(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		want string // diagnostic must quote the offending piece
	}{
		"trailing bang":    {"abc!", `"abc!"`},
		"bang digit":       {"ab!1c", `"!1"`},
		"bang uppercase":   {"ab!Zc", `"!Z"`},
		"bare uppercase":   {"abZc", `'Z'`},
		"bare uppercase A": {"Abc", `'A'`},
		"bare uppercase Z": {"abZ", `'Z'`},
		"bang dot":         {"ab!.c", `"!."`},
		"bang backtick":    {"ab!`c", "\"!`\""},
		"bang brace":       {"ab!{c", `"!{"`},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := Unescape(tc.in)
			if err == nil {
				t.Fatalf("Unescape(%q) = %q, want error", tc.in, out)
			}
			if out != "" {
				t.Fatalf("failure returned %q, want the zero value", out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not quote %s", err, tc.want)
			}
		})
	}
}

// Escape round-trips through Unescape and is injective: distinct inputs
// never collide in the case-insensitive escaped space.
func TestEscapeProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		alphabet := []rune("abzABZ019.!-_~/")
		gen := rapid.SliceOfN(rapid.SampledFrom(alphabet), 0, 12)
		a := string(gen.Draw(t, "a"))
		b := string(gen.Draw(t, "b"))
		ea, eb := Escape(a), Escape(b)
		if back, err := Unescape(ea); err != nil || back != a {
			t.Fatalf("round trip of %q via %q: (%q, %v)", a, ea, back, err)
		}
		if a != b && ea == eb {
			t.Fatalf("collision: %q and %q both escape to %q", a, b, ea)
		}
		// The escaped form is case-fold-safe: it never contains an
		// uppercase ASCII letter for storage to fold.
		if ea != strings.ToLower(ea) {
			t.Fatalf("Escape(%q) = %q carries uppercase", a, ea)
		}
	})
}

func TestEndpointURLs(t *testing.T) {
	v, err := version.Parse("v1.0.0-RC.1")
	if err != nil {
		t.Fatal(err)
	}
	const base = "https://proxy.example.com/pb/"
	const mod = "example.com/Protos"
	for got, want := range map[string]string{
		ListURL(base, mod):    "https://proxy.example.com/pb/example.com/!protos/@v/list",
		LatestURL(base, mod):  "https://proxy.example.com/pb/example.com/!protos/@latest",
		InfoURL(base, mod, v): "https://proxy.example.com/pb/example.com/!protos/@v/v1.0.0-!r!c.1.info",
		ModURL(base, mod, v):  "https://proxy.example.com/pb/example.com/!protos/@v/v1.0.0-!r!c.1.mod",
		ZipURL(base, mod, v):  "https://proxy.example.com/pb/example.com/!protos/@v/v1.0.0-!r!c.1.zip",
		ProvURL(base, mod, v): "https://proxy.example.com/pb/example.com/!protos/@v/v1.0.0-!r!c.1.prov",
	} {
		if got != want {
			t.Errorf("endpoint = %q, want %q", got, want)
		}
	}
	// A base without a trailing slash joins identically.
	if got := ListURL("https://proxy.example.com/pb", mod); got != ListURL(base, mod) {
		t.Errorf("no-slash base = %q", got)
	}
}

func TestParseListGolden(t *testing.T) {
	vs, err := ParseList([]byte("v1.0.0\nv2.0.0-rc.1\n\nv0.1.0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(vs) != "[v1.0.0 v2.0.0-rc.1 v0.1.0]" {
		t.Fatalf("list = %v", vs)
	}
	if vs, err := ParseList(nil); err != nil || len(vs) != 0 {
		t.Fatalf("empty list = (%v, %v)", vs, err)
	}
}

func TestParseListRejections(t *testing.T) {
	for name, body := range map[string]string{
		"not a version":      "v1.0.0\nnot-a-version\n",
		"missing v":          "1.0.0\n",
		"pseudo-version":     "v0.0.0-20260808123015-0123456789ab\n",
		"leading whitespace": " v1.0.0\n",
		"crlf line ending":   "v1.0.0\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseList([]byte(body)); !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
		})
	}
}

func TestParseInfoGolden(t *testing.T) {
	info, err := ParseInfo([]byte(`{"version":"v1.2.3","time":"2026-08-08T12:30:15Z"}`))
	if err != nil || info.Version.String() != "v1.2.3" ||
		!info.Time.Equal(time.Date(2026, 8, 8, 12, 30, 15, 0, time.UTC)) {
		t.Fatalf("info = %+v err = %v", info, err)
	}
	// time is optional; unknown members are ignored.
	info, err = ParseInfo([]byte(`{"version":"v1.2.3","origin":{"vcs":"git"}}`))
	if err != nil || info.Version.String() != "v1.2.3" || !info.Time.IsZero() {
		t.Fatalf("info = %+v err = %v", info, err)
	}
	// A null time decodes to absence, Go's conventional optional-member
	// treatment.
	info, err = ParseInfo([]byte(`{"version":"v1.2.3","time":null}`))
	if err != nil || !info.Time.IsZero() {
		t.Fatalf("null time: info = %+v err = %v", info, err)
	}
}

func TestParseInfoRejections(t *testing.T) {
	for name, body := range map[string]string{
		"not json":        "vroom",
		"missing version": `{"time":"2026-08-08T12:30:15Z"}`,
		"bad version":     `{"version":"1.2.3"}`,
		"bad time":        `{"version":"v1.2.3","time":"yesterday"}`,
		"truncated json":  `{"version":"v1.2.3"`,
		// Wrong-typed members are type errors AFTER partial decoding —
		// Unmarshal populates earlier members before failing, so the
		// unmarshal guard is load-bearing, not subsumed by field checks.
		"wrong-typed time":    `{"version":"v1.2.3","time":123}`,
		"wrong-typed version": `{"version":123,"time":"2026-08-08T12:30:15Z"}`,
		// Present-but-empty is not absence: an empty string is not an
		// RFC 3339 timestamp.
		"empty time": `{"version":"v1.2.3","time":""}`,
		// A duplicate key whose first occurrence is wrong-typed: json
		// saves the type error but keeps decoding, so the struct ends
		// fully valid — only the unmarshal-error guard rejects this.
		"duplicate key masks type error": `{"version":123,"version":"v1.2.3"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseInfo([]byte(body)); !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
		})
	}
}

func envelopeJSON(evidence ...string) []byte {
	return []byte(`{"formatVersion":1,"evidence":[` + strings.Join(evidence, ",") + `]}`)
}

const validSignedTag = `{"type":"git-signed-tag","objectFormat":"sha1",` +
	`"tag":"dGFn","commit":"Y29tbWl0","treePath":["dHJlZQ=="]}`

func TestParseEnvelopeGolden(t *testing.T) {
	env, err := ParseEnvelope(envelopeJSON(validSignedTag))
	if err != nil || len(env.GitSignedTags) != 1 {
		t.Fatalf("env = %+v err = %v", env, err)
	}
	got := env.GitSignedTags[0]
	if got.ObjectFormat != "sha1" || string(got.Tag) != "tag" ||
		string(got.Commit) != "commit" || len(got.TreePath) != 1 || string(got.TreePath[0]) != "tree" {
		t.Fatalf("evidence = %+v", got)
	}
	// Empty treePath is a root module, not an omission.
	env, err = ParseEnvelope(envelopeJSON(
		`{"type":"git-signed-tag","objectFormat":"sha256","tag":"dGFn","commit":"Y29tbWl0","treePath":[]}`))
	if err != nil || len(env.GitSignedTags) != 1 || len(env.GitSignedTags[0].TreePath) != 0 {
		t.Fatalf("root-module evidence = %+v err = %v", env, err)
	}
}

// Unrecognized evidence types are ignored, opaque fields and all; only
// an unsupported formatVersion fails (REQ-proxy-prov-unknown).
func TestParseEnvelopeUnknownEvidence(t *testing.T) {
	env, err := ParseEnvelope(envelopeJSON(
		`{"type":"notarized-sbom","payload":"???not-base64"}`,
		validSignedTag,
	))
	if err != nil || len(env.GitSignedTags) != 1 {
		t.Fatalf("env = %+v err = %v", env, err)
	}
	// A future type may reuse a recognized field name at a different
	// JSON type; its fields are opaque and must not poison the envelope.
	env, err = ParseEnvelope(envelopeJSON(
		`{"type":"future-attestation","commit":{"id":"abc"},"treePath":7}`,
		validSignedTag,
	))
	if err != nil || len(env.GitSignedTags) != 1 {
		t.Fatalf("colliding-field unknown evidence: env = %+v err = %v", env, err)
	}
	if _, err := ParseEnvelope([]byte(`{"formatVersion":2,"evidence":[]}`)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("unsupported formatVersion accepted: %v", err)
	}
	// An empty envelope of the supported version is valid: a version may
	// carry no recognized evidence.
	if env, err := ParseEnvelope(envelopeJSON()); err != nil || len(env.GitSignedTags) != 0 {
		t.Fatalf("empty envelope = (%+v, %v)", env, err)
	}
}

func TestParseEnvelopeRejections(t *testing.T) {
	for name, body := range map[string][]byte{
		"not json":              []byte("vroom"),
		"missing formatVersion": []byte(`{"evidence":[]}`),
		"truncated json":        []byte(`{"formatVersion":1,"evidence":[`),
		// Wrong-typed evidence is a type error after formatVersion has
		// already decoded — the unmarshal guard is load-bearing.
		"wrong-typed evidence": []byte(`{"formatVersion":1,"evidence":"x"}`),
		"missing evidence":     []byte(`{"formatVersion":1}`),
		"null evidence":        []byte(`{"formatVersion":1,"evidence":null}`),
		// The entry boundary (envelope term): an entry must be a JSON
		// object carrying a string type — even for types the consumer
		// would otherwise ignore.
		"non-object evidence entry": envelopeJSON(`"x"`),
		"entry without type":        envelopeJSON(`{"payload":"eA=="}`),
		"wrong-typed type":          envelopeJSON(`{"type":123}`),
		// StdEncoding requires padding; the unpadded spelling is a
		// second wire form of the same bytes.
		"unpadded base64": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dGFnZQ","commit":"Y29tbWl0","treePath":[]}`),
		// A duplicate key whose first occurrence is wrong-typed: json
		// saves the type error but keeps decoding, leaving every field
		// valid — only the unmarshal-error guard rejects this.
		"duplicate key masks type error": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":123,"tag":"dGFn","commit":"Y29tbWl0","treePath":["dHJlZQ=="]}`),
		"bad commit base64": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dGFn","commit":"???","treePath":[]}`),
		// Canonical base64 only: whitespace, spare trailing bits, and
		// empty objects are one-object-one-spelling violations. The \n
		// below is JSON's escape (this is a raw Go string), so the parsed
		// value carries a real newline — which the base64 decoder would
		// silently skip; only the whitespace check rejects it.
		"newline in base64": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dGF\nn","commit":"Y29tbWl0","treePath":[]}`),
		"space in base64": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dGF n","commit":"Y29tbWl0","treePath":[]}`),
		// Corruption after a complete quantum: the decoder returns the
		// partial bytes alongside the error, so only the error check —
		// not the empty-object check — rejects this.
		"corrupt after valid quantum": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dGFn????","commit":"Y29tbWl0","treePath":[]}`),
		"noncanonical trailing bits": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dGF=","commit":"Y29tbWl0","treePath":[]}`),
		"empty object": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":"","commit":"Y29tbWl0","treePath":[]}`),
		"null treePath entry": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dGFn","commit":"Y29tbWl0","treePath":[null]}`),
		"bad objectFormat": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"md5","tag":"dGFn","commit":"Y29tbWl0","treePath":[]}`),
		"missing tag": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","commit":"Y29tbWl0","treePath":[]}`),
		"missing commit": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dGFn","treePath":[]}`),
		"missing treePath": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dGFn","commit":"Y29tbWl0"}`),
		"bad tag base64": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":"???","commit":"Y29tbWl0","treePath":[]}`),
		"bad tree base64": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dGFn","commit":"Y29tbWl0","treePath":["???"]}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseEnvelope(body); !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
		})
	}
	// Base64 diagnostics name the offending field.
	for field, body := range map[string][]byte{
		"tag": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":"???","commit":"Y29tbWl0","treePath":[]}`),
		"commit": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dGFn","commit":"???","treePath":[]}`),
		"treePath entry": envelopeJSON(
			`{"type":"git-signed-tag","objectFormat":"sha1","tag":"dGFn","commit":"Y29tbWl0","treePath":["???"]}`),
	} {
		if _, err := ParseEnvelope(body); err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("bad %s base64: error %v does not name the field", field, err)
		}
	}
}
