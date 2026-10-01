package format

import (
	"errors"
	"fmt"

	"github.com/bufbuild/protocompile/ast"
)

// option writes `option name = value;`, or a compact option's
// `name = value` without keyword and semicolon.
func (p *printer) option(n *ast.OptionNode) {
	p.optionHead(n)
	if n.Semicolon != nil {
		if s, ok := n.Val.(*ast.CompoundStringLiteralNode); ok {
			p.compoundString(s, true, true)
			p.end(n.Semicolon)
			return
		}
		p.within(n.Val)
		p.end(n.Semicolon)
		return
	}
	if s, ok := n.Val.(*ast.CompoundStringLiteralNode); ok {
		p.compoundString(s, true, true)
		return
	}
	p.within(n.Val)
}

// lastCompactOption writes the last option of a multi-line option
// list, its value ending the line with the value's trailing comments.
func (p *printer) lastCompactOption(n *ast.OptionNode) {
	p.optionHead(n)
	p.end(n.Val)
}

// optionHead writes `option name = ` or `name = `.
func (p *printer) optionHead(n *ast.OptionNode) {
	if n.Keyword != nil {
		p.open(n.Keyword, false)
		p.space()
		p.node(n.Name)
	} else {
		p.open(n.Name, false)
	}
	p.space()
	p.within(n.Equals)
	p.space()
}

// optionName writes `go_package`, `(custom.thing)`,
// `(custom.thing).bridge.(another)`. Inside compact options the
// first part opens the line, its comments placed by open.
func (p *printer) optionName(n *ast.OptionNameNode) {
	for k, part := range n.Parts {
		if p.inCompactOptions && k == 0 {
			if part.Open != nil {
				p.node(part.Open)
				p.inlineComments(p.info(part.Open).trailing)
				p.within(part.Name)
			} else {
				p.node(part.Name)
				p.inlineComments(p.info(part.Name).trailing)
			}
			if part.Close != nil {
				p.within(part.Close)
			}
			continue
		}
		if k > 0 {
			p.within(n.Dots[k-1])
		}
		p.node(part)
	}
}

// fieldReference writes `(foo.bar)` or `name` in line.
func (p *printer) fieldReference(n *ast.FieldReferenceNode) {
	if n.Open != nil {
		p.within(n.Open)
	}
	p.within(n.Name)
	if n.Close != nil {
		p.within(n.Close)
	}
}

// compactOptions writes `[name = value]` for one option without a
// compound string value and no comment after `[` or before the
// name, else one option per line between `[` and `]`
// (REQ-format-normalization).
func (p *printer) compactOptions(n *ast.CompactOptionsNode) {
	p.inCompactOptions = true
	defer func() { p.inCompactOptions = false }()
	if len(n.Options) == 1 && !p.interiorComments(n.OpenBracket, n.Options[0].Name) {
		o := n.Options[0]
		p.within(n.OpenBracket)
		p.within(o.Name)
		p.space()
		p.within(o.Equals)
		if s, ok := o.Val.(*ast.CompoundStringLiteralNode); ok {
			p.compoundString(s, false, true)
			p.within(n.CloseBracket)
			return
		}
		p.space()
		p.within(o.Val)
		p.within(n.CloseBracket)
		return
	}
	var elements func(*printer)
	if len(n.Options) > 0 {
		elements = func(p *printer) {
			for k, o := range n.Options {
				if k == len(n.Options)-1 {
					p.lastCompactOption(o)
					return
				}
				p.node(o)
				p.end(n.Commas[k])
			}
		}
	}
	p.valueBody(n.OpenBracket, n.CloseBracket, elements)
}

// arrayLiteral writes `[value]` for one scalar element with no
// comment inside, else one element per line.
func (p *printer) arrayLiteral(n *ast.ArrayLiteralNode) {
	if len(n.Elements) == 1 && !p.interiorComments(n.Children()...) && !nestedLiteral(n.Elements) {
		p.within(n.OpenBracket)
		p.within(n.Elements[0])
		p.within(n.CloseBracket)
		return
	}
	var elements func(*printer)
	if len(n.Elements) > 0 {
		elements = func(p *printer) {
			for k, e := range n.Elements {
				last := k == len(n.Elements)-1
				if c, ok := e.(ast.CompositeNode); ok {
					p.arrayElement(c, last)
					if !last {
						p.end(n.Commas[k])
					}
					continue
				}
				if last {
					p.entry(e)
					return
				}
				p.open(e, false)
				p.end(n.Commas[k])
			}
		}
	}
	p.valueBody(n.OpenBracket, n.CloseBracket, elements)
}

// nestedLiteral reports a message or array literal among values.
func nestedLiteral(values []ast.ValueNode) bool {
	for _, v := range values {
		switch v.(type) {
		case *ast.ArrayLiteralNode, *ast.MessageLiteralNode:
			return true
		}
	}
	return false
}

// arrayElement writes a composite value on its own line of an array
// literal, its comments placed as a line's.
func (p *printer) arrayElement(n ast.CompositeNode, last bool) {
	switch n := n.(type) {
	case *ast.CompoundStringLiteralNode:
		for k, c := range n.Children() {
			if !last && k == len(n.Children())-1 {
				p.open(c, false)
				return
			}
			p.entry(c)
		}
	case *ast.NegativeIntLiteralNode:
		p.open(n.Minus, false)
		if last {
			p.end(n.Uint)
			return
		}
		p.within(n.Uint)
	case *ast.SignedFloatLiteralNode:
		p.open(n.Sign, false)
		if last {
			p.end(n.Float)
			return
		}
		p.within(n.Float)
	case *ast.MessageLiteralNode:
		p.messageLiteralInArray(n, last)
	default:
		p.err = errors.Join(p.err, fmt.Errorf("internal error: no array writer for %T", n))
	}
}

// messageLiteral writes `{name: value}` for at most one scalar
// element with no comment inside, else one element per line, `<`
// and `>` written `{` and `}`.
func (p *printer) messageLiteral(n *ast.MessageLiteralNode) {
	if p.compactMessageLiteral(n, false) {
		return
	}
	var elements func(*printer)
	if len(n.Elements) > 0 {
		elements = func(p *printer) { p.messageLiteralElements(n) }
	}
	closeBrace := literalClose(n)
	p.valueBody(literalOpen(n), closeBrace, elements)
}

// messageLiteralInArray writes a message literal as an element of an
// array: its opener on a line of its own, or the whole on one line
// where compact, the last element then ending its line.
func (p *printer) messageLiteralInArray(n *ast.MessageLiteralNode, last bool) {
	if p.compactMessageLiteral(n, true) {
		if last {
			p.line("")
		}
		return
	}
	var elements func(*printer)
	if len(n.Elements) > 0 {
		elements = func(p *printer) { p.messageLiteralElements(n) }
	}
	closing := p.closeInline
	if last {
		closing = p.close
	}
	closeBrace := literalClose(n)
	p.body(literalOpen(n), closeBrace, elements, p.openerOwnLine, closing)
}

// compactMessageLiteral writes a message literal on one line where it
// has at most one element, that element's value no literal and no
// compound string, and no comment inside; it reports whether it did.
func (p *printer) compactMessageLiteral(n *ast.MessageLiteralNode, inArray bool) bool {
	if len(n.Elements) > 1 || p.interiorComments(n.Children()...) {
		return false
	}
	for _, e := range n.Elements {
		switch e.Val.(type) {
		case *ast.ArrayLiteralNode, *ast.MessageLiteralNode, *ast.CompoundStringLiteralNode:
			return false
		}
	}
	open, closeBrace := literalOpen(n), literalClose(n)
	if inArray {
		p.indent(open)
	}
	p.within(open)
	if len(n.Elements) == 1 {
		f := n.Elements[0]
		p.within(f.Name)
		if f.Sep != nil {
			p.within(f.Sep)
		} else {
			p.text(":")
		}
		p.space()
		if n.Seps[0] != nil {
			p.moveSeparator(n.Seps[0], f.Val)
		}
		p.within(f.Val)
	}
	p.within(closeBrace)
	return true
}

// messageLiteralElements writes the elements one per line, the
// optional separators dropped, a dropped separator's comments carried
// to the element's value.
func (p *printer) messageLiteralElements(n *ast.MessageLiteralNode) {
	for k, e := range n.Elements {
		if n.Seps[k] != nil {
			p.moveSeparator(n.Seps[k], e.Val)
		}
		p.node(e)
	}
}

// messageField writes `name: value` ending its line.
func (p *printer) messageField(n *ast.MessageFieldNode) {
	ref := n.Name
	if ref.Open != nil {
		p.open(ref.Open, false)
		if ref.URLPrefix != nil {
			p.within(ref.URLPrefix)
			p.within(ref.Slash)
		}
		p.within(ref.Name)
	} else {
		p.open(ref.Name, false)
	}
	if ref.Close != nil {
		p.within(ref.Close)
	}
	if n.Sep != nil {
		p.within(n.Sep)
	} else {
		p.text(":")
	}
	p.space()
	if s, ok := n.Val.(*ast.CompoundStringLiteralNode); ok {
		p.compoundString(s, true, false)
		return
	}
	p.end(n.Val)
}

// compoundString writes adjacent string tokens one per line, after
// the line that introduces them, indented one level where indented;
// the last token left open where punctuation follows it on its line.
func (p *printer) compoundString(n *ast.CompoundStringLiteralNode, indented, punctuated bool) {
	p.line("")
	if indented {
		p.level++
	}
	children := n.Children()
	for k, c := range children {
		if punctuated && k == len(children)-1 {
			p.open(c, false)
			break
		}
		p.entry(c)
	}
	if indented {
		p.deeper(-1)
	}
}

// literalOpen and literalClose spell a message literal's delimiters
// as braces whatever the source used.
func literalOpen(n *ast.MessageLiteralNode) *ast.RuneNode {
	if n.Open.Rune == '{' {
		return n.Open
	}
	return ast.NewRuneNode('{', n.Open.Token())
}

func literalClose(n *ast.MessageLiteralNode) *ast.RuneNode {
	if n.Close.Rune == '}' {
		return n.Close
	}
	return ast.NewRuneNode('}', n.Close.Token())
}
