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
