package contractfile

import (
	"errors"
	"strings"
	"testing"
)

// The writer renders block style canonically: two-space indentation,
// a sequence's entries under their key with the dash in an indent's
// place, nested mappings and sequences at every depth, every scalar
// through Spell, a literal as given, `{}` for nothing.
func TestWriter(t *testing.T) {
	var w Writer
	w.Literal("version", "1")
	w.Scalar("module", "example.com/m")
	w.Scalar("12", "yes")
	w.List("use", []string{"a", "1.0/x"})
	w.List("none", nil)
	w.Empty("fresh")
	w.Mapping("deps", func() {
		w.Scalar("example.com/a", "v1.0.0")
		w.Mapping("nested", func() {
			w.List("inner", []string{"i"})
		})
	})
	w.Literal("12x", "1")
	w.Sequence("entries", 3, func(i int) {
		if i == 2 {
			return // an entry body writes nothing for
		}
		w.Scalar("path", "p")
		if i == 0 {
			w.List("paths", []string{"*.proto", "x"})
			w.Mapping("prov", func() {
				w.Scalar("type", "t")
				w.Sequence("deep", 1, func(int) { w.Scalar("k", "v") })
			})
		}
	})
	want := `version: 1
module: example.com/m
"12": "yes"
use:
  - a
  - "1.0/x"
none: []
fresh: {}
deps:
  example.com/a: v1.0.0
  nested:
    inner:
      - i
"12x": 1
entries:
  - path: p
    paths:
      - "*.proto"
      - x
    prov:
      type: t
      deep:
        - k: v
  - path: p
  - {}
`
	if got := string(w.Bytes()); got != want {
		t.Fatalf("rendering:\n%s", got)
	}
	if got := string(new(Writer).Bytes()); got != "{}\n" {
		t.Fatalf("empty: %q", got)
	}
	// What the writer renders, the reader reads: the document parses and
	// every scalar reads back as the text given.
	doc, err := Doc(w.Bytes())
	if err != nil || doc == nil {
		t.Fatalf("the rendering does not parse: %v", err)
	}
	// Emit holds the rendering to its reading: a reader's refusal is
	// returned as it is, a reading that differs from what was meant is
	// invalid, and a reading that agrees yields the bytes.
	refused, invalid := errors.New("refused"), errors.New("invalid")
	build := func(w *Writer) { w.Scalar("a", "b") }
	if _, err := Emit(build, func([]byte) (string, error) { return "", refused }, "a: b\n", func(a, b string) bool { return a == b }, invalid); !errors.Is(err, refused) {
		t.Fatalf("Emit ignored the refusal: %v", err)
	}
	if _, err := Emit(build, func(b []byte) (string, error) { return "something else", nil }, "a: b\n", func(a, b string) bool { return a == b }, invalid); !errors.Is(err, invalid) || !strings.Contains(err.Error(), "reads back as a different file") {
		t.Fatalf("Emit ignored the difference: %v", err)
	}
	out, err := Emit(build, func(b []byte) (string, error) { return string(b), nil }, "a: b\n", func(a, b string) bool { return a == b }, invalid)
	if err != nil || string(out) != "a: b\n" {
		t.Fatalf("Emit: %q %v", out, err)
	}
}
