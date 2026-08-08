// Package yamlshape rejects YAML node kinds that have no place in pb's
// contract files (pb.yaml, pb.lock): merge keys, anchors, aliases, and
// tags. Their resolution is parser-defined — YAML 1.1 and 1.2 consumers
// disagree — and a content-addressed contract file must read identically
// under every consumer. Canonical emission never produces them, so
// rejecting them on input loses no expressible content.
package yamlshape

import (
	"errors"
	"fmt"

	"github.com/goccy/go-yaml/ast"
)

// ErrForbiddenNode is wrapped by every rejection.
var ErrForbiddenNode = errors.New("forbidden YAML construct")

// Check walks the node tree and rejects merge keys, anchors, aliases, and
// tags anywhere — including keys, values, and nodes nested behind other
// containers.
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
			// Schema keys are strings; a null, boolean, or numeric key is
			// outside every contract-file schema, and a null key silently
			// defeats strict-mode unknown-field detection in the decoder.
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
