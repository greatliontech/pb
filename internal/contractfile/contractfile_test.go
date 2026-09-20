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
