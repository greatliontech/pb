package trust

import (
	"errors"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func TestParse(t *testing.T) {
	t.Run("happy: full policy", func(t *testing.T) {
		p, err := Parse([]byte(`default: require-provenance
modules:
  - prefix: github.com/acme
    require: allow-unsigned
  - prefix: github.com/acme/critical
    identity:
      san: "https://github.com/acme/critical/*"
      issuer: https://token.actions.githubusercontent.com
plugins:
  - prefix: ghcr.io/acme
    require: require-provenance
`))
		if err != nil {
			t.Fatalf("Parse = %v, want nil", err)
		}
		if p.Default != RequireProvenance || len(p.Modules) != 2 || len(p.Plugins) != 1 {
			t.Fatalf("policy = %+v", p)
		}
		if p.Modules[1].Identity == nil || p.Modules[1].Identity.Issuer != "https://token.actions.githubusercontent.com" {
			t.Fatalf("identity rule = %+v", p.Modules[1].Identity)
		}
		if p.Modules[0].Require != AllowUnsigned || p.Modules[0].Prefix != "github.com/acme" {
			t.Fatalf("rule[0] = %+v: parsed fields must land on the rule", p.Modules[0])
		}
		if p.Modules[1].Identity.SAN != "https://github.com/acme/critical/*" {
			t.Fatalf("rule[1].san = %q", p.Modules[1].Identity.SAN)
		}
	})

	t.Run("happy: empty file is the empty policy", func(t *testing.T) {
		p, err := Parse([]byte(""))
		if err != nil {
			t.Fatalf("Parse(empty) = %v, want nil", err)
		}
		if p.Default != "" || p.Modules != nil || p.Plugins != nil {
			t.Fatalf("policy = %+v, want zero", p)
		}
	})

	t.Run("happy: comment-only file is the empty policy", func(t *testing.T) {
		// The natural shape of a documented-but-disabled policy file.
		p, err := Parse([]byte("# every rule commented out\n# for now\n"))
		if err != nil {
			t.Fatalf("Parse(comments) = %v, want nil", err)
		}
		if p.Default != "" || p.Modules != nil || p.Plugins != nil {
			t.Fatalf("policy = %+v, want zero", p)
		}
	})

	invalid := map[string]struct {
		data    string
		wantSub string
	}{
		"unknown top-level key":  {"defaults: allow-unsigned\n", `unknown key "defaults"`},
		"bad default mode":       {"default: never\n", `unknown mode "never"`},
		"default not a string":   {"default: [a]\n", "default must be a string"},
		"modules not a list":     {"modules: yes\n", "modules must be a list"},
		"rule not a mapping":     {"modules:\n  - prefix\n", "modules[0] must be a mapping"},
		"unknown rule key":       {"modules:\n  - scope: x\n", `unknown key "scope"`},
		"bad rule require":       {"modules:\n  - require: maybe\n", `unknown mode "maybe"`},
		"identity not a mapping": {"modules:\n  - identity: x\n", "identity must be a mapping"},
		"identity unknown key":   {"modules:\n  - identity:\n      email: a@b\n", `unknown key "email"`},
		"identity missing san":   {"modules:\n  - identity:\n      issuer: https://x\n", "san and issuer are both required"},
		"identity missing issuer": {"modules:\n  - identity:\n      san: \"*\"\n",
			"san and issuer are both required"},
		"yaml alias": {"a: &x 1\ndefault: *x\n", "forbidden YAML construct"},
		"duplicate rule prefixes": {"modules:\n  - prefix: a/b\n  - prefix: a/b\n    require: require-provenance\n",
			`duplicate prefix "a/b"`},
		"unparseable YAML":            {"{", "could not find flow mapping end"},
		"more than one document":      {"a: 1\n---\nb: 2\n", "exactly one YAML document"},
		"top level not a mapping":     {"- a\n", "top level must be a mapping"},
		"plugins not a list":          {"plugins: yes\n", "plugins must be a list"},
		"rule prefix not a string":    {"modules:\n  - prefix: [a]\n", "prefix must be a string"},
		"identity value not a string": {"modules:\n  - identity:\n      san: [a]\n", "identity.san must be a string"},
	}
	for name, c := range invalid {
		t.Run("invalid: "+name, func(t *testing.T) {
			_, err := Parse([]byte(c.data))
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("Parse = %v, want ErrInvalid containing %q", err, c.wantSub)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	p := &Policy{
		Default: AllowUnsigned,
		Modules: []Rule{
			{Prefix: "github.com/acme", Require: RequireProvenance},
			{Prefix: "github.com/acme/lab", Require: AllowUnsigned},
			{Prefix: "github.com/acme/critical", Identity: &IdentityRule{SAN: "*", Issuer: "https://x"}},
		},
	}

	t.Run("fallback to default when nothing matches", func(t *testing.T) {
		d := p.EvaluateModule("example.org/mod")
		if d.Require || d.Identity != nil {
			t.Fatalf("decision = %+v, want default allow", d)
		}
	})
	t.Run("prefix rule governs", func(t *testing.T) {
		if d := p.EvaluateModule("github.com/acme/widget"); !d.Require {
			t.Fatalf("decision = %+v, want require", d)
		}
	})
	t.Run("longest prefix wins", func(t *testing.T) {
		if d := p.EvaluateModule("github.com/acme/lab/x"); d.Require {
			t.Fatalf("decision = %+v, want the longer allow-unsigned rule", d)
		}
	})
	t.Run("longest prefix wins regardless of rule order", func(t *testing.T) {
		q := &Policy{Default: AllowUnsigned, Modules: []Rule{
			{Prefix: "a/b", Require: RequireProvenance},
			{Prefix: "a", Require: AllowUnsigned},
		}}
		if d := q.EvaluateModule("a/b/c"); !d.Require {
			t.Fatalf("decision = %+v: the longer rule listed first must still win", d)
		}
	})
	t.Run("rule without require inherits a require-provenance default", func(t *testing.T) {
		q := &Policy{Default: RequireProvenance, Modules: []Rule{
			{Prefix: "a", Identity: &IdentityRule{SAN: "*", Issuer: "https://x"}},
		}}
		d := q.EvaluateModule("a/b")
		if !d.Require {
			t.Fatalf("decision = %+v: an unset rule mode must inherit the strict default", d)
		}
	})
	t.Run("rule without require inherits the default", func(t *testing.T) {
		d := p.EvaluateModule("github.com/acme/critical/core")
		if d.Require {
			t.Fatalf("decision = %+v, want inherited allow-unsigned", d)
		}
		if d.Identity == nil || d.Identity.Issuer != "https://x" {
			t.Fatalf("identity = %+v, want the rule's identity", d.Identity)
		}
	})
	t.Run("prefix matching is segment-aware", func(t *testing.T) {
		// "github.com/acme" must not govern "github.com/acmecorp/x".
		if d := p.EvaluateModule("github.com/acmecorp/x"); d.Require {
			t.Fatalf("decision = %+v: prefix leaked across a segment boundary", d)
		}
	})
	t.Run("exact prefix match governs", func(t *testing.T) {
		if d := p.EvaluateModule("github.com/acme"); !d.Require {
			t.Fatalf("decision = %+v, want require for the exact match", d)
		}
	})
	t.Run("empty policy allows everything", func(t *testing.T) {
		var zero Policy
		if d := zero.EvaluateModule("any/path"); d.Require || d.Identity != nil {
			t.Fatalf("decision = %+v, want allow with no identity", d)
		}
	})
	t.Run("empty prefix is a catch-all", func(t *testing.T) {
		q := &Policy{Modules: []Rule{{Require: RequireProvenance}}}
		if d := q.EvaluateModule("anything"); !d.Require {
			t.Fatalf("decision = %+v, want the catch-all rule", d)
		}
	})
	t.Run("plugins evaluate their own list", func(t *testing.T) {
		q := &Policy{
			Default: RequireProvenance,
			Plugins: []Rule{{Prefix: "ghcr.io/acme", Require: AllowUnsigned}},
		}
		if d := q.EvaluatePlugin("ghcr.io/acme/protoc-gen"); d.Require {
			t.Fatalf("decision = %+v, want the plugin rule", d)
		}
		if d := q.EvaluatePlugin("docker.io/x"); !d.Require {
			t.Fatalf("decision = %+v, want default require", d)
		}
	})
	t.Run("plugin rules govern tagged and digested references", func(t *testing.T) {
		q := &Policy{
			Default: RequireProvenance,
			Plugins: []Rule{{Prefix: "ghcr.io/acme/gen", Require: AllowUnsigned}},
		}
		for _, ref := range []string{
			"ghcr.io/acme/gen:v1",
			"ghcr.io/acme/gen@sha256:abcd",
			"ghcr.io/acme/gen:v1@sha256:abcd",
		} {
			if d := q.EvaluatePlugin(ref); d.Require {
				t.Fatalf("EvaluatePlugin(%q) = %+v: the rule must govern the repository regardless of tag/digest", ref, d)
			}
		}
	})
	t.Run("registry ports are not tags", func(t *testing.T) {
		q := &Policy{
			Default: AllowUnsigned,
			Plugins: []Rule{{Prefix: "registry.local:5000/acme", Require: RequireProvenance}},
		}
		if d := q.EvaluatePlugin("registry.local:5000/acme/gen"); !d.Require {
			t.Fatalf("decision = %+v: the port colon must survive stripping", d)
		}
	})
	t.Run("digit-bearing tags strip cleanly", func(t *testing.T) {
		q := &Policy{
			Default: RequireProvenance,
			Plugins: []Rule{{Prefix: "ghcr.io/acme/gen", Require: AllowUnsigned}},
		}
		if d := q.EvaluatePlugin("ghcr.io/acme/gen:v10"); d.Require {
			t.Fatalf("decision = %+v: the tag boundary is the colon, nothing else", d)
		}
	})
	t.Run("a bare single-component reference evaluates without panic", func(t *testing.T) {
		q := &Policy{Default: RequireProvenance}
		if d := q.EvaluatePlugin("genplugin"); !d.Require {
			t.Fatalf("decision = %+v, want the default", d)
		}
	})
}

// TestEvaluateAlwaysPicksTheLongestMatch proves the governing-rule
// selection as a for-all property: over arbitrary rule sets and
// subjects, the decision always reflects a rule whose prefix matches
// and whose length no other matching rule exceeds, or the default when
// none matches.
func TestEvaluateAlwaysPicksTheLongestMatch(t *testing.T) {
	seg := rapid.SampledFrom([]string{"a", "b", "acme", "lab", "x"})
	pathGen := rapid.Custom(func(rt *rapid.T) string {
		n := rapid.IntRange(1, 4).Draw(rt, "segs")
		parts := make([]string, n)
		for i := range parts {
			parts[i] = seg.Draw(rt, "seg")
		}
		return strings.Join(parts, "/")
	})
	rapid.Check(t, func(rt *rapid.T) {
		var rules []Rule
		for i, n := 0, rapid.IntRange(0, 5).Draw(rt, "rules"); i < n; i++ {
			mode := Mode("")
			if rapid.Bool().Draw(rt, "hasMode") {
				mode = rapid.SampledFrom([]Mode{AllowUnsigned, RequireProvenance}).Draw(rt, "mode")
			}
			rules = append(rules, Rule{Prefix: pathGen.Draw(rt, "prefix"), Require: mode})
		}
		p := &Policy{Default: AllowUnsigned, Modules: rules}
		subject := pathGen.Draw(rt, "subject")
		d := p.EvaluateModule(subject)

		// Recompute the expectation independently.
		bestLen, bestMode := -1, Mode("")
		for _, r := range rules {
			if !(r.Prefix == "" || subject == r.Prefix || (strings.HasPrefix(subject, r.Prefix+"/"))) {
				continue
			}
			if len(r.Prefix) > bestLen {
				bestLen, bestMode = len(r.Prefix), r.Require
			}
		}
		want := false
		if bestLen >= 0 && bestMode != "" {
			want = bestMode == RequireProvenance
		}
		if d.Require != want {
			t.Fatalf("Require = %v, want %v (subject %q, rules %+v)", d.Require, want, subject, rules)
		}
	})
}
