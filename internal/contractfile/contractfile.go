// Package contractfile is the one home of pb's strict YAML
// contract-file surface (pb.yaml, pb.lock, pb.trust.yaml): the shared
// document prologue every contract-file parser opens with, and the
// node-admissibility rule it enforces.
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
// parser built on it cannot forget the guard; schema walking stays with
// each format, whose shapes are its own contract.
package contractfile

import (
	"errors"
	"fmt"

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
