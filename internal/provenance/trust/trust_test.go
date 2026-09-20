package trust

import (
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

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
		"default not a string":   {"default: [a]\n", "default must be one line of text"},
		"modules not a list":     {"modules: yes\n", "modules must be a list"},
		"rule not a mapping":     {"modules:\n  - prefix\n", "modules[0] must be a mapping"},
		"unknown rule key":       {"modules:\n  - scope: x\n", `unknown key "scope"`},
		"bad rule require":       {"modules:\n  - require: maybe\n", `unknown mode "maybe"`},
		"identity not a mapping": {"modules:\n  - identity: x\n", "identity must be a mapping"},
		"identity unknown key":   {"modules:\n  - identity:\n      email: a@b\n", `unknown key "email"`},
		"identity missing san":   {"modules:\n  - identity:\n      issuer: https://x\n", "identity: missing san"},
		"identity missing issuer": {"modules:\n  - identity:\n      san: \"*\"\n",
			"identity: missing issuer"},
		"yaml alias": {"a: &x 1\ndefault: *x\n", "forbidden YAML construct"},
		"duplicate rule prefixes": {"modules:\n  - prefix: a/b\n  - prefix: a/b\n    require: require-provenance\n",
			`duplicate prefix "a/b"`},
		"unparseable YAML":                {"{", "could not find flow mapping end"},
		"more than one document":          {"a: 1\n---\nb: 2\n", "exactly one YAML document"},
		"top level not a mapping":         {"- a\n", "top level must be a mapping"},
		"plugins not a list":              {"plugins: yes\n", "plugins must be a list"},
		"rule prefix not a string":        {"modules:\n  - prefix: [a]\n", "prefix must be one line of text"},
		"rule prefix literal block":       {"modules:\n  - prefix: |\n      github.com/a\n", "prefix must be one line of text"},
		"limit not a line":                {"execution:\n  limits:\n    memory: [512]\n", "execution.limits.memory must be one line of text"},
		"unknown limit not a line":        {"execution:\n  limits:\n    disk: [1Gi]\n", `execution.limits: unknown key "disk"`},
		"identity san literal block":      {"modules:\n  - identity:\n      san: |\n        a\n", "identity.san must be one non-empty line of text"},
		"identity unknown key not a line": {"modules:\n  - identity:\n      email: [a]\n", `identity: unknown key "email"`},
		"identity value not a string":     {"modules:\n  - identity:\n      san: [a]\n", "identity.san must be one non-empty line of text"},
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

// The execution block parses every key, accessors fold the defaults, and
// scheme permission answers exactly the listed schemes
// (REQ-prov-exec-policy).
func TestParseExecution(t *testing.T) {
	in := `
execution:
  min-tier: Minimal
  schemes: [oci, local]
  local-pin: false
  plugin-overrides: false
  limits:
    memory: 512Mi
    cpu: "1.5"
    pids: 64
    timeout: 30s
`
	p, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	e := &p.Execution
	if e.EffectiveMinTier() != TierMinimal {
		t.Errorf("min tier %q", e.EffectiveMinTier())
	}
	if !e.SchemeAllowed(SchemeLocal) || !e.SchemeAllowed(SchemeOCI) {
		t.Error("listed schemes not allowed")
	}
	if e.LocalPinEnabled() || e.OverridesAllowed() {
		t.Error("explicit false read as true")
	}
	if e.Limits.Memory != 512<<20 || e.Limits.CPU != 1.5 || e.Limits.Pids != 64 || e.Limits.Timeout != 30*time.Second {
		t.Errorf("limits = %+v", e.Limits)
	}
}

// The zero posture is the default posture: Strong floor, oci only,
// pinning and overrides on, no limit overrides.
func TestExecutionDefaults(t *testing.T) {
	for _, in := range []string{"", "execution: {}\n", "execution:\n  schemes: [oci]\n"} {
		p, err := Parse([]byte(in))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		e := &p.Execution
		if e.EffectiveMinTier() != TierStrong {
			t.Errorf("%q: tier %q", in, e.EffectiveMinTier())
		}
		if !e.SchemeAllowed(SchemeOCI) || e.SchemeAllowed(SchemeLocal) || e.SchemeAllowed("remote") {
			t.Errorf("%q: scheme defaults wrong", in)
		}
		if !e.LocalPinEnabled() || !e.OverridesAllowed() {
			t.Errorf("%q: boolean defaults wrong", in)
		}
		if e.Limits != (Limits{}) {
			t.Errorf("%q: limits set: %+v", in, e.Limits)
		}
	}
}

func TestParseExecutionRejections(t *testing.T) {
	cases := []struct{ name, in, msg string }{
		{"not a mapping", "execution: strict\n", "must be a mapping"},
		{"unknown key", "execution:\n  runner: docker\n", `unknown key "runner"`},
		{"bad tier", "execution:\n  min-tier: strong\n", "one of Strong"},
		{"reserved scheme", "execution:\n  schemes: [remote]\n", "oci or local"},
		{"duplicate scheme", "execution:\n  schemes: [oci, oci]\n", `lists "oci" twice`},
		{"non-bool pin", "execution:\n  local-pin: yes\n", "must be true or false"},
		{"capitalized bool", "execution:\n  local-pin: True\n", "must be true or false"},
		{"non-bool overrides", "execution:\n  plugin-overrides: 1\n", "must be true or false"},
		{"unknown limit", "execution:\n  limits:\n    disk: 1Gi\n", `unknown key "disk"`},
		{"zero memory", "execution:\n  limits:\n    memory: 0\n", "not positive"},
		{"bad memory suffix", "execution:\n  limits:\n    memory: 512Ti\n", "not a positive integer"},
		{"negative pids", "execution:\n  limits:\n    pids: -1\n", "not a positive integer"},
		{"cpu exponent", "execution:\n  limits:\n    cpu: 1e2\n", "not a plain decimal"},
		{"timeout no unit", "execution:\n  limits:\n    timeout: 30\n", "no s/m/h unit"},
		{"timeout bad unit", "execution:\n  limits:\n    timeout: 30d\n", "no s/m/h unit"},
		{"memory overflow", "execution:\n  limits:\n    memory: 99999999999999999999\n", "overflows"},
		{"schemes not a list", "execution:\n  schemes: oci\n", "must be a list"},
		{"limits not a mapping", "execution:\n  limits: 5\n", "must be a mapping"},
		{"empty pids", "execution:\n  limits:\n    pids: \"\"\n", "empty value"},
		{"empty timeout", "execution:\n  limits:\n    timeout: \"\"\n", "empty value"},
		{"colon digit", "execution:\n  limits:\n    pids: \"1:\"\n", "not a positive integer"},
		{"trailing dot cpu", "execution:\n  limits:\n    cpu: \"1.\"\n", "not a plain decimal"},
		{"colon cpu", "execution:\n  limits:\n    cpu: \"1:\"\n", "not a plain decimal"},
		{"zero cpu", "execution:\n  limits:\n    cpu: \"0\"\n", "not a positive decimal"},
		{"double memory suffix", "execution:\n  limits:\n    memory: 1MiKi\n", "not a positive integer"},
		{"exact overflow boundary", "execution:\n  limits:\n    memory: 18446744073709551616\n", "overflows"},
		{"suffix overflow", "execution:\n  limits:\n    memory: 18446744073709551615Gi\n", "overflows"},
		{"timeout bad decimal", "execution:\n  limits:\n    timeout: \"1.h\"\n", "not a plain decimal"},
		{"mid-window suffix overflow", "execution:\n  limits:\n    memory: 17179869184Gi\n", "overflows"},
		{"leading junk cpu", "execution:\n  limits:\n    cpu: \"x5\"\n", "not a plain decimal"},
		{"signed cpu", "execution:\n  limits:\n    cpu: \"+5\"\n", "not a plain decimal"},
	}
	for _, tc := range cases {
		_, err := Parse([]byte(tc.in))
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: err = %v, want ErrInvalid with %q", tc.name, err, tc.msg)
		}
	}
}

// A timeout spelling past Duration's int64 range is rejected, never
// wrapped negative.
func TestTimeoutOverflowRejected(t *testing.T) {
	_, err := Parse([]byte("execution:\n  limits:\n    timeout: 99999999999999h\n"))
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "overflows") {
		t.Fatalf("err = %v, want overflow rejection", err)
	}
	// The largest representable hour count still parses positive.
	p, err := Parse([]byte("execution:\n  limits:\n    timeout: 2562047h\n"))
	if err != nil || p.Execution.Limits.Timeout <= 0 {
		t.Fatalf("in-range timeout: %v %v", p, err)
	}
}

// Every tier name is accepted; explicit-true booleans stay true; each
// limit grammar's valid spellings land on the declared values.
func TestExecutionValidSpellings(t *testing.T) {
	for _, tier := range []string{TierStrong, TierOS, TierMinimal, TierNone} {
		p, err := Parse([]byte("execution:\n  min-tier: " + tier + "\n"))
		if err != nil || p.Execution.EffectiveMinTier() != tier {
			t.Errorf("tier %s: %v", tier, err)
		}
	}
	p, err := Parse([]byte("execution:\n  local-pin: true\n  plugin-overrides: true\n"))
	if err != nil || !p.Execution.LocalPinEnabled() || !p.Execution.OverridesAllowed() {
		t.Fatalf("explicit true read as false (%v)", err)
	}
	for in, want := range map[string]uint64{
		"512":                  512,
		"1Ki":                  1 << 10,
		"3Mi":                  3 << 20,
		"2Gi":                  2 << 30,
		"18446744073709551615": 1<<64 - 1, // exactly max: the overflow guard must not fire
	} {
		p, err := Parse([]byte("execution:\n  limits:\n    memory: " + in + "\n"))
		if err != nil || p.Execution.Limits.Memory != want {
			t.Errorf("memory %s = %d, %v; want %d", in, p.Execution.Limits.Memory, err, want)
		}
	}
	// Non-dyadic decimal: exact float64 equality separates the 64-bit
	// parse from a float32 detour (0.1 is not float32-representable).
	p2, err := Parse([]byte("execution:\n  limits:\n    cpu: \"0.1\"\n"))
	if err != nil || p2.Execution.Limits.CPU != 0.1 {
		t.Fatalf("cpu 0.1 = %v, %v; want exactly 0.1", p2.Execution.Limits.CPU, err)
	}
	for in, want := range map[string]time.Duration{
		"45s": 45 * time.Second,
		"2m":  2 * time.Minute,
		"1h":  time.Hour,
	} {
		p, err := Parse([]byte("execution:\n  limits:\n    timeout: " + in + "\n"))
		if err != nil || p.Execution.Limits.Timeout != want {
			t.Errorf("timeout %s = %v, %v; want %v", in, p.Execution.Limits.Timeout, err, want)
		}
	}
}

// EffectiveLimits folds each declared default independently and
// leaves set fields alone; every field of the result is bounded.
func TestEffectiveLimits(t *testing.T) {
	got := (&Execution{}).EffectiveLimits()
	if got.Memory != DefaultMemoryBytes || got.Pids != DefaultPids || got.Timeout != DefaultTimeout || got.CPU != float64(runtime.NumCPU()) {
		t.Fatalf("zero limits = %+v", got)
	}
	set := Limits{Memory: 1 << 20, CPU: 2, Pids: 3, Timeout: time.Second}
	if got := (&Execution{Limits: set}).EffectiveLimits(); got != set {
		t.Fatalf("set limits changed: %+v", got)
	}
	partial := (&Execution{Limits: Limits{Pids: 9}}).EffectiveLimits()
	if partial.Pids != 9 || partial.Memory != DefaultMemoryBytes || partial.Timeout != DefaultTimeout || partial.CPU <= 0 {
		t.Fatalf("partial limits = %+v", partial)
	}
}

// A policy value is one line of text in any YAML scalar spelling: a
// folded block that folds to a line, a quoted string, a typed spelling
// read as the text written (module-file.md REQ-modfile-acceptance's
// family rule; provenance.md REQ-prov-trust-schema).
func TestParseTakesEveryLineSpelling(t *testing.T) {
	in := "default: 'allow-unsigned'\nmodules:\n  - prefix: >-\n      github.com/\n      acme\n    require: \"require-provenance\"\n    identity:\n      san: >-\n        https://x/*\n      issuer: 1\n"
	p, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if p.Default != AllowUnsigned || len(p.Modules) != 1 {
		t.Fatalf("policy = %+v", p)
	}
	r := p.Modules[0]
	if r.Prefix != "github.com/ acme" || r.Require != RequireProvenance || r.Identity == nil || r.Identity.SAN != "https://x/*" || r.Identity.Issuer != "1" {
		t.Fatalf("rule = %+v identity = %+v", r, r.Identity)
	}
}
