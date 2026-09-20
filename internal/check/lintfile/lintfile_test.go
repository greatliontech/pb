package lintfile

import (
	"errors"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/check/rules"
	"github.com/greatliontech/pb/internal/module/modfile"
	"github.com/greatliontech/pb/internal/module/workspace"
	"github.com/greatliontech/pb/internal/proto/modfiles"
)

const full = `rulesets:
  - example.com/std
  - example.com/house
enable:
  - STANDARD
  - HOUSE_ONE
exclude:
  - FIELD_NAMES
severity:
  ENUM_NAMES: warning
ignore:
  - paths: ["vendor/**", "legacy/*.proto"]
    rules: [ENUM_NAMES]
  - paths: ["gen/**"]
breaking:
  base:
    ref: main
`

// The lint file parses to its parts, each optional, an absent enable
// meaning every rule and an ignore without rules every rule; every
// schema departure is refused naming it (REQ-lint-config-schema).
func TestParse(t *testing.T) {
	f, err := Parse([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.Rulesets, ",") != "example.com/std,example.com/house" || strings.Join(f.Enable, ",") != "STANDARD,HOUSE_ONE" || strings.Join(f.Exclude, ",") != "FIELD_NAMES" {
		t.Fatalf("file = %+v", f)
	}
	if f.Severity["ENUM_NAMES"] != check.SeverityWarning || len(f.Ignore) != 2 || len(f.Ignore[0].Paths) != 2 || f.Ignore[1].Rules != nil || f.Breaking == nil || f.Breaking.Base != (Base{Form: BaseRef, Value: "main"}) || f.Breaking.Base.String() != "ref main" {
		t.Fatalf("file = %+v", f)
	}
	empty, err := Parse([]byte(""))
	if err != nil || empty.Enable != nil || empty.Breaking != nil || len(empty.Rulesets) != 0 {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	for in, want := range map[string]Base{
		"breaking:\n  base:\n    version: v1.2.0\n": {Form: BaseVersion, Value: "v1.2.0"},
		"breaking:\n  base:\n    pinned: true\n":    {Form: BasePinned},
	} {
		f, err := Parse([]byte(in))
		if err != nil || f.Breaking.Base != want || f.Breaking.Base.String() != strings.TrimSpace(string(want.Form)+" "+want.Value) {
			t.Errorf("%q: %+v %v", in, f.Breaking, err)
		}
	}
	cases := map[string]struct{ in, want string }{
		"unknown key":        {"rules: []\n", `unknown key "rules"`},
		"rulesets not list":  {"rulesets: example.com/x\n", "rulesets must be a list"},
		"ruleset empty":      {"rulesets: [\"\"]\n", "rulesets must hold non-empty lines of text"},
		"ruleset bad path":   {"rulesets: [\"not a path\"]\n", "rulesets:"},
		"ruleset twice":      {"rulesets: [example.com/x, example.com/x]\n", "listed twice"},
		"enable not list":    {"enable: X\n", "enable must be a list"},
		"severity not map":   {"severity: [X]\n", "severity must be a mapping"},
		"severity bad":       {"severity:\n  X: info\n", "severity.X must be error or warning"},
		"ignore not list":    {"ignore: {}\n", "ignore must be a list"},
		"ignore not mapping": {"ignore:\n  - x\n", "ignore[0] must be a mapping"},
		"ignore unknown key": {"ignore:\n  - paths: [a]\n    files: [b]\n", `ignore[0]: unknown key "files"`},
		"ignore no paths":    {"ignore:\n  - rules: [X]\n", "ignore[0] has no paths"},
		"ignore paths empty": {"ignore:\n  - paths: []\n", "ignore[0].paths must not be empty"},
		"ignore rules empty": {"ignore:\n  - paths: [a]\n    rules: []\n", "ignore[0].rules must not be empty"},
		"base bad version":   {"breaking:\n  base:\n    version: nonsense\n", "breaking.base.version:"},
		"ignore bad glob":    {"ignore:\n  - paths: [\"[\"]\n", "ignore[0].paths:"},
		"breaking not map":   {"breaking: main\n", "breaking must be a mapping"},
		"breaking no base":   {"breaking: {}\n", "breaking: missing base"},
		"breaking unknown":   {"breaking:\n  base: {ref: main}\n  other: 1\n", `breaking: unknown key "other"`},
		"base two forms":     {"breaking:\n  base:\n    ref: main\n    version: v1.0.0\n", "exactly one of ref, version, pinned"},
		"base none":          {"breaking:\n  base: {}\n", "exactly one of ref, version, pinned"},
		"base unknown":       {"breaking:\n  base:\n    tag: v1\n", `breaking.base: unknown key "tag"`},
		"base pinned false":  {"breaking:\n  base:\n    pinned: false\n", "breaking.base.pinned must be true"},
		"base pinned quoted": {"breaking:\n  base:\n    pinned: \"true\"\n", "breaking.base.pinned must be true"},
		"base pinned yes":    {"breaking:\n  base:\n    pinned: yes\n", "breaking.base.pinned must be true"},
		"base ref empty":     {"breaking:\n  base:\n    ref: \"\"\n", "breaking.base.ref must be a non-empty string"},
		"anchor":             {"enable: &a [X]\n", "forbidden YAML construct"},
	}
	for name, c := range cases {
		_, err := Parse([]byte(c.in))
		if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want ErrInvalid naming %q", name, err, c.want)
		}
	}
}

// A finding is ignored under a matching glob when the entry names its
// rule or none; a finding without a path never is.
func TestIgnored(t *testing.T) {
	f, err := Parse([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path, rule string
		want       bool
	}{
		{"vendor/a/b.proto", "ENUM_NAMES", true},
		{"vendor/a/b.proto", "FIELD_NAMES", false},
		{"legacy/x.proto", "ENUM_NAMES", true},
		{"legacy/sub/x.proto", "ENUM_NAMES", false},
		{"gen/x.proto", "ANY", true},
		{"gen/deep/x.proto", "ANY", true},
		{"src/x.proto", "ENUM_NAMES", false},
		{"", "ANY", false},
	}
	for _, c := range cases {
		if got := f.Ignored(c.path, c.rule); got != c.want {
			t.Errorf("Ignored(%q, %s) = %v", c.path, c.rule, got)
		}
	}
	// A glob matching everything still ignores no finding without a
	// path: a package or set finding is suppressed by selection alone.
	all, err := Parse([]byte("ignore:\n  - paths: [\"**\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if all.Ignored("", "ANY") || !all.Ignored("x.proto", "ANY") {
		t.Error("the path-less finding under **")
	}
}

func rule(id, kind string, tags ...string) rules.Rule {
	return rules.Rule{ID: id, Kind: check.Kind(kind), Target: check.TargetField, Severity: check.SeverityError, Tags: tags, CEL: "true", Message: id}
}

func located(path string, rs ...rules.Rule) rules.Located {
	return rules.Located{Path: path, File: &rules.File{CELEnv: 1, Rules: rs}}
}

// Selection: every rule when enable is absent, else the ones named
// by id or tag, less the excluded, with severities overridden, in
// ruleset, file and declaration order; a name no rule declares, and
// an id declared twice, fail naming them (REQ-lint-selection).
func TestSelect(t *testing.T) {
	std := Ruleset{Path: "example.com/std", Files: []rules.Located{
		located("a.rules.yaml", rule("FIELD_NAMES", "lint", "STANDARD"), rule("ENUM_NAMES", "lint", "STANDARD")),
		located("b.rules.yaml", rule("NO_DELETE", "breaking", "STANDARD")),
	}}
	house := Ruleset{Path: "example.com/house", Files: []rules.Located{located("h.rules.yaml", rule("HOUSE_ONE", "lint"), rule("HOUSE_TWO", "lint", "extra"))}}
	ids := func(rs []rules.Rule) string {
		var out []string
		for _, r := range rs {
			out = append(out, r.ID+":"+string(r.Severity))
		}
		return strings.Join(out, " ")
	}
	all, err := Select(&File{}, []Ruleset{std, house})
	if err != nil || ids(all) != "FIELD_NAMES:error ENUM_NAMES:error NO_DELETE:error HOUSE_ONE:error HOUSE_TWO:error" {
		t.Fatalf("all: %s %v", ids(all), err)
	}
	f, err := Parse([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	sel, err := Select(f, []Ruleset{std, house})
	if err != nil || ids(sel) != "ENUM_NAMES:warning NO_DELETE:error HOUSE_ONE:error" {
		t.Fatalf("selected: %s %v", ids(sel), err)
	}
	none, err := Select(&File{Enable: []string{}}, []Ruleset{std})
	if err != nil || len(none) != 0 || none == nil {
		t.Fatalf("enable empty: %v %v", none, err)
	}
	for name, f := range map[string]*File{
		"enable unknown":   {Enable: []string{"NOPE"}},
		"exclude unknown":  {Exclude: []string{"nope"}},
		"severity unknown": {Severity: map[string]check.Severity{"STANDARD": check.SeverityWarning}}, // a tag is no id
	} {
		if _, err := Select(f, []Ruleset{std, house}); err == nil || !errors.Is(err, ErrSelection) {
			t.Errorf("%s: %v", name, err)
		}
	}
	dup := Ruleset{Path: "example.com/dup", Files: []rules.Located{located("d.rules.yaml", rule("FIELD_NAMES", "lint"))}}
	if _, err := Select(&File{}, []Ruleset{std, dup}); err == nil || !errors.Is(err, ErrSelection) || !strings.Contains(err.Error(), "rule FIELD_NAMES declared by example.com/std/a.rules.yaml and example.com/dup/d.rules.yaml") {
		t.Fatalf("duplicate: %v", err)
	}
}

// A ruleset is a workspace module or a dependency a workspace module
// declares, among the build's modules, its rule files read; anything
// else, and a bad rule file, fail naming the ruleset (REQ-lint-
// rulesets-declared, REQ-rules-file-discovery).
func TestRulesets(t *testing.T) {
	root := &workspace.Root{Modules: []workspace.Module{
		{Dir: "a", File: &modfile.File{Module: "example.com/a", Deps: map[string]string{"example.com/std": "v1.0.0"}}},
		{Dir: "lib", File: &modfile.File{Module: "example.com/lib"}},
	}}
	good := []byte("celEnv: 1\nrules:\n  - id: X\n    kind: lint\n    target: field\n    severity: error\n    cel: \"true\"\n    message: m\n")
	mods := []modfiles.Module{
		{Path: "example.com/a", Local: true},
		{Path: "example.com/lib", Local: true, Rules: map[string][]byte{"lib.rules.yaml": good}},
		{Path: "example.com/std", Version: "v1.0.0", Rules: map[string][]byte{"z.rules.yaml": good, "a/b.rules.yaml": []byte("celEnv: 1\nrules: []\n")}},
		{Path: "example.com/transitive", Version: "v1.0.0", Rules: map[string][]byte{"t.rules.yaml": good}},
		{Path: "example.com/bad", Version: "v1.0.0", Rules: map[string][]byte{"bad.rules.yaml": []byte("celEnv: 9\nrules: []\n")}},
	}
	sets, err := Rulesets(&File{Rulesets: []string{"example.com/std", "example.com/lib"}}, root, mods)
	if err != nil || len(sets) != 2 || sets[0].Path != "example.com/std" || len(sets[0].Files) != 2 || sets[0].Files[0].Path != "a/b.rules.yaml" || len(sets[1].Files) != 1 {
		t.Fatalf("rulesets: %+v %v", sets, err)
	}
	for name, path := range map[string]string{"transitive": "example.com/transitive", "absent": "example.com/nowhere"} {
		_, err := Rulesets(&File{Rulesets: []string{path}}, root, mods)
		if err == nil || !errors.Is(err, ErrRuleset) || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Declared but not in the build's modules: refused too.
	root.Modules[0].File.Deps["example.com/bad"] = "v1.0.0"
	root.Modules[0].File.Deps["example.com/missing"] = "v1.0.0"
	if _, err := Rulesets(&File{Rulesets: []string{"example.com/missing"}}, root, mods); err == nil || !errors.Is(err, ErrRuleset) {
		t.Fatalf("declared but absent from the build: %v", err)
	}
	_, err = Rulesets(&File{Rulesets: []string{"example.com/bad"}}, root, mods)
	if err == nil || !errors.Is(err, ErrRuleset) || !errors.Is(err, rules.ErrEnvironment) || !strings.Contains(err.Error(), "example.com/bad: bad.rules.yaml") {
		t.Fatalf("bad rule file: %v", err)
	}
}
