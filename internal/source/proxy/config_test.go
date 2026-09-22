package proxy

import (
	"errors"
	"fmt"
	"github.com/greatliontech/pb/internal/source"
	"path"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// No configuration means direct: no party beyond the origin host is
// trusted for a first fetch.
func TestParseConfigDefault(t *testing.T) {
	cfg, err := parseConfig("", "")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%+v", cfg.Sources) != "[{URL: Direct:true Off:false}]" || cfg.NoProxy != nil {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseConfigGolden(t *testing.T) {
	cfg, err := parseConfig("https://proxy.example.com/pb,direct,off", "corp.example.com,*.internal/*")
	if err != nil {
		t.Fatal(err)
	}
	want := []Source{
		{URL: "https://proxy.example.com/pb"},
		{Direct: true},
		{Off: true},
	}
	if fmt.Sprintf("%+v", cfg.Sources) != fmt.Sprintf("%+v", want) {
		t.Fatalf("sources = %+v, want %+v", cfg.Sources, want)
	}
	if fmt.Sprint(cfg.NoProxy) != "[corp.example.com *.internal/*]" {
		t.Fatalf("noproxy = %v", cfg.NoProxy)
	}
}

// An http (not just https) proxy entry is a valid base URL: the spec
// pins proxies "by base URL" without a scheme constraint.
func TestParseConfigHTTPProxy(t *testing.T) {
	cfg, err := parseConfig("http://proxy.internal:3000/pb", "")
	if err != nil || len(cfg.Sources) != 1 || cfg.Sources[0].URL != "http://proxy.internal:3000/pb" {
		t.Fatalf("cfg = %+v err = %v", cfg, err)
	}
}

// parseConfig composes the two setting parsers as the CLI does.
func parseConfig(proxy, noproxy string) (Config, error) {
	sources, err := ParseSources(proxy)
	if err != nil {
		return Config{}, err
	}
	patterns, err := ParseNoProxy(noproxy)
	if err != nil {
		return Config{}, err
	}
	return Config{Sources: sources, NoProxy: patterns}, nil
}

func TestParseConfigRejections(t *testing.T) {
	for name, tc := range map[string]struct {
		pbproxy, pbnoproxy string
		want               string // diagnostic must name the defect
	}{
		"empty proxy entry":         {"https://p.example.com,,direct", "", "empty entry"},
		"relative proxy URL":        {"proxy.example.com", "", "scheme"},
		"bad scheme":                {"ftp://proxy.example.com", "", "scheme"},
		"hostless URL":              {"https://", "", "host"},
		"control char in URL":       {"https://p.example.com/\x00pb", "", "source \"https://p.example.com/\\x00pb\""},
		"query in URL":              {"https://p.example.com/pb?x=1", "", "query"},
		"bare query marker":         {"https://p.example.com/pb?", "", "query"},
		"fragment in URL":           {"https://p.example.com/pb#f", "", "query"},
		"bare fragment marker":      {"https://p.example.com/pb#", "", "query"},
		"credentials in URL":        {"https://u:p@p.example.com", "", "query"},
		"multiple trailing slashes": {"https://p.example.com/pb//", "", "trailing slashes"},
		"empty noproxy pattern":     {"", "corp.example.com,", "empty pattern"},
		"unterminated class":        {"", "corp.[a-example.com", "syntax error"},
		"empty class":               {"", "corp.[]x.com", "syntax error"},
		"trailing backslash":        {"", `corp.example.com\`, "syntax error"},
		"class-escape at end":       {"", `corp.[a\`, "syntax error"},
		"range without low":         {"", "corp.[-a]x.com", "syntax error"},
		"range without high":        {"", "corp.[a-]x.com", "syntax error"},
		// Env values are bytes: an invalid-UTF-8 byte inside a class is
		// ErrBadPattern to the matching engine and must be a config
		// error, never a pattern that silently misroutes by matching
		// nothing. (A bare invalid byte outside a class is a legal
		// literal — pinned accepted in TestCheckPatternGolden.)
		"invalid utf8 in class": {"", "corp.[\xe4]xample.com", "syntax error"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseConfig(tc.pbproxy, tc.pbnoproxy)
			if !errors.Is(err, ErrConfig) {
				t.Fatalf("err = %v, want ErrConfig", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// Boundary goldens for the glob grammar, both polarities.
func TestCheckPatternGolden(t *testing.T) {
	for pat, valid := range map[string]bool{
		"corp.example.com": true,
		"*.example.com/*":  true,
		"a?b":              true,
		"[ab]":             true,
		"[a-z]":            true,
		"[^ab]":            true,
		"[a^]":             true, // '^' is only special first
		`[\]]`:             true, // escaped ']' as a member
		`[\-a]`:            true, // escaped '-' as a low endpoint
		`[\-]`:             true, // escaped '-' as the sole member
		`[\a]`:             true, // escape of an ordinary character
		`a\*b`:             true,
		"[a-z][0-9]":       true,
		"[":                false,
		"[]":               false,
		"[^]":              false,
		"[-a]":             false, // bare '-' cannot open a member
		"[a-]":             false, // range missing its high endpoint
		"[a-]x":            false,
		`[a\`:              false,
		`\`:                false,
		"[\xff]":           false, // invalid UTF-8 is not a class member
		"corp.\xff.com":    true,  // …but a bare invalid byte is a legal literal (it just never matches an ASCII module path)
		"[é]":              true,  // valid multi-byte runes are members
		"[a-é]":            true,
	} {
		if got := source.CheckPattern(pat) == nil; got != valid {
			t.Errorf("source.CheckPattern(%q) valid=%v, want %v", pat, got, valid)
		}
	}
}

// Differential truth: patterns assembled from grammar pieces of known
// validity — checkPattern must accept exactly the assemblies built from
// valid pieces. This drives both polarities, unlike the soundness
// property, which can only convict over-acceptance.
func TestCheckPatternConstructedValidityProperty(t *testing.T) {
	valid := []string{"a", "b", "*", "?", "/", ".", "[ab]", "[a-z]", "[^x]", `[\]]`, `[\-]`, `\*`, `[a-b][c-d]`}
	invalid := []string{"[", "[]", "[^]", "[a-", "[-a]", "[a-]", `[a\`}
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(t, "n")
		var b strings.Builder
		wantValid := true
		for i := range n {
			if rapid.IntRange(0, 6).Draw(t, fmt.Sprint("bad", i)) == 0 {
				// An invalid piece ends the assembly: appending more could
				// repair it (an unterminated class swallows what follows —
				// "[a-" + "z]" is the valid "[a-z]"), breaking the oracle.
				b.WriteString(rapid.SampledFrom(invalid).Draw(t, fmt.Sprint("p", i)))
				wantValid = false
				break
			}
			b.WriteString(rapid.SampledFrom(valid).Draw(t, fmt.Sprint("p", i)))
		}
		pat := b.String()
		if got := source.CheckPattern(pat) == nil; got != wantValid {
			t.Fatalf("source.CheckPattern(%q) valid=%v, want %v", pat, got, wantValid)
		}
	})
}

func TestSourcesForRouting(t *testing.T) {
	cfg, err := parseConfig("https://p.example.com/pb,off", "corp.example.com,*.secret.io/protos")
	if err != nil {
		t.Fatal(err)
	}
	direct := "[{URL: Direct:true Off:false}]"
	for modulePath, wantDirect := range map[string]bool{
		"corp.example.com/protos":   true,  // host prefix match
		"corp.example.com/a/b/c":    true,  // deeper subtree
		"corp.example.com":          true,  // exact pattern match
		"corpx.example.com/protos":  false, // no partial-segment match
		"api.secret.io/protos":      true,  // glob star within one segment
		"api.secret.io/protos/v1":   true,  // prefix of a deeper path
		"api.secret.io/other":       false, // pattern names /protos
		"a.b.secret.io/protos":      true,  // '*' spans dots freely; only '/' bounds it
		"public.example.org/protos": false,
	} {
		got := cfg.SourcesFor(modulePath)
		if wantDirect != (fmt.Sprintf("%+v", got) == direct) {
			t.Errorf("SourcesFor(%q) = %+v, wantDirect=%v", modulePath, got, wantDirect)
		}
		if !wantDirect && fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", cfg.Sources) {
			t.Errorf("SourcesFor(%q) = %+v, want the configured list", modulePath, got)
		}
	}
}

// A pattern matches the module path itself, not only proper prefixes —
// pinned directly, independent of the routing table.
func TestSourcesForExactMatch(t *testing.T) {
	cfg, err := parseConfig("off", "corp.example.com")
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.SourcesFor("corp.example.com")
	if len(got) != 1 || !got[0].Direct {
		t.Fatalf("exact pattern match = %+v, want direct", got)
	}
}

// A noproxy match routes direct even when the source list is off.
func TestSourcesForNoProxyBeatsOff(t *testing.T) {
	cfg, err := parseConfig("off", "corp.example.com")
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.SourcesFor("corp.example.com/protos")
	if len(got) != 1 || !got[0].Direct || got[0].Off {
		t.Fatalf("noproxy under off = %+v, want direct", got)
	}
	rest := cfg.SourcesFor("other.example.com/protos")
	if len(rest) != 1 || !rest[0].Off {
		t.Fatalf("unmatched under off = %+v, want off", rest)
	}
}

// checkPattern is sound: an accepted pattern never makes path.Match
// report ErrBadPattern, on any probe — so SourcesFor can safely read a
// Match error as non-match.
func TestCheckPatternSoundnessProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Env values are bytes: the alphabet includes invalid-UTF-8
		// bytes, which the engine treats as bad class members.
		alphabet := []byte(`ab*?[]^-\/.` + "\xff\xc3")
		pat := string(rapid.SliceOfN(rapid.SampledFrom(alphabet), 0, 10).Draw(t, "pat"))
		probe := string(rapid.SliceOfN(rapid.SampledFrom([]byte("ab./")), 0, 10).Draw(t, "probe"))
		if err := source.CheckPattern(pat); err != nil {
			return
		}
		if _, err := path.Match(pat, probe); err != nil {
			t.Fatalf("checkPattern accepted %q but path.Match(%q, %q) = %v", pat, pat, probe, err)
		}
		// Prefix matching probes every segment prefix; none may error.
		prefix := probe
		for {
			if _, err := path.Match(pat, prefix); err != nil {
				t.Fatalf("prefix probe %q: %v", prefix, err)
			}
			i := strings.LastIndexByte(prefix, '/')
			if i < 0 {
				break
			}
			prefix = prefix[:i]
		}
	})
}
