package contractfile

import (
	"errors"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

func parseBody(t *testing.T, src string) ast.Node {
	t.Helper()
	f, err := parser.ParseBytes([]byte(src), 0)
	if err != nil || len(f.Docs) != 1 {
		t.Fatalf("fixture parse: %v", err)
	}
	return f.Docs[0].Body
}

func TestCheck(t *testing.T) {
	good := []string{
		"a: 1\nb:\n  c: [x, y]\n",
		"- 1\n- {k: v}\n",
		"plain: scalar\n",
	}
	for _, src := range good {
		if err := Check(parseBody(t, src)); err != nil {
			t.Errorf("%q rejected: %v", src, err)
		}
	}
	bad := []struct{ name, src, msg string }{
		{"merge key", "a:\n  <<: {x: y}\n", "merge keys"},
		{"anchor", "a: &x 1\n", "anchors"},
		{"anchored mapping", "a: &m\n  k: v\n", "anchors"},
		{"alias", "a: &x 1\nb: *x\n", "anchors"}, // anchor rejected first
		{"tag", "a: !!str x\n", "tags"},
		{"nested in sequence", "a:\n  - k: v\n  - <<: {x: y}\n", "merge keys"},
		{"anchored key", "&k a: 1\n", "anchors"},
		{"null key", "a: 1\nnull: x\n", "mapping keys are strings"},
		{"integer key", "7: x\n", "mapping keys are strings"},
		{"bool key", "true: x\n", "mapping keys are strings"},
		{"null key nested", "a:\n  - b:\n      null: x\n", "mapping keys are strings"},
		{"forbidden node in second pair", "ok: 1\nbad: &x 2\n", "anchors"},
		{"forbidden key in second pair", "ok: 1\n<<: {x: y}\n", "merge keys"},
	}
	for _, tc := range bad {
		err := Check(parseBody(t, tc.src))
		if !errors.Is(err, ErrForbiddenNode) || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: err = %v, want %q rejection", tc.name, err, tc.msg)
		}
	}
	if err := Check(nil); err != nil {
		t.Errorf("nil node: %v", err)
	}
}

// Programmatic nodes reach cases valid YAML source cannot: an alias is
// always preceded by its anchor (which rejects first), so the AliasNode
// case is exercised by direct construction.
func TestCheckConstructedNodes(t *testing.T) {
	if err := Check(&ast.AliasNode{}); !errors.Is(err, ErrForbiddenNode) || !strings.Contains(err.Error(), "aliases") {
		t.Fatalf("alias node: %v", err)
	}
	// Sequence error propagation from a non-first element.
	seq := parseBody(t, "- a\n- b\n")
	sq := seq.(*ast.SequenceNode)
	sq.Values[1] = &ast.AliasNode{}
	if err := Check(sq); !errors.Is(err, ErrForbiddenNode) {
		t.Fatalf("sequence alias propagation: %v", err)
	}
}

// TestDoc pins the shared prologue: exactly one document, admissibility
// before any interpretation, top-level mapping, and nil for a bodyless
// document.
func TestDoc(t *testing.T) {
	t.Run("happy: mapping returned", func(t *testing.T) {
		m, err := Doc([]byte("a: 1\nb: 2\n"))
		if err != nil || m == nil || len(m.Values) != 2 {
			t.Fatalf("Doc = (%v, %v)", m, err)
		}
	})
	t.Run("empty document is (nil, nil)", func(t *testing.T) {
		for _, src := range []string{"", "# only comments\n# here\n"} {
			m, err := Doc([]byte(src))
			if m != nil || err != nil {
				t.Fatalf("Doc(%q) = (%v, %v), want (nil, nil)", src, m, err)
			}
		}
	})
	t.Run("unparseable input surfaces the parser error", func(t *testing.T) {
		if _, err := Doc([]byte("{")); err == nil ||
			!strings.Contains(err.Error(), "could not find flow mapping end") {
			t.Fatalf("Doc = %v, want the parser's own error", err)
		}
	})
	t.Run("more than one document", func(t *testing.T) {
		if _, err := Doc([]byte("a: 1\n---\nb: 2\n")); err == nil ||
			!strings.Contains(err.Error(), "expected exactly one YAML document, found 2") {
			t.Fatalf("Doc = %v, want document-count error", err)
		}
	})
	t.Run("top level not a mapping", func(t *testing.T) {
		if _, err := Doc([]byte("- a\n")); err == nil ||
			!strings.Contains(err.Error(), "top level must be a mapping") {
			t.Fatalf("Doc = %v, want mapping error", err)
		}
	})
	t.Run("admissibility runs before the mapping assertion", func(t *testing.T) {
		// A top-level anchored scalar is both inadmissible and not a
		// mapping; the guard's rejection must win — inadmissible input
		// is never interpreted, not even to name a better error.
		if _, err := Doc([]byte("&x 1\n")); !errors.Is(err, ErrForbiddenNode) {
			t.Fatalf("Doc = %v, want ErrForbiddenNode before the mapping assertion", err)
		}
	})
}

// The scalar readers: a string in any spelling is its written text, a
// block's line breaks kept; a typed scalar is its source token, never
// the parser's reading; a line is a scalar holding no line break;
// nothing else is any of them.
func TestScalarReaders(t *testing.T) {
	value := func(src string) ast.Node {
		t.Helper()
		return parseBody(t, "k: "+src+"\n").(*ast.MappingNode).Values[0].Value
	}
	cases := []struct {
		src            string
		str, scl, line string
		isStr, isScl   bool
		isLine         bool
	}{
		{"plain", "plain", "plain", "plain", true, true, true},
		{`"quoted"`, "quoted", "quoted", "quoted", true, true, true},
		{"'single'", "single", "single", "single", true, true, true},
		{`"true"`, "true", "true", "true", true, true, true},
		{"|\n  literal\n  block", "literal\nblock\n", "literal\nblock\n", "", true, true, false},
		{">-\n  folded\n  block", "folded block", "folded block", "folded block", true, true, true},
		{">\n  folded", "folded\n", "folded\n", "", true, true, false},
		{`"a\nb"`, "a\nb", "a\nb", "", true, true, false},
		{`"a\rb"`, "a\rb", "a\rb", "", true, true, false},
		{"007", "", "007", "007", false, true, true},
		{"1.50", "", "1.50", "1.50", false, true, true},
		{"True", "", "True", "True", false, true, true},
		{".inf", "", ".inf", ".inf", false, true, true},
		{".nan", "", ".nan", ".nan", false, true, true},
		{"~", "", "", "", false, false, false},
		{"null", "", "", "", false, false, false},
		{"[a]", "", "", "", false, false, false},
		{"{a: b}", "", "", "", false, false, false},
	}
	for _, c := range cases {
		n := value(c.src)
		if s, ok := String(n); ok != c.isStr || s != c.str {
			t.Errorf("String(%q) = %q, %v", c.src, s, ok)
		}
		if s, ok := Scalar(n); ok != c.isScl || s != c.scl {
			t.Errorf("Scalar(%q) = %q, %v", c.src, s, ok)
		}
		if s, ok := Line(n); ok != c.isLine || s != c.line {
			t.Errorf("Line(%q) = %q, %v", c.src, s, ok)
		}
	}
	// An empty value is a null, not an empty string.
	if _, ok := Scalar(value("")); ok {
		t.Error("an absent value read as a scalar")
	}
	// Keys are their spellings.
	m := parseBody(t, "a: 1\n\"b c\": 2\n").(*ast.MappingNode)
	if Key(m.Values[0].Key) != "a" || Key(m.Values[1].Key) != "b c" {
		t.Errorf("keys = %q, %q", Key(m.Values[0].Key), Key(m.Values[1].Key))
	}
}

// The mapping walker: a non-mapping refused; an unknown key refused
// before any value is read; each field's value handed to its reader,
// a reader's error returned as is; required fields missing named in
// field order; the message prefixed by where, or unprefixed at a
// document's top level. Sequence and Strings likewise.
func TestWalkers(t *testing.T) {
	sentinel := errors.New("bad")
	var got []string
	fields := []Field{
		{Name: "a", Required: true, Read: func(n ast.Node) error { s, _ := Line(n); got = append(got, "a="+s); return nil }},
		{Name: "b", Read: func(n ast.Node) error { return errors.New("b refused") }},
		{Name: "c", Required: true, Read: func(n ast.Node) error { got = append(got, "c"); return nil }},
	}
	cases := []struct{ src, where, want string }{
		{"a: 1\nc: 2\n", "", ""},
		{"c: 2\na: 1\n", "x", ""},
		{"a: 1\n", "", "bad: missing c"},
		{"{}\n", "x", "bad: x: missing a and c"},
		{"a: 1\nc: 2\nd: 3\n", "", `bad: unknown key "d" (keys: a, b, c)`},
		{"a: 1\nc: 2\nd: 3\n", "x[0]", `bad: x[0]: unknown key "d" (keys: a, b, c)`},
		{"a: 1\nb: 2\nc: 3\n", "", "b refused"},
		{"- 1\n", "x", "bad: x must be a mapping"},
		{"- 1\n", "", "bad: must be a mapping"},
		// The unknown key is refused before its value or a later
		// field is read.
		{"d: 3\nb: 1\n", "", `bad: unknown key "d" (keys: a, b, c)`},
	}
	for _, c := range cases {
		got = nil
		err := Mapping(parseBody(t, c.src), c.where, sentinel, fields...)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if msg != c.want {
			t.Errorf("%q: %q, want %q", c.src, msg, c.want)
		}
		if err != nil && !errors.Is(err, sentinel) && msg != "b refused" {
			t.Errorf("%q: not the sentinel", c.src)
		}
	}
	got = nil
	if err := Mapping(parseBody(t, "a: 1\nc: 2\n"), "", sentinel, fields...); err != nil || strings.Join(got, ",") != "a=1,c" {
		t.Errorf("readers: %q %v", got, err)
	}
	// A field without a reader is judged by presence alone.
	if err := Mapping(parseBody(t, "k: [1, 2]\n"), "", sentinel, Field{Name: "k", Required: true}); err != nil {
		t.Errorf("presence-only field: %v", err)
	}
	if err := Mapping(parseBody(t, "{}\n"), "", sentinel, Field{Name: "k", Required: true}); err == nil || err.Error() != "bad: missing k" {
		t.Errorf("presence-only field missing: %v", err)
	}
	var idx []int
	if err := Sequence(parseBody(t, "- x\n- y\n"), "s", sentinel, func(i int, n ast.Node) error { idx = append(idx, i); return nil }); err != nil || len(idx) != 2 || idx[1] != 1 {
		t.Errorf("sequence: %v %v", idx, err)
	}
	if err := Sequence(parseBody(t, "k: v\n"), "s", sentinel, nil); err == nil || err.Error() != "bad: s must be a list" || !errors.Is(err, sentinel) {
		t.Errorf("non-list: %v", err)
	}
	if err := Sequence(parseBody(t, "k: v\n"), "", sentinel, nil); err == nil || err.Error() != "bad: must be a list" {
		t.Errorf("non-list, no where: %v", err)
	}
	if _, err := Strings(parseBody(t, "k: v\n"), "", sentinel); err == nil || err.Error() != "bad: must be a list" {
		t.Errorf("strings, no where: %v", err)
	}
	if err := Sequence(parseBody(t, "- x\n"), "s", sentinel, func(int, ast.Node) error { return errors.New("item") }); err == nil || err.Error() != "item" {
		t.Errorf("item error: %v", err)
	}
	if ss, err := Strings(parseBody(t, "- x\n- \"y z\"\n- 7\n"), "l", sentinel); err != nil || strings.Join(ss, ",") != "x,y z,7" {
		t.Errorf("strings: %q %v", ss, err)
	}
	for _, src := range []string{"x\n", "- \"\"\n", "- [a]\n", "- ~\n", "- |\n  a\n  b\n"} {
		if _, err := Strings(parseBody(t, src), "l", sentinel); err == nil || !errors.Is(err, sentinel) {
			t.Errorf("%q admitted: %v", src, err)
		}
	}
	if ss, err := Strings(parseBody(t, "[]\n"), "l", sentinel); err != nil || ss == nil || len(ss) != 0 {
		t.Errorf("empty list: %v %v", ss, err)
	}
}

// Walk hands a key that is no field to the policy given, its value
// unread, where Mapping refuses it.
func TestWalkUnknownPolicy(t *testing.T) {
	m, err := Doc([]byte("a: 1\nb: [1, 2]\nc: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	var passed []string
	read := 0
	err = Walk(m, "", errors.New("bad"), func(key string, value ast.Node) {
		// The value handed over is the key's own.
		if _, seq := value.(*ast.SequenceNode); key == "b" && !seq {
			t.Errorf("b's value: %T", value)
		}
		if s, ok := Line(value); key == "c" && (!ok || s != "x") {
			t.Errorf("c's value: %v %v", s, ok)
		}
		passed = append(passed, key)
	}, Field{Name: "a", Read: func(ast.Node) error { read++; return nil }})
	if err != nil || strings.Join(passed, ",") != "b,c" || read != 1 {
		t.Fatalf("walk: %v %v %d", err, passed, read)
	}
	if err := Mapping(m, "", errors.New("bad"), Field{Name: "a"}); err == nil || !strings.Contains(err.Error(), `unknown key "b"`) {
		t.Fatalf("mapping: %v", err)
	}
}

// Spell quotes what a YAML schema of any version reads as null, a
// boolean or a number — the reader of today or another — a merge or
// value key, an alias-like glob, a mapping, a comment, a control
// character, a line separator of YAML 1.1's, a space at an end; and
// leaves plain what every reader
// reads back as itself, an inner space or a hash included.
func TestSpell(t *testing.T) {
	for _, c := range []struct{ in, out string }{
		{"Null", `"Null"`}, {"~", `"~"`}, {"True", `"True"`}, {"FALSE", `"FALSE"`}, {"12", `"12"`}, {"0", `"0"`}, {"1.0", `"1.0"`}, {"0x1f", `"0x1f"`}, {"-0x1f", `"-0x1f"`}, {"0b1", `"0b1"`}, {"0o7", `"0o7"`}, {".inf", `".inf"`}, {"+.Inf", `"+.Inf"`}, {".NaN", `".NaN"`}, {"+1", `"+1"`}, {"-.5", `"-.5"`},
		{"yes", `"yes"`}, {"YES", `"YES"`}, {"y", `"y"`}, {"On", `"On"`}, {"Off", `"Off"`}, {"NO", `"NO"`}, {"1e3", `"1e3"`}, {"6.8523015e+5", `"6.8523015e+5"`}, {"685_230.15", `"685_230.15"`}, {"2024-01-01", `"2024-01-01"`}, {"2024-01-01T00:00:00Z", `"2024-01-01T00:00:00Z"`}, {"1:20", `"1:20"`}, {"190:20:30", `"190:20:30"`}, {"1_000", `"1_000"`}, {"<<", `"<<"`}, {"=", `"="`},
		{"*.proto", `"*.proto"`}, {"a:", `"a:"`}, {"a #b", `"a #b"`}, {"a\tb", `"a\tb"`}, {"a\x00b", `"a\x00b"`}, {"a\x7fb", `"a\x7fb"`}, {"a\u0085b", `"a\u0085b"`}, {"a\u2028b", `"a\u2028b"`}, {"a\u2029b", `"a\u2029b"`}, {" a", `" a"`}, {"a ", `"a "`}, {"", `""`}, {"[a]", `"[a]"`}, {"{a}", `"{a}"`}, {"!a", `"!a"`}, {"&a", `"&a"`}, {"|", `"|"`}, {"-", `"-"`}, {"'a", `"'a"`},
		{"vendor/**", "vendor/**"}, {"a b", "a b"}, {"a#b", "a#b"}, {"a:b", "a:b"}, {`a"b`, `a"b`}, {"-a", "-a"}, {".", "."}, {"yesterday", "yesterday"}, {"nan", "nan"}, {"inf", "inf"}, {"v1.0.0", "v1.0.0"}, {"e3", "e3"}, {"<<a", "<<a"}, {"a=b", "a=b"}, {"no_sir", "no_sir"}, {"example.com/x:A", "example.com/x:A"},
	} {
		if got := Spell(c.in); got != c.out {
			t.Errorf("Spell(%q) = %s, want %s", c.in, got, c.out)
		}
	}
}
