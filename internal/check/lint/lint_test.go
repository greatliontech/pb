package lint

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/greatliontech/pb/internal/testing/prototest"
	"pgregory.net/rapid"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/check/env1"
	"github.com/greatliontech/pb/internal/check/rules"
)

// The fixture: tabs and multibyte text before declarations, every
// suppression spelling, block comments, two packages, a dependency,
// and a file of every kind under a license header.
var fixture = map[string]string{
	"p/one.proto": "syntax = \"proto3\";\npackage p;\n\nimport \"q/dep.proto\";\n\n" +
		"message Bad {\n" +
		"\tstring BadName = 1;\n" + // tab-indented: column 2 in code points
		"  /* é */ string Other_Name = 2; // pb:ignore FIELD_NAMES the legacy name\n" + // same-line suppression, multibyte before the declaration
		"  // pb:ignore FIELD_NAMES\n  string Third = 3;\n" + // previous-line suppression
		"  string Fourth = 4; // pb:ignore OTHER_RULE\n" + // the wrong rule
		"  string Fifth = 5; // pb:ignoreFIELD_NAMES\n" + // not the marker as a word
		"  string url = 6 [(q.doc) = \"http://x\"]; // pb:ignore FIELD_NAMES\n" + // a // inside a string before the comment
		"  string Sixth = 7 [(q.doc) = \"// pb:ignore FIELD_NAMES x\"];\n" + // the marker inside a string is no comment; the trailing comment above is url's, not Sixth's
		"  /* pb:ignore FIELD_NAMES */ string Seventh = 8;\n" + // a block comment is no suppression
		"  /* // pb:ignore FIELD_NAMES */ string Eighth = 9;\n" + // nor is a // inside one
		"  string Ninth = 10; /* the user's name */ // pb:ignore FIELD_NAMES\n" + // a quote inside a block comment opens no string
		"  /* a block\n     // pb:ignore FIELD_NAMES\n  */ string Tenth = 11;\n" + // a // inside a block comment spanning lines
		"  string Eleventh = 12; //pb:ignore FIELD_NAMES\n" + // no space after the slashes: the comment's first word is the marker
		"  string ok = 13;\n" +
		"}\n\n" +
		"// pb:ignore FIELD_NAMES\n\n" + // two lines above: not adjacent
		"message Fine {\n  string Twelfth = 1;\n}\n",
	"p/two.proto": "syntax = \"proto3\";\npackage p;\nmessage Two {}\n",
	"q/dep.proto": "syntax = \"proto3\";\npackage q;\nimport \"google/protobuf/descriptor.proto\";\nextend google.protobuf.FieldOptions {\n  string doc = 50000;\n}\nmessage DepBad {\n  string DepName = 1;\n}\n",
	"k/kinds.proto": "// Copyright: a license header\n// pushes the syntax off line one.\n\nsyntax = \"proto2\";\npackage k;\n" +
		"import \"google/protobuf/descriptor.proto\";\n" +
		"message M {\n" +
		"  oneof o {\n    int32 a = 1;\n  }\n" +
		"  /* ü */ optional string b = 2;\n" + // multibyte before, unsuppressed: column 12
		"  message Nested {\n    extensions 10 to 20;\n  }\n" +
		"  extend Nested {\n    optional int32 x = 10;\n  }\n" +
		"}\n" +
		"enum E {\n  E_ZERO = 0;\n}\n" +
		"service S {\n  rpc Do(M) returns (M);\n}\n" +
		"extend google.protobuf.FieldOptions {\n  optional string k = 50001;\n}\n",
}

func fixtureEnv(t testing.TB) (*env1.Env, *env1.Set) {
	t.Helper()
	set := env1.NewSet(prototest.Compile(t, fixture))
	env, err := env1.New(set, nil)
	if err != nil {
		t.Fatal(err)
	}
	return env, set
}

func fixtureRun(t testing.TB, rs []rules.Rule, checked ...string) (*Report, error) {
	t.Helper()
	env, set := fixtureEnv(t)
	return Run(env, set, checked, prototest.Source(fixture), rs)
}

var fieldNames = rules.Rule{ID: "FIELD_NAMES", Kind: check.KindLint, Target: check.TargetField, Severity: check.SeverityError, CEL: "case(field.name, 'snake') == field.name", Message: "field names are snake_case"}

func lines(r *Report) []string {
	var out []string
	for _, f := range r.Findings {
		out = append(out, fmt.Sprintf("%s:%d:%d %s %s: %s", f.Path, f.Line, f.Column, f.Severity, f.RuleID, f.Message))
	}
	return out
}

// A false verdict is one finding at the entity's declaration — line
// and column 1-based, the column in code points whatever tabs and
// multibyte text precede it — over the checked files alone; a
// suppression comment on the line, or alone on the line before,
// naming the rule drops it, and nothing else does — not a marker in
// a string, in a block comment, or two lines up (REQ-rules-verdict,
// REQ-rules-finding-location, REQ-lint-suppression).
func TestFindings(t *testing.T) {
	r, err := fixtureRun(t, []rules.Rule{fieldNames}, "p/one.proto", "p/two.proto")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"p/one.proto:7:2 error FIELD_NAMES: field names are snake_case",
		"p/one.proto:11:3 error FIELD_NAMES: field names are snake_case",
		"p/one.proto:12:3 error FIELD_NAMES: field names are snake_case",
		"p/one.proto:14:3 error FIELD_NAMES: field names are snake_case",
		"p/one.proto:15:31 error FIELD_NAMES: field names are snake_case",
		"p/one.proto:16:34 error FIELD_NAMES: field names are snake_case",
		"p/one.proto:20:6 error FIELD_NAMES: field names are snake_case",
		"p/one.proto:28:3 error FIELD_NAMES: field names are snake_case",
	}
	if got := lines(r); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if r.Rules != 1 {
		t.Fatalf("rules = %d", r.Rules)
	}
}

// Every kind's finding sits at its declaration's first token: the
// file's at its syntax statement past a header, the others at the
// keyword or name opening them, the column in code points past
// multibyte text; the population's order is the walk's — a kind's
// file-level declarations, then the messages' — the verb sorting
// what it prints (REQ-rules-finding-location).
func TestLocationsByKind(t *testing.T) {
	var rs []rules.Rule
	for _, target := range check.Targets() {
		rs = append(rs, rules.Rule{ID: strings.ToUpper(string(target)), Kind: check.KindLint, Target: target, Severity: check.SeverityError, CEL: "false", Message: "m"})
	}
	r, err := fixtureRun(t, rs, "k/kinds.proto")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"k/kinds.proto:4:1 error FILE: m",
		"k/kinds.proto:0:0 error PACKAGE: m",
		"k/kinds.proto:7:1 error MESSAGE: m",
		"k/kinds.proto:12:3 error MESSAGE: m",
		"k/kinds.proto:9:5 error FIELD: m",
		"k/kinds.proto:11:11 error FIELD: m",
		"k/kinds.proto:8:3 error ONEOF: m",
		"k/kinds.proto:19:1 error ENUM: m",
		"k/kinds.proto:20:3 error ENUM-VALUE: m",
		"k/kinds.proto:22:1 error SERVICE: m",
		"k/kinds.proto:23:3 error METHOD: m",
		"k/kinds.proto:26:3 error EXTENSION: m",
		"k/kinds.proto:16:5 error EXTENSION: m",
		":0:0 error SET: m",
	}
	if got := lines(r); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A package rule's finding is at the package's first checked file in
// path order without a position; a set rule's has no location; a
// comment cannot suppress either (REQ-rules-finding-location).
func TestPositionless(t *testing.T) {
	pkg := rules.Rule{ID: "PKG", Kind: check.KindLint, Target: check.TargetPackage, Severity: check.SeverityWarning, CEL: "pkg == 'zzz'", Message: "m"}
	set := rules.Rule{ID: "SET", Kind: check.KindLint, Target: check.TargetSet, Severity: check.SeverityError, CEL: "files.size() == 0", Message: "n"}
	r, err := fixtureRun(t, []rules.Rule{set, pkg}, "p/two.proto", "p/one.proto")
	if err != nil {
		t.Fatal(err)
	}
	// Given out of path order, the first file in path order names the
	// package's finding.
	want := []string{":0:0 error SET: n", "p/one.proto:0:0 warning PKG: m"}
	if got := lines(r); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findings: %q", got)
	}
}

// Zero rules evaluate to zero findings and a report saying so; a rule
// that does not compile, or fails to evaluate, or a source that
// cannot be read, fails the run naming it (REQ-rules-no-defaults).
func TestRunRefusals(t *testing.T) {
	r, err := fixtureRun(t, nil, "p/one.proto")
	if err != nil || r.Rules != 0 || r.Findings == nil || len(r.Findings) != 0 {
		t.Fatalf("no rules: %+v %v", r, err)
	}
	bad := fieldNames
	bad.CEL = "field.nonesuch == 1"
	if _, err := fixtureRun(t, []rules.Rule{bad}, "p/one.proto"); err == nil || !errors.Is(err, env1.ErrCompile) || !strings.Contains(err.Error(), "FIELD_NAMES") {
		t.Fatalf("compile: %v", err)
	}
	bad.CEL = "[1][field.number] == 1"
	if _, err := fixtureRun(t, []rules.Rule{bad}, "p/one.proto"); err == nil || !errors.Is(err, env1.ErrEval) {
		t.Fatalf("eval: %v", err)
	}
	if _, err := fixtureRun(t, []rules.Rule{fieldNames}, "nonesuch.proto"); err == nil {
		t.Fatal("a path outside the schema ran")
	}
	// The source is read only for a finding with a position.
	set := rules.Rule{ID: "S", Kind: check.KindLint, Target: check.TargetSet, Severity: check.SeverityError, CEL: "false", Message: "m"}
	fileRule := rules.Rule{ID: "F", Kind: check.KindLint, Target: check.TargetFile, Severity: check.SeverityError, CEL: "false", Message: "m"}
	noSource := func(string) ([]byte, error) { return nil, errors.New("gone") }
	env, setv := fixtureEnv(t)
	if r, err := Run(env, setv, []string{"p/two.proto"}, noSource, []rules.Rule{set}); err != nil || len(r.Findings) != 1 {
		t.Fatalf("positionless without source: %+v %v", r, err)
	}
	if _, err := Run(env, setv, []string{"p/two.proto"}, noSource, []rules.Rule{fileRule}); err == nil || !errors.Is(err, ErrSource) {
		t.Fatalf("positioned without source: %v", err)
	}
}

// The column recount inverts the compiler's count exactly: for any
// line of tabs, ASCII, multibyte runes and stray bytes, and any
// code-point index, the compiler's column of that code point recounts
// to the index plus one — a code point being each byte starting a
// UTF-8 sequence, as the compiler counts.
func TestColumnRecount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		pieces := rapid.SliceOfN(rapid.SampledFrom([]string{"\t", " ", "a", "é", "漢", "𝔘", "\x80", "\xc3"}), 0, 24).Draw(t, "line")
		line := strings.Join(pieces, "")
		// The code points as the compiler sees them: sequence starts.
		var starts []int
		for i := 0; i < len(line); i++ {
			if utf8.RuneStart(line[i]) {
				starts = append(starts, i)
			}
		}
		index := rapid.IntRange(0, len(starts)).Draw(t, "index")
		col := 0
		for _, i := range starts[:index] {
			if line[i] == '\t' {
				col += 8 - col%8
			} else {
				col++
			}
		}
		tx := newText([]byte(line + "\n"))
		if got := tx.column(0, col); got != index+1 {
			t.Fatalf("line %q col %d: %d, want %d", line, col, got, index+1)
		}
	})
}

// A line comment is the text after a // outside string literals and
// block comments, a block comment spanning lines included; a
// suppression is the marker and the id as the comment's first two
// words; a comment is alone when nothing but whitespace precedes it.
func TestLineComment(t *testing.T) {
	cases := []struct {
		src     string
		comment string
		ok      bool
		alone   bool
	}{
		{`string a = 1; // pb:ignore X`, " pb:ignore X", true, false},
		{`string a = 1;`, "", false, false},
		{`string a = 1 [(o) = "http://x"]; // c`, " c", true, false},
		{`string a = 1 [(o) = "// no"];`, "", false, false},
		{`string a = 1 [(o) = 'it\'s // no'];`, "", false, false},
		{`string a = 1 [(o) = "esc\"aped // no"]; //yes`, "yes", true, false},
		{`// pb:ignore X`, " pb:ignore X", true, true},
		{"  \t// pb:ignore X", " pb:ignore X", true, true},
		{`/* pb:ignore X */`, "", false, false},
		{`/* // pb:ignore X */ string a = 1;`, "", false, false},
		{`string a = 1; /* the user's name */ // yes`, " yes", true, false},
		{`/* a */ // yes`, " yes", true, false},
		{`a / b // c`, " c", true, false},
		{"/* opens\n // inside\n */ string a = 1; // after", " after", true, false},
		{`// pb:ignore X // note`, " pb:ignore X // note", true, true},
		{"// see /* below\n// pb:ignore X", " pb:ignore X", true, true},
	}
	for _, c := range cases {
		tx := newText([]byte(c.src))
		l := tx.lines[len(tx.lines)-1]
		if l.has != c.ok || l.comment != c.comment || l.alone != c.alone {
			t.Errorf("%q: %q has=%v alone=%v", c.src, l.comment, l.has, l.alone)
		}
	}
	// Inside a block comment spanning lines, a // is no comment.
	tx := newText([]byte("/* opens\n // inside\n */ string a = 1;\n"))
	if tx.lines[1].has || tx.lines[2].has {
		t.Errorf("a // inside a block comment read as a comment: %+v", tx.lines)
	}
	// A string resets at the line end, so an unbalanced quote never
	// swallows the next line; CRLF endings are stripped.
	tx = newText([]byte("string a = 1 [(o) = \"unterminated;\r\n// pb:ignore X\r\n"))
	if l := tx.lines[1]; !l.has || !l.alone || l.comment != " pb:ignore X" {
		t.Errorf("after an unbalanced quote: %+v", l)
	}
	for comment, want := range map[string]bool{" pb:ignore X": true, "pb:ignore X reason here": true, "  pb:ignore   X  ": true, " pb:ignore Y": false, " pb:ignore": false, " pb:ignoreX": false, " pb:ignore XY": false, " see pb:ignore X": false, " pb:ignore X, Y": false} {
		if ignores(comment, "X") != want {
			t.Errorf("%q: %v", comment, !want)
		}
	}
}
