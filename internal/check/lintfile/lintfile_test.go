package lintfile

import (
	"errors"
	"strings"
	"testing"

	"github.com/greatliontech/glob"
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
modules:
  legacy/api:
    enable: [HOUSE_ONE]
    severity:
      HOUSE_ONE: warning
  fresh: {}
  .: {}
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
	if legacy, fresh := f.Modules["legacy/api"], f.Modules["fresh"]; len(f.Modules) != 3 || f.Modules["."].Enable != nil || strings.Join(legacy.Enable, ",") != "HOUSE_ONE" || legacy.Severity["HOUSE_ONE"] != check.SeverityWarning || fresh.Enable != nil || fresh.Exclude != nil || fresh.Severity != nil {
		t.Fatalf("modules = %+v", f.Modules)
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
		"modules not a map":  {"modules: [a]\n", "modules must be a mapping"},
		"modules bad dir":    {"modules:\n  ../x: {}\n", "modules:"},
		"modules abs dir":    {"modules:\n  /a: {}\n", "modules:"},
		"modules unclean":    {"modules:\n  ./a: {}\n", "modules:"},
		"modules trailing":   {"modules:\n  a/: {}\n", "modules:"},
		"modules unknown":    {"modules:\n  a:\n    ignore: []\n", `modules.a: unknown key "ignore"`},
		"modules severity":   {"modules:\n  a:\n    severity:\n      X: loud\n", "modules.a.severity.X must be error or warning"},
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
	std := Ruleset{Path: "example.com/std", Files: []rules.Located{located("a.rules.yaml", rule("FIELD_NAMES", "lint", "STANDARD"), rule("ENUM_NAMES", "lint", "STANDARD"), rule("NO_DELETE", "breaking", "STANDARD"))}}
	house := Ruleset{Path: "example.com/house", Files: []rules.Located{located("h.rules.yaml", rule("HOUSE_ONE", "lint"), rule("HOUSE_TWO", "lint", "extra"))}}
	sel, err := Select(f, []Ruleset{std, house})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		name := c.rule
		if name != "ANY" {
			name = "example.com/std:" + name
		}
		if got := sel.Ignored(c.path, name); got != c.want {
			t.Errorf("Ignored(%q, %s) = %v", c.path, name, got)
		}
	}
	// A glob matching everything still ignores no finding without a
	// path: a package or set finding is suppressed by selection alone.
	every, err := Parse([]byte("ignore:\n  - paths: [\"**\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	all, err := Select(every, []Ruleset{std})
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
// by name, id or tag, less the excluded, with severities overridden,
// in ruleset, file and declaration order; a spelling no rule
// declares, an ambiguous one, a ruleset declaring an id twice or as
// both an id and a tag, and two severity spellings of one rule, fail
// naming them (REQ-lint-selection).
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
	if err != nil || ids(all.Rules) != "FIELD_NAMES:error ENUM_NAMES:error NO_DELETE:error HOUSE_ONE:error HOUSE_TWO:error" {
		t.Fatalf("all: %s %v", ids(all.Rules), err)
	}
	f, err := Parse([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	sel, err := Select(f, []Ruleset{std, house})
	if err != nil || ids(sel.Rules) != "ENUM_NAMES:warning NO_DELETE:error HOUSE_ONE:error" {
		t.Fatalf("selected: %s %v", ids(sel.Rules), err)
	}
	none, err := Select(&File{Enable: []string{}}, []Ruleset{std})
	if err != nil || len(none.Rules) != 0 || none.Rules == nil {
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
	// Two rulesets may declare one id: each rule bears its name, a
	// bare spelling of the shared id is ambiguous and names the
	// candidates, a qualified spelling — of a rule or a tag — names
	// its ruleset's alone, and severity and ignore take the canonical
	// name (the rule name term).
	dup := Ruleset{Path: "example.com/dup", Files: []rules.Located{located("d.rules.yaml", rule("FIELD_NAMES", "lint", "STANDARD"))}}
	names := func(rs []rules.Rule) string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Name()+":"+string(r.Severity))
		}
		return strings.Join(out, " ")
	}
	both, err := Select(&File{}, []Ruleset{std, dup})
	if err != nil || names(both.Rules) != "example.com/std:FIELD_NAMES:error example.com/std:ENUM_NAMES:error example.com/std:NO_DELETE:error example.com/dup:FIELD_NAMES:error" {
		t.Fatalf("two rulesets: %s %v", names(both.Rules), err)
	}
	for name, f := range map[string]*File{
		"enable bare":   {Enable: []string{"FIELD_NAMES"}},
		"enable tag":    {Enable: []string{"STANDARD"}},
		"exclude bare":  {Exclude: []string{"FIELD_NAMES"}},
		"severity bare": {Severity: map[string]check.Severity{"FIELD_NAMES": check.SeverityWarning}},
		"ignore bare":   {Ignore: []Ignore{{Paths: []*glob.Pattern{glob.MustCompile("x/**")}, Rules: []string{"FIELD_NAMES"}}}},
		"severity tag":  {Severity: map[string]check.Severity{"example.com/std:STANDARD": check.SeverityWarning}},
		"ignore tag":    {Ignore: []Ignore{{Paths: []*glob.Pattern{glob.MustCompile("x/**")}, Rules: []string{"example.com/std:STANDARD"}}}},
		"unknown name":  {Enable: []string{"example.com/std:NOPE"}},
	} {
		_, err := Select(f, []Ruleset{std, dup})
		switch {
		case err == nil || !errors.Is(err, ErrSelection):
			t.Errorf("%s: %v", name, err)
		case strings.HasSuffix(name, "bare") && !strings.Contains(err.Error(), "example.com/std:FIELD_NAMES, example.com/dup:FIELD_NAMES"):
			t.Errorf("%s names no candidates: %v", name, err)
		case name == "enable tag" && !strings.Contains(err.Error(), "example.com/std:STANDARD, example.com/dup:STANDARD"):
			t.Errorf("%s names no candidates: %v", name, err)
		case strings.HasSuffix(name, "tag") && name != "enable tag" && !strings.Contains(err.Error(), "a tag where a rule is named"):
			t.Errorf("%s: %v", name, err)
		}
	}
	qualified := &File{Enable: []string{"example.com/dup:STANDARD", "ENUM_NAMES"}, Exclude: []string{"example.com/std:NO_DELETE"}, Severity: map[string]check.Severity{"example.com/dup:FIELD_NAMES": check.SeverityWarning}, Ignore: []Ignore{{Paths: []*glob.Pattern{glob.MustCompile("x/**")}, Rules: []string{"example.com/std:FIELD_NAMES", "ENUM_NAMES"}}}}
	sel, err = Select(qualified, []Ruleset{std, dup})
	if err != nil || names(sel.Rules) != "example.com/std:ENUM_NAMES:error example.com/dup:FIELD_NAMES:warning" {
		t.Fatalf("qualified: %s %v", names(sel.Rules), err)
	}
	if !sel.Ignored("x/a.proto", "example.com/std:ENUM_NAMES") || !sel.Ignored("x/a.proto", "example.com/std:FIELD_NAMES") || sel.Ignored("x/a.proto", "example.com/dup:FIELD_NAMES") {
		t.Fatal("ignore matches by canonical name")
	}
	if got := qualified.Ignore[0].Rules; strings.Join(got, " ") != "example.com/std:FIELD_NAMES ENUM_NAMES" {
		t.Fatalf("the file's own spellings changed: %v", got)
	}
	// A module's own selection replaces the root's for that module
	// alone, resolved over the same imports and spelled by its entry.
	own := &File{Enable: []string{"STANDARD"}, Modules: map[string]ModuleSelection{"legacy": {Enable: []string{"HOUSE_ONE"}, Severity: map[string]check.Severity{"HOUSE_ONE": check.SeverityWarning}}, "fresh": {}}}
	sel, err = Select(own, []Ruleset{std, house})
	if err != nil || ids(sel.Rules) != "FIELD_NAMES:error ENUM_NAMES:error NO_DELETE:error" || ids(sel.Modules["legacy"]) != "HOUSE_ONE:warning" || ids(sel.Modules["fresh"]) != "FIELD_NAMES:error ENUM_NAMES:error NO_DELETE:error HOUSE_ONE:error HOUSE_TWO:error" {
		t.Fatalf("per module: %s | %s | %s %v", ids(sel.Rules), ids(sel.Modules["legacy"]), ids(sel.Modules["fresh"]), err)
	}
	if ids(sel.RulesFor("legacy")) != "HOUSE_ONE:warning" || ids(sel.RulesFor("other")) != ids(sel.Rules) {
		t.Fatalf("RulesFor: %s %s", ids(sel.RulesFor("legacy")), ids(sel.RulesFor("other")))
	}
	if _, err := Select(&File{Modules: map[string]ModuleSelection{"legacy": {Enable: []string{"NOPE"}}}}, []Ruleset{std}); err == nil || !strings.Contains(err.Error(), `modules.legacy.enable names "NOPE"`) {
		t.Fatalf("a module's unknown spelling: %v", err)
	}
	if _, err := Select(&File{Modules: map[string]ModuleSelection{"legacy": {Severity: map[string]check.Severity{"STANDARD": check.SeverityWarning}}}}, []Ruleset{std}); err == nil || !strings.Contains(err.Error(), `modules.legacy.severity names "STANDARD"`) {
		t.Fatalf("a module's severity naming a tag: %v", err)
	}
	// A ruleset declaring a name twice, in two files, or as both a
	// rule and a tag; and two severity spellings of one rule.
	twice := Ruleset{Path: "example.com/twice", Files: []rules.Located{located("a.rules.yaml", rule("X", "lint")), located("b.rules.yaml", rule("X", "lint"))}}
	if _, err := Select(&File{}, []Ruleset{twice}); err == nil || !errors.Is(err, ErrSelection) || !strings.Contains(err.Error(), "example.com/twice:X declared by example.com/twice/a.rules.yaml and example.com/twice/b.rules.yaml") {
		t.Fatalf("twice: %v", err)
	}
	clash := Ruleset{Path: "example.com/clash", Files: []rules.Located{located("a.rules.yaml", rule("STANDARD", "lint"), rule("OTHER", "lint", "STANDARD"))}}
	if _, err := Select(&File{}, []Ruleset{clash}); err == nil || !errors.Is(err, ErrSelection) || !strings.Contains(err.Error(), "example.com/clash:STANDARD is both a rule, declared by example.com/clash/a.rules.yaml, and a tag, first carried by a rule of example.com/clash/a.rules.yaml") {
		t.Fatalf("rule and tag: %v", err)
	}
	if _, err := Select(&File{Severity: map[string]check.Severity{"ENUM_NAMES": check.SeverityError, "example.com/std:ENUM_NAMES": check.SeverityWarning}}, []Ruleset{std}); err == nil || !errors.Is(err, ErrSelection) || !strings.Contains(err.Error(), `severity names example.com/std:ENUM_NAMES twice, as "ENUM_NAMES" and "example.com/std:ENUM_NAMES"`) {
		t.Fatalf("severity twice: %v", err)
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
