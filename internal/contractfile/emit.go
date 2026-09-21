package contractfile

import (
	"fmt"
	"strings"
)

// Writer renders a contract file in block style, canonically: LF line
// endings, two-space indentation, a sequence's entries indented under
// their key with the dash taking an indent's place, every scalar
// spelled by Spell, a schema-typed literal apart. It writes what it
// is told in the order told; a file's key order, list orders and
// absent keys are its own emitter's decisions, made by its spec.
type Writer struct {
	b     strings.Builder
	depth int
	item  bool // the next line opens a sequence entry, `- ` in place of its last indent
}

// prefix is the next line's indentation, or a sequence entry's dash
// where one is pending.
func (w *Writer) prefix() string {
	if w.item {
		w.item = false
		return strings.Repeat("  ", w.depth-1) + "- "
	}
	return strings.Repeat("  ", w.depth)
}

// Scalar writes `key: value`, the value spelled by Spell.
func (w *Writer) Scalar(key, value string) {
	w.b.WriteString(w.prefix() + Spell(key) + ": " + Spell(value) + "\n")
}

// Literal writes `key: value` with the value as given: a scalar the
// file's schema types — an integer version, the word true — which
// Spell would quote into text.
func (w *Writer) Literal(key, value string) {
	w.b.WriteString(w.prefix() + Spell(key) + ": " + value + "\n")
}

// Empty writes `key: {}`, an empty mapping.
func (w *Writer) Empty(key string) {
	w.b.WriteString(w.prefix() + Spell(key) + ": {}\n")
}

// List writes a block sequence of scalars under the key, `key: []`
// for none.
func (w *Writer) List(key string, values []string) {
	if len(values) == 0 {
		w.b.WriteString(w.prefix() + Spell(key) + ": []\n")
		return
	}
	w.b.WriteString(w.prefix() + Spell(key) + ":\n")
	w.depth += 2
	for _, v := range values {
		w.item = true
		w.b.WriteString(w.prefix() + Spell(v) + "\n")
	}
	w.depth -= 2
}

// Mapping writes the key and, one level in, what body writes.
func (w *Writer) Mapping(key string, body func()) {
	w.b.WriteString(w.prefix() + Spell(key) + ":\n")
	w.depth++
	body()
	w.depth--
}

// Sequence writes the key and, one level in, n entries, each a
// mapping opened by a dash on the first line body writes for it; an
// entry body writes nothing for is `{}`, an empty mapping. An entry
// is always a mapping: a sequence of sequences is no contract file's
// shape and the writer does not spell one.
func (w *Writer) Sequence(key string, n int, body func(i int)) {
	w.b.WriteString(w.prefix() + Spell(key) + ":\n")
	w.depth += 2
	for i := 0; i < n; i++ {
		w.item = true
		body(i)
		if w.item {
			w.b.WriteString(w.prefix() + "{}\n")
		}
	}
	w.depth -= 2
}

// Bytes is the rendering: `{}`, an empty mapping, where nothing was
// written.
func (w *Writer) Bytes() []byte {
	if w.b.Len() == 0 {
		return []byte("{}\n")
	}
	return []byte(w.b.String())
}

// Emit renders what build writes and holds the rendering to its
// reading: read is the file's own reader over the bytes, yielding
// the file as read in whatever form the emitter compares — the facts
// free of the spellings that mean nothing — and same compares it to
// what was meant; a reader's refusal is returned as it is, a reading
// that differs as invalid — the file's own sentinel, never nil — so
// no emitter writes what its reader rejects or reads as another
// file.
func Emit[T any](build func(*Writer), read func([]byte) (T, error), want T, same func(a, b T) bool, invalid error) ([]byte, error) {
	var w Writer
	build(&w)
	out := w.Bytes()
	got, err := read(out)
	if err != nil {
		return nil, err
	}
	if !same(got, want) {
		return nil, fmt.Errorf("%w: the rendering reads back as a different file", invalid)
	}
	return out, nil
}
