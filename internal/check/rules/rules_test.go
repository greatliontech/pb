package rules

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/check"
	"pgregory.net/rapid"
)

const good = `celEnv: 1
rules:
  - id: FIELD_LOWER_SNAKE_CASE
    kind: lint
    target: field
    severity: error
    tags: [STANDARD, naming]
    cel: field.name.matches('^[a-z][a-z0-9_]*$')
    message: field names are lower_snake_case
  - id: FIELD_NO_DELETE
    kind: breaking
    target: field
    severity: warning
    cel: new != null
    message: a field was deleted
`

// A rule file parses to its environment version and its rules in
// declaration order, each field the written spelling, tags optional
// (REQ-rules-file-schema).
func TestParseRuleFile(t *testing.T) {
	f, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if f.CELEnv != 1 || len(f.Rules) != 2 {
		t.Fatalf("file = %+v", f)
	}
	r := f.Rules[0]
	if r.ID != "FIELD_LOWER_SNAKE_CASE" || r.Kind != check.KindLint || r.Target != check.TargetField || r.Severity != check.SeverityError ||
		len(r.Tags) != 2 || r.Tags[0] != "STANDARD" || r.CEL != "field.name.matches('^[a-z][a-z0-9_]*$')" || r.Message != "field names are lower_snake_case" {
		t.Fatalf("rule 0 = %+v", r)
	}
	if b := f.Rules[1]; b.Kind != check.KindBreaking || b.Severity != check.SeverityWarning || b.Tags != nil || b.CEL != "new != null" {
		t.Fatalf("rule 1 = %+v", b)
	}
}

// Every departure from the schema is refused naming what departed:
// no other keys exist, every field but tags is required, the
// enumerated fields take their spellings alone, ids are unique
// (REQ-rules-file-schema).
func TestParseRefusesSchemaDepartures(t *testing.T) {
	rule := func(fields string) string {
		return "celEnv: 1\nrules:\n  - " + strings.ReplaceAll(strings.TrimSpace(fields), "\n", "\n    ") + "\n"
	}
	full := "id: A\nkind: lint\ntarget: file\nseverity: error\ncel: \"true\"\nmessage: m"
	cases := map[string]struct{ in, want string }{
		"empty document":     {"", "missing celEnv and rules"},
		"unknown top key":    {good + "extra: 1\n", `unknown key "extra"`},
		"missing celEnv":     {"rules: []\n", "missing celEnv"},
		"missing rules":      {"celEnv: 1\n", "missing rules"},
		"celEnv not integer": {"celEnv: one\nrules: []\n", "celEnv must be an unquoted decimal integer"},
		"celEnv float":       {"celEnv: 1.0\nrules: []\n", "celEnv must be an unquoted decimal integer"},
		"celEnv quoted":      {"celEnv: \"1\"\nrules: []\n", "celEnv must be an unquoted decimal integer"},
		"celEnv single":      {"celEnv: '1'\nrules: []\n", "celEnv must be an unquoted decimal integer"},
		"celEnv hex":         {"celEnv: 0x1\nrules: []\n", "celEnv must be an unquoted decimal integer"},
		"celEnv octal":       {"celEnv: 0o1\nrules: []\n", "celEnv must be an unquoted decimal integer"},
		"celEnv signed":      {"celEnv: +1\nrules: []\n", "celEnv must be an unquoted decimal integer"},
		"celEnv zero-led":    {"celEnv: 01\nrules: []\n", "celEnv must be an unquoted decimal integer"},
		"celEnv negative":    {"celEnv: -1\nrules: []\n", "celEnv must be an unquoted decimal integer"},
		"celEnv underscored": {"celEnv: 1_000\nrules: []\n", "celEnv must be an unquoted decimal integer"},
		"celEnv null":        {"celEnv: ~\nrules: []\n", "celEnv must be an unquoted decimal integer"},
		"celEnv mapping":     {"celEnv: {}\nrules: []\n", "celEnv must be an unquoted decimal integer"},
		"rules not a list":   {"celEnv: 1\nrules: {}\n", "rules must be a list"},
		"rule not a mapping": {"celEnv: 1\nrules:\n  - x\n", "rules[0] must be a mapping"},
		"unknown rule key":   {rule(full + "\nextra: 1"), `unknown key "extra"`},
		"missing message":    {rule("id: A\nkind: lint\ntarget: file\nseverity: error\ncel: \"true\""), "rules[0]: missing message"},
		"missing id":         {rule("kind: lint\ntarget: file\nseverity: error\ncel: \"true\"\nmessage: m"), "rules[0]: missing id"},
		"missing kind":       {rule("id: A\ntarget: file\nseverity: error\ncel: \"true\"\nmessage: m"), "rules[0]: missing kind"},
		"missing target":     {rule("id: A\nkind: lint\nseverity: error\ncel: \"true\"\nmessage: m"), "rules[0]: missing target"},
		"missing severity":   {rule("id: A\nkind: lint\ntarget: file\ncel: \"true\"\nmessage: m"), "rules[0]: missing severity"},
		"missing cel":        {rule("id: A\nkind: lint\ntarget: file\nseverity: error\nmessage: m"), "rules[0]: missing cel"},
		"empty cel":          {rule("id: A\nkind: lint\ntarget: file\nseverity: error\ncel: \"\"\nmessage: m"), "cel must be a non-empty scalar"},
		"empty id":           {rule(strings.Replace(full, "id: A", "id: \"\"", 1)), "id must be one non-empty line of text"},
		"null id":            {rule(strings.Replace(full, "id: A", "id: ~", 1)), "id must be one non-empty line of text"},
		"literal-block id":   {rule(strings.Replace(full, "id: A", "id: |\n  A", 1)), "id must be one non-empty line of text"},
		"broken message":     {rule(strings.Replace(full, "message: m", "message: \"a\\nb\"", 1)), "message must be one non-empty line of text"},
		"returned message":   {rule(strings.Replace(full, "message: m", "message: \"a\\rb\"", 1)), "message must be one non-empty line of text"},
		"folded message":     {rule(strings.Replace(full, "message: m", "message: >\n  m", 1)), "message must be one non-empty line of text"},
		"mapping cel":        {rule(strings.Replace(full, "cel: \"true\"", "cel: {a: b}", 1)), "cel must be a non-empty scalar"},
		"null cel":           {rule(strings.Replace(full, "cel: \"true\"", "cel: ~", 1)), "cel must be a non-empty scalar"},
		"null tag":           {rule(full + "\ntags: [~]"), "rules[0].tags must hold non-empty lines of text"},
		"broken tag":         {rule(full + "\ntags: [\"a\\nb\"]"), "rules[0].tags must hold non-empty lines of text"},
		"literal-block tag":  {rule(full + "\ntags:\n  - |\n    a"), "rules[0].tags must hold non-empty lines of text"},
		"bad kind":           {rule(strings.Replace(full, "kind: lint", "kind: style", 1)), `kind "style"`},
		"bad target":         {rule(strings.Replace(full, "target: file", "target: import", 1)), `target "import"`},
		"bad severity":       {rule(strings.Replace(full, "severity: error", "severity: fatal", 1)), `severity "fatal"`},
		"tags not a list":    {rule(full + "\ntags: STANDARD"), "rules[0].tags must be a list"},
		"empty tag":          {rule(full + "\ntags: [\"\"]"), "rules[0].tags must hold non-empty lines of text"},
		"duplicate rule key": {rule(full + "\nid: B"), `mapping key "id" already defined`},
		"duplicate id":       {"celEnv: 1\nrules:\n  - " + strings.ReplaceAll(full, "\n", "\n    ") + "\n  - " + strings.ReplaceAll(full, "\n", "\n    ") + "\n", `id "A" declared twice`},
		"anchor":             {"celEnv: &e 1\nrules: []\n", "forbidden YAML construct"},
		"two documents":      {good + "---\ncelEnv: 1\nrules: []\n", "exactly one YAML document"},
	}
	for name, c := range cases {
		_, err := Parse([]byte(c.in))
		if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want ErrInvalid naming %q", name, err, c.want)
		}
	}
	// Every target the vocabulary names is accepted.
	for _, target := range check.Targets() {
		if _, err := Parse([]byte(rule(strings.Replace(full, "target: file", "target: "+string(target), 1)))); err != nil {
			t.Errorf("target %s: %v", target, err)
		}
	}
	// A file with no rules is a file with no rules.
	if f, err := Parse([]byte("celEnv: 1\nrules: []\n")); err != nil || len(f.Rules) != 0 {
		t.Fatalf("no rules: %+v %v", f, err)
	}
}

// Every field takes every YAML scalar spelling — quoted, plain, a
// typed spelling read as the text written — and the expression a
// block scalar too, the readable spelling of a multi-line expression,
// its newlines kept; a folded block that folds to one line is a line
// (REQ-rules-file-schema).
func TestParseTakesEveryScalarSpelling(t *testing.T) {
	in := `celEnv: 1
rules:
  - id: "A"
    kind: 'lint'
    target: message
    severity: error
    tags:
      - "q"
      - >-
        folded
        tag
      - 007
    cel: |
      message.fields.all(f,
        f.name.matches('^[a-z]'))
    message: >-
      a folded
      message
  - id: 7
    kind: breaking
    target: field
    severity: warning
    cel: true
    message: 1.50
`
	f, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	r := f.Rules[0]
	if r.ID != "A" || r.Kind != check.KindLint || len(r.Tags) != 3 || r.Tags[1] != "folded tag" || r.Tags[2] != "007" {
		t.Fatalf("rule = %+v", r)
	}
	if r.CEL != "message.fields.all(f,\n  f.name.matches('^[a-z]'))\n" {
		t.Fatalf("cel = %q", r.CEL)
	}
	if r.Message != "a folded message" {
		t.Fatalf("message = %q", r.Message)
	}
	if b := f.Rules[1]; b.ID != "7" || b.CEL != "true" || b.Message != "1.50" {
		t.Fatalf("typed spellings = %+v", b)
	}
}

// A well-spelled rule file is refused exactly when its environment
// version is one the engine does not provide, whatever the version's
// value — one no int holds included — and the refusal names the
// version as written and what is provided (REQ-rules-env-versioned).
func TestParseRefusesUnprovidedEnvironment(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		v := rapid.IntRange(0, 1000).Draw(t, "celEnv")
		_, err := Parse([]byte("celEnv: " + strconv.Itoa(v) + "\nrules: []\n"))
		if check.ProvidesEnvironment(v) {
			if err != nil {
				t.Fatalf("provided %d refused: %v", v, err)
			}
			return
		}
		if err == nil || !errors.Is(err, ErrEnvironment) || !strings.Contains(err.Error(), strconv.Itoa(v)) || !strings.Contains(err.Error(), "provided: [1]") {
			t.Fatalf("unprovided %d: %v", v, err)
		}
	})
	for _, over := range []string{"18446744073709551615", "99999999999999999999999"} {
		_, err := Parse([]byte("celEnv: " + over + "\nrules: []\n"))
		if err == nil || !errors.Is(err, ErrEnvironment) || !strings.Contains(err.Error(), "celEnv "+over+" (provided: [1])") {
			t.Fatalf("over-range %s: %v", over, err)
		}
	}
}

// Discovery parses the module's rule files in the byte order of the
// paths, a ruleset without one yielding none, a file that fails the
// schema failing the discovery naming it (REQ-rules-file-discovery);
// the module loader names the files.
func TestDiscover(t *testing.T) {
	files := map[string][]byte{
		"z.rules.yaml":     []byte("celEnv: 1\nrules: []\n"),
		"a/b/c.rules.yaml": []byte(good),
		"a-b.rules.yaml":   []byte("celEnv: 1\nrules: []\n"),
		"a.rules.yaml":     []byte("celEnv: 1\nrules: []\n"),
	}
	got, err := Discover(files)
	if err != nil {
		t.Fatal(err)
	}
	// Byte order of the paths: '-' before '.' before '/', so the
	// files under a/ come after both a-b and a.
	want := []string{"a-b.rules.yaml", "a.rules.yaml", "a/b/c.rules.yaml", "z.rules.yaml"}
	if len(got) != len(want) {
		t.Fatalf("discovered %+v", got)
	}
	for i, p := range want {
		if got[i].Path != p {
			t.Fatalf("discovered[%d] = %s, want %s", i, got[i].Path, p)
		}
	}
	if len(got[2].File.Rules) != 2 {
		t.Fatalf("discovered %+v", got)
	}
	if _, err := Discover(map[string][]byte{"one.rules.yaml": []byte("celEnv: 1\n"), "two.rules.yaml": []byte(good)}); err == nil || !strings.HasPrefix(err.Error(), "one.rules.yaml: ") || !errors.Is(err, ErrInvalid) {
		t.Fatalf("a bad file: %v", err)
	}
	if _, err := Discover(map[string][]byte{"e.rules.yaml": []byte("celEnv: 7\nrules: []\n")}); err == nil || !errors.Is(err, ErrEnvironment) || !strings.HasPrefix(err.Error(), "e.rules.yaml: ") {
		t.Fatalf("an unprovided environment: %v", err)
	}
	if got, err := Discover(nil); err != nil || len(got) != 0 {
		t.Fatalf("no files: %+v %v", got, err)
	}
}
