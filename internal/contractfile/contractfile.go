// Package contractfile is the one home of pb's strict YAML
// contract-file surface (pb.yaml, pb.lock, pb.trust.yaml, pb.gen.yaml,
// pb.lint.yaml, the rule files): the shared document prologue every
// contract-file parser opens with, the node-admissibility rule it
// enforces, the scalar readers, and the mapping walk.
//
// Admissibility rejects merge keys, anchors, aliases, and tags
// anywhere in the document: their resolution is parser-defined — YAML
// 1.1 and 1.2 consumers disagree — and a content-addressed contract
// file must read identically under every consumer. Canonical emission
// never produces them, so rejecting them on input loses no expressible
// content. Mapping keys must be strings: a null, boolean, or numeric
// key is outside every contract-file schema, and a null key silently
// defeats strict-mode unknown-field detection in decoders.
//
// Doc runs the admissibility check before any interpretation, so a
// parser built on it cannot forget the guard; each format's schema —
// its keys, its values' grammars, its cross-field rules — stays its
// own contract.
//
// Mapping, Sequence and Strings hold the walk every schema shares —
// the generate file, the trust policy, the module file, the rule
// files, the lint file: a field table declares a mapping's keys, an
// unknown key is refused before any value is read, and a missing key
// is named the same way everywhere. The lockfile and the user
// configuration keep their own readers — a strict typed decode, and
// string scalars alone by their spec — with the same guarantees.
package contractfile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// ErrForbiddenNode is wrapped by every admissibility rejection.
var ErrForbiddenNode = errors.New("forbidden YAML construct")

// Doc parses data as exactly one YAML document, checks admissibility,
// and returns the top-level mapping. An empty or comment-only document
// has no body and returns (nil, nil): presence semantics are the
// caller's. Errors carry no format name — callers wrap them with their
// own invalid-file sentinel.
func Doc(data []byte) (*ast.MappingNode, error) {
	astFile, err := parser.ParseBytes(data, 0)
	if err != nil {
		return nil, err
	}
	if len(astFile.Docs) != 1 {
		return nil, fmt.Errorf("expected exactly one YAML document, found %d", len(astFile.Docs))
	}
	body := astFile.Docs[0].Body
	if body == nil {
		return nil, nil
	}
	if err := Check(body); err != nil {
		return nil, err
	}
	mapping, ok := body.(*ast.MappingNode)
	if !ok {
		return nil, errors.New("top level must be a mapping")
	}
	return mapping, nil
}

// Check walks the node tree and rejects merge keys, anchors, aliases,
// tags, and non-string mapping keys anywhere — including keys, values,
// and nodes nested behind other containers. Doc runs it on every
// document; it is exported for callers checking sub-documents obtained
// another way.
func Check(node ast.Node) error {
	// A nil node matches no case and passes: nothing to reject.
	switch n := node.(type) {
	case *ast.MergeKeyNode:
		return fmt.Errorf("%w: merge keys are not part of the schema", ErrForbiddenNode)
	case *ast.AnchorNode:
		return fmt.Errorf("%w: anchors are not part of the schema", ErrForbiddenNode)
	case *ast.AliasNode:
		return fmt.Errorf("%w: aliases are not part of the schema", ErrForbiddenNode)
	case *ast.TagNode:
		return fmt.Errorf("%w: tags are not part of the schema", ErrForbiddenNode)
	case *ast.MappingNode:
		for _, kv := range n.Values {
			if _, ok := kv.Key.(*ast.StringNode); !ok {
				if err := Check(kv.Key); err != nil {
					return err
				}
				return fmt.Errorf("%w: mapping keys are strings", ErrForbiddenNode)
			}
			if err := Check(kv.Value); err != nil {
				return err
			}
		}
	case *ast.SequenceNode:
		for _, v := range n.Values {
			if err := Check(v); err != nil {
				return err
			}
		}
	}
	return nil
}

// Key is a mapping key's spelling. Check admits string keys alone, so
// every key a parser built on Doc sees is a string node; the fallback
// spells any other node for a parser checking a sub-document it
// obtained another way.
func Key(n ast.Node) string {
	if s, ok := n.(*ast.StringNode); ok {
		return s.Value
	}
	return n.String()
}

// String is a YAML string in any of its spellings — plain, quoted, or
// a block scalar — as written, a block's line breaks kept; any other
// node is not a string.
func String(n ast.Node) (string, bool) {
	switch v := n.(type) {
	case *ast.StringNode:
		return v.Value, true
	case *ast.LiteralNode:
		return v.Value.Value, true
	}
	return "", false
}

// Scalar is a scalar node's written spelling: a string's value, or the
// source token of a number, boolean, infinity or NaN — the text the
// author wrote, never the parser's typed reading. A non-scalar
// (mapping, sequence, null) is not a spelling.
func Scalar(n ast.Node) (string, bool) {
	if s, ok := String(n); ok {
		return s, true
	}
	switch n.(type) {
	case *ast.IntegerNode, *ast.FloatNode, *ast.BoolNode, *ast.InfinityNode, *ast.NanNode:
		return n.GetToken().Value, true
	}
	return "", false
}

// Line is a scalar spelling that is one line of text — a name, a path,
// a reference, a message — in any spelling, a folded block included,
// whose text holds no line break. A literal block, or a block ending
// in its newline, is text and not a line.
func Line(n ast.Node) (string, bool) {
	s, ok := Scalar(n)
	if !ok || strings.ContainsAny(s, "\n\r") {
		return "", false
	}
	return s, true
}

// Field is one key of a contract-file mapping: its name, whether the
// mapping must carry it, and the reader of its value — none for a key
// whose presence is all the walk judges.
type Field struct {
	Name     string
	Required bool
	Read     func(ast.Node) error
}

// Mapping walks a mapping against its fields — the one skeleton every
// contract-file parser shares: the node must be a mapping; a key that
// is no field is refused before any value is read, the refusal
// listing the fields; each field's value is handed to its reader; a
// required field absent is refused, the missing named in field order.
// where names the mapping in messages ("plugins[0]", "breaking.base";
// empty for a document's top level) and sentinel wraps every refusal.
// The YAML parser refuses a duplicate key, so each field reads at
// most once.
func Mapping(n ast.Node, where string, sentinel error, fields ...Field) error {
	m, ok := n.(*ast.MappingNode)
	if !ok {
		return fmt.Errorf("%w: %smust be a mapping", sentinel, prefix(where, " "))
	}
	byName := make(map[string]int, len(fields))
	for i, f := range fields {
		byName[f.Name] = i
	}
	seen := make([]bool, len(fields))
	for _, kv := range m.Values {
		key := Key(kv.Key)
		i, known := byName[key]
		if !known {
			return fmt.Errorf("%w: %sunknown key %q (keys: %s)", sentinel, prefix(where, ": "), key, names(fields))
		}
		seen[i] = true
		if read := fields[i].Read; read != nil {
			if err := read(kv.Value); err != nil {
				return err
			}
		}
	}
	var missing []string
	for i, f := range fields {
		if f.Required && !seen[i] {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %smissing %s", sentinel, prefix(where, ": "), strings.Join(missing, " and "))
	}
	return nil
}

// Sequence walks a list: the node must be a sequence, each item
// visited with its index for messages.
func Sequence(n ast.Node, where string, sentinel error, each func(i int, item ast.Node) error) error {
	seq, ok := n.(*ast.SequenceNode)
	if !ok {
		return fmt.Errorf("%w: %smust be a list", sentinel, prefix(where, " "))
	}
	for i, item := range seq.Values {
		if err := each(i, item); err != nil {
			return err
		}
	}
	return nil
}

// Strings reads a list of non-empty lines of text.
func Strings(n ast.Node, where string, sentinel error) ([]string, error) {
	seq, ok := n.(*ast.SequenceNode)
	if !ok {
		return nil, fmt.Errorf("%w: %smust be a list", sentinel, prefix(where, " "))
	}
	out := make([]string, 0, len(seq.Values))
	for _, v := range seq.Values {
		text, ok := Line(v)
		if !ok || text == "" {
			return nil, fmt.Errorf("%w: %smust hold non-empty lines of text", sentinel, prefix(where, " "))
		}
		out = append(out, text)
	}
	return out, nil
}

// names spells the fields' names for a refusal.
func names(fields []Field) string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f.Name
	}
	return strings.Join(out, ", ")
}

// prefix is where followed by sep, or nothing for an empty where.
func prefix(where, sep string) string {
	if where == "" {
		return ""
	}
	return where + sep
}
