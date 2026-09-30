package lintfile

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing/fstest"

	"errors"
	"strings"
	"testing"

	"github.com/greatliontech/glob"
	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/check/rules"
	"github.com/greatliontech/pb/internal/module/archive"
	"github.com/greatliontech/pb/internal/module/modfile"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/module/workspace"
)

const full = `rulesets:
  - path: example.com/std
    version: v1.0.0
    alias: std
  - path: example.com/house
    alias: house
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
  - paths: ["wire/**"]
    kind: breaking
modules:
  legacy/api:
    enable: [HOUSE_ONE]
    severity:
      HOUSE_ONE: warning
    ignore:
      - paths: ["old/**"]
        rules: [HOUSE_ONE]
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
	if !reflect.DeepEqual(f.Rulesets, []rules.Import{{Path: "example.com/std", Version: "v1.0.0", Alias: "std"}, {Path: "example.com/house", Alias: "house"}}) || strings.Join(f.Enable, ",") != "STANDARD,HOUSE_ONE" || strings.Join(f.Exclude, ",") != "FIELD_NAMES" {
		t.Fatalf("file = %+v", f)
	}
	if legacy, fresh := f.Modules["legacy/api"], f.Modules["fresh"]; len(f.Modules) != 3 || f.Modules["."].Enable != nil || strings.Join(legacy.Enable, ",") != "HOUSE_ONE" || legacy.Severity["HOUSE_ONE"] != check.SeverityWarning || fresh.Enable != nil || fresh.Exclude != nil || fresh.Severity != nil {
		t.Fatalf("modules = %+v", f.Modules)
	}
	if f.Severity["ENUM_NAMES"] != check.SeverityWarning || len(f.Ignore) != 3 || len(f.Ignore[0].Paths) != 2 || f.Ignore[1].Rules != nil || f.Ignore[1].Kind != "" || f.Ignore[2].Kind != check.KindBreaking || f.Breaking == nil || f.Breaking.Base != (Base{Form: BaseRef, Value: "main"}) || f.Breaking.Base.String() != "ref main" {
		t.Fatalf("file = %+v", f)
	}
	// One path at two versions under two aliases: two imports, exact
	// and isolated (the ruleset import term).
	two, err := Parse([]byte("rulesets:\n  - path: example.com/x\n    version: v1.0.0\n    alias: one\n  - path: example.com/x\n    version: v2.0.0\n    alias: two\n"))
	if err != nil || len(two.Rulesets) != 2 || two.Rulesets[0].Alias != "one" || two.Rulesets[1].Version != "v2.0.0" {
		t.Fatalf("one path at two versions: %+v %v", two, err)
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
		"ruleset not map":    {"rulesets: [example.com/x]\n", "rulesets[0] must be a mapping"},
		"ruleset empty":      {"rulesets:\n  - path: \"\"\n    alias: x\n", "rulesets[0].path must be a non-empty line of text"},
		"ruleset bad path":   {"rulesets:\n  - path: not a path\n    alias: x\n", "rulesets[0].path:"},
		"ruleset no alias":   {"rulesets:\n  - path: example.com/x\n", "rulesets[0]: missing alias"},
		"ruleset bad alias":  {"rulesets:\n  - path: example.com/x\n    alias: 1x\n", "rulesets[0].alias: alias \"1x\""},
		"ruleset bad ver":    {"rulesets:\n  - path: example.com/x\n    version: latest\n    alias: x\n", "rulesets[0].version:"},
		"ruleset extra key":  {"rulesets:\n  - path: example.com/x\n    alias: x\n    rules: []\n", "rulesets[0]: unknown key \"rules\""},
		"alias twice":        {"rulesets:\n  - path: example.com/x\n    alias: x\n  - path: example.com/y\n    alias: x\n", "rulesets[1]: alias x is another import's"},
		"ruleset twice":      {"rulesets:\n  - path: example.com/x\n    version: v1.0.0\n    alias: x\n  - path: example.com/x\n    version: v1.0.0\n    alias: y\n", "rulesets[1]: example.com/x at this version is imported twice"},
		"enable not list":    {"enable: X\n", "enable must be a list"},
		"severity not map":   {"severity: [X]\n", "severity must be a mapping"},
		"severity bad":       {"severity:\n  X: info\n", "severity.X must be error or warning"},
		"ignore not list":    {"ignore: {}\n", "ignore must be a list"},
		"ignore not mapping": {"ignore:\n  - x\n", "ignore[0] must be a mapping"},
		"ignore unknown key": {"ignore:\n  - paths: [a]\n    files: [b]\n", `ignore[0]: unknown key "files"`},
		"ignore no paths":    {"ignore:\n  - rules: [X]\n", "ignore[0] has no paths"},
		"ignore paths empty": {"ignore:\n  - paths: []\n", "ignore[0].paths must not be empty"},
		"ignore rules empty": {"ignore:\n  - paths: [a]\n    rules: []\n", "ignore[0].rules must not be empty"},
		"ignore kind":        {"ignore:\n  - paths: [a]\n    kind: both\n", "ignore[0].kind must be lint or breaking"},
		"base bad version":   {"breaking:\n  base:\n    version: nonsense\n", "breaking.base.version:"},
		"ignore bad glob":    {"ignore:\n  - paths: [\"[\"]\n", "ignore[0].paths:"},
		"modules not a map":  {"modules: [a]\n", "modules must be a mapping"},
		"modules bad dir":    {"modules:\n  ../x: {}\n", "modules:"},
		"modules abs dir":    {"modules:\n  /a: {}\n", "modules:"},
		"modules unclean":    {"modules:\n  ./a: {}\n", "modules:"},
		"modules trailing":   {"modules:\n  a/: {}\n", "modules:"},
		"modules unknown":    {"modules:\n  a:\n    rules: []\n", `modules.a: unknown key "rules"`},
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
		dir, path, rule string
		want            bool
	}{
		{"", "vendor/a/b.proto", "ENUM_NAMES", true},
		{"", "vendor/a/b.proto", "FIELD_NAMES", false},
		{"", "legacy/x.proto", "ENUM_NAMES", true},
		{"", "legacy/sub/x.proto", "ENUM_NAMES", false},
		{"", "gen/x.proto", "ANY", true},
		{"", "gen/deep/x.proto", "ANY", true},
		{"", "src/x.proto", "ENUM_NAMES", false},
		{"", "", "ANY", false},
		// The root's ignores reach every module; a module's own reach
		// its files alone.
		{"legacy/api", "gen/x.proto", "ANY", true},
		{"legacy/api", "old/x.proto", "HOUSE", true},
		{"legacy/api", "old/x.proto", "ENUM_NAMES", false},
		{"other", "old/x.proto", "HOUSE", false},
	}
	std := Ruleset{Path: "example.com/std", Alias: "std", Files: []rules.Located{located("a.rules.yaml", rule("FIELD_NAMES", "lint", "STANDARD"), rule("ENUM_NAMES", "lint", "STANDARD"), rule("NO_DELETE", "breaking", "STANDARD"))}}
	house := Ruleset{Path: "example.com/house", Alias: "house", Files: []rules.Located{located("h.rules.yaml", rule("HOUSE_ONE", "lint"), rule("HOUSE_TWO", "lint", "extra"))}}
	sel, err := Select(f, []Ruleset{std, house})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		name := c.rule
		switch name {
		case "ANY":
		case "HOUSE":
			name = "house:HOUSE_ONE"
		default:
			name = "std:" + name
		}
		if got := sel.Ignored(c.dir, c.path, name, check.KindLint); got != c.want {
			t.Errorf("Ignored(%q, %q, %s) = %v", c.dir, c.path, name, got)
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
	if all.Ignored("", "", "ANY", check.KindLint) || !all.Ignored("", "x.proto", "ANY", check.KindLint) {
		t.Error("the path-less finding under **")
	}
	// An entry naming a kind excludes that kind's findings alone; one
	// naming none either kind's.
	if !sel.Ignored("", "wire/x.proto", "std:ENUM_NAMES", check.KindBreaking) || sel.Ignored("", "wire/x.proto", "std:ENUM_NAMES", check.KindLint) || !sel.Ignored("", "gen/x.proto", "std:NO_DELETE", check.KindBreaking) {
		t.Fatal("the kind of an ignore entry")
	}
}

// Encode renders the canonical file (REQ-lint-emission): the keys in
// order, lists sorted where the order means nothing, entries as given
// where it does, a scalar quoted only where a plain one would read
// otherwise; a rendering parses back to the file it came from.
func TestEncode(t *testing.T) {
	f, err := Parse([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	want := `rulesets:
  - path: example.com/std
    version: v1.0.0
    alias: std
  - path: example.com/house
    alias: house
enable:
  - HOUSE_ONE
  - STANDARD
exclude:
  - FIELD_NAMES
severity:
  ENUM_NAMES: warning
ignore:
  - paths:
      - gen/**
  - paths:
      - legacy/*.proto
      - vendor/**
    rules:
      - ENUM_NAMES
  - paths:
      - wire/**
    kind: breaking
breaking:
  base:
    ref: main
modules:
  .: {}
  fresh: {}
  legacy/api:
    enable:
      - HOUSE_ONE
    severity:
      HOUSE_ONE: warning
    ignore:
      - paths:
          - old/**
        rules:
          - HOUSE_ONE
`
	if string(out) != want {
		t.Fatalf("Encode:\n%s", out)
	}
	// The rendering is a fixed point: parsed and encoded again, the
	// same bytes.
	again, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if twice, err := Encode(again); err != nil || string(twice) != want {
		t.Fatalf("round trip: %v\n%s", err, twice)
	}
	// An empty file; an empty enable, which means what an absent one
	// does not, at the root and in a module's entry; a pinned base.
	if out, err := Encode(&File{}); err != nil || string(out) != "{}\n" {
		t.Fatalf("empty: %q %v", out, err)
	}
	f = &File{Enable: []string{}, Exclude: []string{}, Breaking: &Breaking{Base: Base{Form: BasePinned}}, Modules: map[string]ModuleSelection{"a": {Enable: []string{}}}}
	if out, err := Encode(f); err != nil || string(out) != "enable: []\nbreaking:\n  base:\n    pinned: true\nmodules:\n  a:\n    enable: []\n" {
		t.Fatalf("empty enable, pinned: %q %v", out, err)
	}
	// A breaking base, a module with nothing, and the scalars quoted:
	// a spelling contractfile.Spell quotes, a glob opening with an
	// asterisk among them.
	g, _ := glob.Compile("*.proto")
	f = &File{
		Rulesets: []rules.Import{{Path: "example.com/x", Alias: "x"}},
		Enable:   []string{"x:B", "x:A", "true"},
		Ignore:   []Ignore{{Paths: []*glob.Pattern{g}, Rules: []string{"x:B", "x:A"}}},
		Breaking: &Breaking{Base: Base{Form: BaseRef, Value: "origin/main"}},
		Modules:  map[string]ModuleSelection{"b": {}, "a": {Exclude: []string{"x:A"}}},
	}
	out, err = Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	want = `rulesets:
  - path: example.com/x
    alias: x
enable:
  - "true"
  - x:A
  - x:B
ignore:
  - paths:
      - "*.proto"
    rules:
      - x:A
      - x:B
breaking:
  base:
    ref: origin/main
modules:
  a:
    exclude:
      - x:A
  b: {}
`
	if string(out) != want {
		t.Fatalf("Encode:\n%s", out)
	}
	// What Parse rejects, Encode refuses: a ruleset that is no path,
	// an ignore with no paths, one whose rules list is empty.
	for name, f := range map[string]*File{
		"ruleset":  {Rulesets: []rules.Import{{Path: "not a path", Alias: "x"}}},
		"alias":    {Rulesets: []rules.Import{{Path: "example.com/x", Alias: "not an alias"}}},
		"no paths": {Ignore: []Ignore{{}}},
		"no rules": {Ignore: []Ignore{{Paths: []*glob.Pattern{g}, Rules: []string{}}}},
	} {
		if _, err := Encode(f); err == nil {
			t.Fatalf("%s: an invalid file encoded", name)
		}
	}
	if _, err := Encode(nil); err == nil {
		t.Fatal("a nil file encoded")
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
	std := Ruleset{Path: "example.com/std", Alias: "std", Files: []rules.Located{
		located("a.rules.yaml", rule("FIELD_NAMES", "lint", "STANDARD"), rule("ENUM_NAMES", "lint", "STANDARD")),
		located("b.rules.yaml", rule("NO_DELETE", "breaking", "STANDARD")),
	}}
	house := Ruleset{Path: "example.com/house", Alias: "house", Files: []rules.Located{located("h.rules.yaml", rule("HOUSE_ONE", "lint"), rule("HOUSE_TWO", "lint", "extra"))}}
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
	dup := Ruleset{Path: "example.com/dup", Alias: "dup", Files: []rules.Located{located("d.rules.yaml", rule("FIELD_NAMES", "lint", "STANDARD"))}}
	names := func(rs []rules.Rule) string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Name()+":"+string(r.Severity))
		}
		return strings.Join(out, " ")
	}
	both, err := Select(&File{}, []Ruleset{std, dup})
	if err != nil || names(both.Rules) != "std:FIELD_NAMES:error std:ENUM_NAMES:error std:NO_DELETE:error dup:FIELD_NAMES:error" {
		t.Fatalf("two rulesets: %s %v", names(both.Rules), err)
	}
	for name, f := range map[string]*File{
		"enable bare":   {Enable: []string{"FIELD_NAMES"}},
		"enable tag":    {Enable: []string{"STANDARD"}},
		"exclude bare":  {Exclude: []string{"FIELD_NAMES"}},
		"severity bare": {Severity: map[string]check.Severity{"FIELD_NAMES": check.SeverityWarning}},
		"ignore bare":   {Ignore: []Ignore{{Paths: []*glob.Pattern{glob.MustCompile("x/**")}, Rules: []string{"FIELD_NAMES"}}}},
		"severity tag":  {Severity: map[string]check.Severity{"std:STANDARD": check.SeverityWarning}},
		"ignore tag":    {Ignore: []Ignore{{Paths: []*glob.Pattern{glob.MustCompile("x/**")}, Rules: []string{"std:STANDARD"}}}},
		"unknown name":  {Enable: []string{"std:NOPE"}},
	} {
		_, err := Select(f, []Ruleset{std, dup})
		switch {
		case err == nil || !errors.Is(err, ErrSelection):
			t.Errorf("%s: %v", name, err)
		case strings.HasSuffix(name, "bare") && !strings.Contains(err.Error(), "std:FIELD_NAMES, dup:FIELD_NAMES"):
			t.Errorf("%s names no candidates: %v", name, err)
		case name == "enable tag" && !strings.Contains(err.Error(), "std:STANDARD, dup:STANDARD"):
			t.Errorf("%s names no candidates: %v", name, err)
		case strings.HasSuffix(name, "tag") && name != "enable tag" && !strings.Contains(err.Error(), "a tag where a rule is named"):
			t.Errorf("%s: %v", name, err)
		}
	}
	qualified := &File{Enable: []string{"dup:STANDARD", "ENUM_NAMES"}, Exclude: []string{"std:NO_DELETE"}, Severity: map[string]check.Severity{"dup:FIELD_NAMES": check.SeverityWarning}, Ignore: []Ignore{{Paths: []*glob.Pattern{glob.MustCompile("x/**")}, Rules: []string{"std:FIELD_NAMES", "ENUM_NAMES"}}}}
	sel, err = Select(qualified, []Ruleset{std, dup})
	if err != nil || names(sel.Rules) != "std:ENUM_NAMES:error dup:FIELD_NAMES:warning" {
		t.Fatalf("qualified: %s %v", names(sel.Rules), err)
	}
	if !sel.Ignored("", "x/a.proto", "std:ENUM_NAMES", check.KindLint) || !sel.Ignored("", "x/a.proto", "std:FIELD_NAMES", check.KindLint) || sel.Ignored("", "x/a.proto", "dup:FIELD_NAMES", check.KindLint) {
		t.Fatal("ignore matches by canonical name")
	}
	if got := qualified.Ignore[0].Rules; strings.Join(got, " ") != "std:FIELD_NAMES ENUM_NAMES" {
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
	twice := Ruleset{Path: "example.com/twice", Alias: "twice", Files: []rules.Located{located("a.rules.yaml", rule("X", "lint")), located("b.rules.yaml", rule("X", "lint"))}}
	if _, err := Select(&File{}, []Ruleset{twice}); err == nil || !errors.Is(err, ErrSelection) || !strings.Contains(err.Error(), "twice:X declared by twice's a.rules.yaml and twice's b.rules.yaml") {
		t.Fatalf("twice: %v", err)
	}
	clash := Ruleset{Path: "example.com/clash", Alias: "clash", Files: []rules.Located{located("a.rules.yaml", rule("STANDARD", "lint"), rule("OTHER", "lint", "STANDARD"))}}
	if _, err := Select(&File{}, []Ruleset{clash}); err == nil || !errors.Is(err, ErrSelection) || !strings.Contains(err.Error(), "clash:STANDARD is both a rule, declared by clash's a.rules.yaml, and a tag, first carried by a rule of clash's a.rules.yaml") {
		t.Fatalf("rule and tag: %v", err)
	}
	if _, err := Select(&File{Severity: map[string]check.Severity{"ENUM_NAMES": check.SeverityError, "std:ENUM_NAMES": check.SeverityWarning}}, []Ruleset{std}); err == nil || !errors.Is(err, ErrSelection) || !strings.Contains(err.Error(), `severity names std:ENUM_NAMES twice, as "ENUM_NAMES" and "std:ENUM_NAMES"`) {
		t.Fatalf("severity twice: %v", err)
	}
}

// An import is read exactly as written (REQ-lint-rulesets-imported,
// REQ-rules-file-discovery): a workspace module from the working
// tree, a version written for it refused; any other at its version
// through the verified archive, no version refused; the rule files
// in path order; a bad rule file, and an archive that does not
// resolve, fail naming the ruleset.
func TestRulesets(t *testing.T) {
	root := &workspace.Root{Dir: "ws", Modules: []workspace.Module{
		{Dir: "a", File: &modfile.File{Module: "example.com/a", Deps: map[string]string{"example.com/std": "v1.0.0"}}},
		{Dir: "lib", File: &modfile.File{Module: "example.com/lib"}},
	}}
	good := "celEnv: 1\nrules:\n  - id: X\n    kind: lint\n    target: field\n    severity: error\n    cel: \"true\"\n    message: m\n"
	fsys := fstest.MapFS{
		"ws/a/pb.yaml":               {Data: []byte("module: example.com/a\n")},
		"ws/lib/pb.yaml":             {Data: []byte("module: example.com/lib\n")},
		"ws/lib/lib.rules.yaml":      {Data: []byte(good)},
		"ws/lib/nested/pb.yaml":      {Data: []byte("module: example.com/nested\n")},
		"ws/lib/nested/n.rules.yaml": {Data: []byte(good)},
	}
	served := map[string]map[string]string{
		"example.com/std@v1.0.0": {"pb.yaml": "module: example.com/std\n", "z.rules.yaml": good, "a/b.rules.yaml": "celEnv: 1\nrules: []\n"},
		"example.com/bad@v1.0.0": {"pb.yaml": "module: example.com/bad\n", "bad.rules.yaml": "celEnv: 9\nrules: []\n"},
	}
	var asked []string
	zip := func(_ context.Context, modPath string, v version.Version) ([]byte, error) {
		asked = append(asked, modPath+"@"+v.String())
		files, ok := served[modPath+"@"+v.String()]
		if !ok {
			return nil, fmt.Errorf("no origin serves %s@%s", modPath, v)
		}
		var entries []archive.File
		for _, p := range slices.Sorted(maps.Keys(files)) {
			entries = append(entries, archive.File{Path: p, Body: strings.NewReader(files[p])})
		}
		var buf bytes.Buffer
		if _, err := archive.WriteZip(&buf, entries); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes(), nil
	}
	imports := func(imps ...rules.Import) *File { return &File{Rulesets: imps} }
	sets, err := Rulesets(context.Background(), imports(rules.Import{Path: "example.com/std", Version: "v1.0.0", Alias: "std"}, rules.Import{Path: "example.com/lib", Alias: "lib"}), root, fsys, zip)
	if err != nil || len(sets) != 2 || sets[0].Alias != "std" || sets[0].Version != "v1.0.0" || len(sets[0].Files) != 2 || sets[0].Files[0].Path != "a/b.rules.yaml" ||
		sets[1].Alias != "lib" || len(sets[1].Files) != 1 || sets[1].Files[0].Path != "lib.rules.yaml" || strings.Join(asked, ",") != "example.com/std@v1.0.0" {
		t.Fatalf("rulesets: %+v %v asked %v", sets, err, asked)
	}
	for name, c := range map[string]struct {
		imp  rules.Import
		want string
	}{
		"workspace with a version": {rules.Import{Path: "example.com/lib", Version: "v1.0.0", Alias: "lib"}, "example.com/lib: a workspace module, read from the working tree: write no version (v1.0.0 written)"},
		"external without":         {rules.Import{Path: "example.com/std", Alias: "std"}, "example.com/std: no workspace module: write the version to read"},
		"unresolved":               {rules.Import{Path: "example.com/nowhere", Version: "v1.0.0", Alias: "no"}, "example.com/nowhere@v1.0.0: no origin serves example.com/nowhere@v1.0.0"},
		"bad rule file":            {rules.Import{Path: "example.com/bad", Version: "v1.0.0", Alias: "bad"}, "example.com/bad: bad.rules.yaml"},
	} {
		_, err := Rulesets(context.Background(), imports(c.imp), root, fsys, zip)
		if err == nil || !errors.Is(err, ErrRuleset) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Rulesets(context.Background(), imports(rules.Import{Path: "example.com/bad", Version: "v1.0.0", Alias: "bad"}), root, fsys, zip); !errors.Is(err, rules.ErrEnvironment) {
		t.Fatalf("bad rule file's cause: %v", err)
	}

	// A rule file's imports lend their rulesets' functions to that
	// file alone, each with its own file's scope; every import is an
	// edge and every fetched pair is read once; a chain of imports
	// returning to a path, and an import declaring no rule file, are
	// refused naming them (REQ-rules-imports).
	fnFile := func(imports, fns string) string {
		if fns != "" {
			fns = "functions:\n" + fns
		}
		return "celEnv: 1\n" + imports + fns + "rules: []\n"
	}
	served["example.com/util@v1.0.0"] = map[string]string{"pb.yaml": "module: example.com/util\n", "util.rules.yaml": fnFile("", "  - name: isSnake\n    returns: bool\n    params:\n      - name: s\n        type: string\n    cel: case(s, 'snake') == s\n")}
	served["example.com/std@v2.0.0"] = map[string]string{"pb.yaml": "module: example.com/std\n", "s.rules.yaml": fnFile("imports:\n  - path: example.com/util\n    version: v1.0.0\n    alias: util\n", "  - name: fieldOk\n    returns: bool\n    params:\n      - name: n\n        type: string\n    cel: util.isSnake(n)\n") + "", "t.rules.yaml": "celEnv: 1\nrules:\n  - id: T\n    kind: lint\n    target: field\n    severity: error\n    cel: \"true\"\n    message: m\n"}
	fsys["ws/lib/lib.rules.yaml"] = &fstest.MapFile{Data: []byte(fnFile("imports:\n  - path: example.com/std\n    version: v2.0.0\n    alias: std\n  - path: example.com/util\n    version: v1.0.0\n    alias: older\n", "  - name: ok\n    returns: bool\n    params:\n      - name: n\n        type: string\n    cel: std.fieldOk(n) && older.isSnake(n)\n"))}
	asked = nil
	loaded, err := Load(context.Background(), imports(rules.Import{Path: "example.com/lib", Alias: "house"}, rules.Import{Path: "example.com/std", Version: "v2.0.0", Alias: "std"}), root, fsys, zip)
	if err != nil {
		t.Fatal(err)
	}
	house := loaded.Rulesets[0].Files[0].File.Scope
	if len(loaded.Rulesets) != 2 || house.Where != "example.com/lib's lib.rules.yaml" || len(house.Lent["std"]) != 1 || house.Lent["std"][0].Function.Name != "fieldOk" || len(house.Lent["older"]) != 1 || house.Lent["older"][0].Function.Name != "isSnake" {
		t.Fatalf("lent scopes: %+v", house)
	}
	if stdScope := house.Lent["std"][0].Scope; stdScope.Where != "example.com/std@v2.0.0's s.rules.yaml" || len(stdScope.Lent["util"]) != 1 || stdScope.Lent["util"][0].Scope != house.Lent["older"][0].Scope {
		t.Fatalf("the lent function's own scope: %+v", stdScope)
	}
	if len(loaded.Rulesets[1].Files) != 2 || loaded.Rulesets[1].Files[1].File.Scope.Lent != nil {
		t.Fatalf("a file importing nothing is lent nothing: %+v", loaded.Rulesets[1].Files)
	}
	wantEdges := []Edge{{"pb.lint.yaml", "example.com/lib", ""}, {"example.com/lib", "example.com/std", "v2.0.0"}, {"example.com/std@v2.0.0", "example.com/util", "v1.0.0"}, {"example.com/lib", "example.com/util", "v1.0.0"}, {"pb.lint.yaml", "example.com/std", "v2.0.0"}}
	if !reflect.DeepEqual(loaded.Edges, wantEdges) {
		t.Fatalf("edges = %v", loaded.Edges)
	}
	if strings.Join(asked, ",") != "example.com/std@v2.0.0,example.com/util@v1.0.0" || len(loaded.Fetched) != 2 {
		t.Fatalf("each pair read once: asked %v, fetched %v", asked, loaded.Fetched)
	}
	// A chain returning to a path at any version is a cycle; an
	// import of a ruleset without rule files lends nothing and is
	// refused.
	served["example.com/std@v2.0.0"]["s.rules.yaml"] = fnFile("imports:\n  - path: example.com/util\n    version: v1.0.0\n    alias: util\n", "")
	served["example.com/util@v1.0.0"]["util.rules.yaml"] = fnFile("imports:\n  - path: example.com/std\n    version: v2.0.0\n    alias: std\n", "")
	if _, err := Load(context.Background(), imports(rules.Import{Path: "example.com/std", Version: "v2.0.0", Alias: "std"}), root, fsys, zip); err == nil || !errors.Is(err, ErrRuleset) || !strings.Contains(err.Error(), "imports cycle: example.com/std -> example.com/util -> example.com/std") {
		t.Fatalf("a cycle: %v", err)
	}
	// A ruleset's function names are one namespace across its files.
	served["example.com/util@v1.0.0"]["util.rules.yaml"] = fnFile("", "  - name: dup\n    returns: bool\n    cel: \"true\"\n")
	served["example.com/util@v1.0.0"]["z.rules.yaml"] = fnFile("", "  - name: dup\n    returns: bool\n    cel: \"true\"\n")
	if _, err := Load(context.Background(), imports(rules.Import{Path: "example.com/util", Version: "v1.0.0", Alias: "util"}), root, fsys, zip); err == nil || !errors.Is(err, ErrRuleset) || !strings.Contains(err.Error(), "example.com/util: function dup declared by util.rules.yaml and z.rules.yaml") {
		t.Fatalf("a function declared by two files: %v", err)
	}
	delete(served["example.com/util@v1.0.0"], "z.rules.yaml")
	served["example.com/empty@v1.0.0"] = map[string]string{"pb.yaml": "module: example.com/empty\n"}
	served["example.com/util@v1.0.0"]["util.rules.yaml"] = fnFile("imports:\n  - path: example.com/empty\n    version: v1.0.0\n    alias: e\n", "")
	if _, err := Load(context.Background(), imports(rules.Import{Path: "example.com/util", Version: "v1.0.0", Alias: "util"}), root, fsys, zip); err == nil || !errors.Is(err, ErrRuleset) || !strings.Contains(err.Error(), "example.com/util: util.rules.yaml imports example.com/empty, which declares no rule file") {
		t.Fatalf("an import without rule files: %v", err)
	}
}
