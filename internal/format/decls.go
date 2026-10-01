package format

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bufbuild/protocompile/ast"
)

// writeFile writes the header, then every other declaration in the
// file's order — a blank line between the header and the first, and
// before one carrying a leading comment, the others separated as the
// source separated them — then the comments before the end of the
// file, and ends the last line (REQ-format-header, REQ-format-layout).
func (p *printer) writeFile() {
	p.header()
	headed := p.prev != nil
	for _, d := range p.file.Decls {
		switch d.(type) {
		case *ast.PackageNode, *ast.OptionNode, *ast.ImportNode, *ast.EmptyDeclNode:
			continue
		}
		if p.prev != nil && (headed || len(p.info(d).leading) > 0) && !p.blankLineBefore(d) {
			p.line("")
		}
		headed = false
		p.node(d)
	}
	p.flush()
	if p.file.EOF != nil {
		p.ownLines(p.info(p.file.EOF).leading)
	}
	if p.last != 0 && p.last != '\n' {
		p.line("")
	}
}

// header writes the edition or syntax statement, the package, the
// imports sorted and the file options sorted, a blank line between
// the groups (REQ-format-header).
func (p *printer) header() {
	var pkg *ast.PackageNode
	var imports []*ast.ImportNode
	var options []*ast.OptionNode
	for _, d := range p.file.Decls {
		switch n := d.(type) {
		case *ast.PackageNode:
			pkg = n
		case *ast.ImportNode:
			imports = append(imports, n)
		case *ast.OptionNode:
			options = append(options, n)
		}
	}
	if p.file.Syntax == nil && p.file.Edition == nil && pkg == nil && imports == nil && options == nil {
		return
	}
	first := true
	if e := p.file.Edition; e != nil {
		p.open(e.Keyword, len(p.info(e).leading) == 0)
		p.space()
		p.within(e.Equals)
		p.space()
		p.within(e.Edition)
		p.end(e.Semicolon)
		first = false
	} else if s := p.file.Syntax; s != nil {
		p.open(s.Keyword, len(p.info(s).leading) == 0)
		p.space()
		p.within(s.Equals)
		p.space()
		p.within(s.Syntax)
		p.end(s.Semicolon)
		first = false
	}
	if pkg != nil {
		p.open(pkg.Keyword, first && len(p.info(pkg).leading) == 0)
		p.space()
		p.within(pkg.Name)
		p.end(pkg.Semicolon)
		first = false
	}
	sort.SliceStable(imports, func(a, b int) bool {
		an, bn := imports[a].Name.AsString(), imports[b].Name.AsString()
		if an != bn {
			return an < bn
		}
		if ar, br := importRank(imports[a]), importRank(imports[b]); ar != br {
			return ar < br
		}
		return p.importHasComment(imports[a]) && !p.importHasComment(imports[b])
	})
	for k, imp := range imports {
		if k == 0 && p.prev != nil && !p.blankLineBefore(imp) {
			p.line("")
		}
		if k > 0 && imp.Name.AsString() == imports[k-1].Name.AsString() && !p.importHasComment(imp) {
			continue
		}
		p.importDecl(imp, k > 0, first)
		first = false
	}
	sort.SliceStable(options, func(a, b int) bool {
		an, bn := optionNameText(options[a].Name), optionNameText(options[b].Name)
		ac, bc := strings.HasPrefix(an, "("), strings.HasPrefix(bn, "(")
		if ac != bc {
			return !ac
		}
		return an < bn
	})
	for k, o := range options {
		if k == 0 && p.prev != nil && !p.blankLineBefore(o) {
			p.line("")
		}
		p.fileOption(o, k > 0, first)
		first = false
	}
}

// importRank orders imports of one path: public, then plain, then
// weak.
func importRank(n *ast.ImportNode) int {
	switch {
	case n.Public != nil:
		return 0
	case n.Weak != nil:
		return 2
	}
	return 1
}

func (p *printer) importHasComment(n *ast.ImportNode) bool {
	return p.hasComment(n) || p.hasComment(n.Keyword) || p.hasComment(n.Name) || p.hasComment(n.Semicolon) || p.hasComment(n.Public) || p.hasComment(n.Weak)
}

// importDecl writes `import ["public" | "weak"] "path";`, compact
// (no blank line before it) among the sorted imports.
func (p *printer) importDecl(n *ast.ImportNode, compact, first bool) {
	p.openMaybeCompact(n.Keyword, compact, first && !p.importHasComment(n))
	p.space()
	switch {
	case n.Public != nil:
		p.within(n.Public)
		p.space()
	case n.Weak != nil:
		p.within(n.Weak)
		p.space()
	}
	p.within(n.Name)
	p.end(n.Semicolon)
}

// fileOption writes a file-level option, compact among the sorted
// options.
func (p *printer) fileOption(n *ast.OptionNode, compact, first bool) {
	p.openMaybeCompact(n.Keyword, compact, first && len(p.info(n).leading) == 0)
	p.space()
	p.node(n.Name)
	p.space()
	p.within(n.Equals)
	if s, ok := n.Val.(*ast.CompoundStringLiteralNode); ok {
		p.compoundString(s, true, true)
		p.end(n.Semicolon)
		return
	}
	p.space()
	p.within(n.Val)
	p.end(n.Semicolon)
}

// optionNameText spells an option name for sorting: the parts joined
// by `.`, a custom part in its parentheses.
func optionNameText(n *ast.OptionNameNode) string {
	var b strings.Builder
	for k, part := range n.Parts {
		if k > 0 {
			b.WriteByte('.')
		}
		if part.Open != nil {
			b.WriteByte('(')
		}
		b.WriteString(string(part.Name.AsIdentifier()))
		if part.Close != nil {
			b.WriteByte(')')
		}
	}
	return b.String()
}

// node writes a node by its kind, each kind's writer placing the
// node's own comments.
func (p *printer) node(n ast.Node) {
	switch n := n.(type) {
	case *ast.ArrayLiteralNode:
		p.arrayLiteral(n)
	case *ast.CompactOptionsNode:
		p.compactOptions(n)
	case *ast.CompoundIdentNode:
		p.compoundIdent(n)
	case *ast.CompoundStringLiteralNode:
		p.compoundString(n, true, false)
	case *ast.EnumNode:
		p.enum(n)
	case *ast.EnumValueNode:
		p.enumValue(n)
	case *ast.ExtendNode:
		p.extend(n)
	case *ast.ExtensionRangeNode:
		p.extensions(n)
	case ast.FieldLabel:
		p.text(n.Val)
	case *ast.FieldNode:
		p.field(n)
	case *ast.FieldReferenceNode:
		p.fieldReference(n)
	case *ast.FloatLiteralNode:
		p.raw(n)
	case *ast.GroupNode:
		p.group(n)
	case *ast.IdentNode:
		p.text(n.Val)
	case *ast.ImportNode:
		p.importDecl(n, false, false)
	case *ast.KeywordNode:
		p.text(n.Val)
	case *ast.MapFieldNode:
		p.mapField(n)
	case *ast.MapTypeNode:
		p.mapType(n)
	case *ast.MessageNode:
		p.message(n)
	case *ast.MessageFieldNode:
		p.messageField(n)
	case *ast.MessageLiteralNode:
		p.messageLiteral(n)
	case *ast.NegativeIntLiteralNode:
		p.within(n.Minus)
		p.within(n.Uint)
	case *ast.OneofNode:
		p.oneof(n)
	case *ast.OptionNode:
		p.option(n)
	case *ast.OptionNameNode:
		p.optionName(n)
	case *ast.PackageNode:
		p.open(n.Keyword, false)
		p.space()
		p.within(n.Name)
		p.end(n.Semicolon)
	case *ast.RangeNode:
		p.rangeNode(n)
	case *ast.ReservedNode:
		p.reserved(n)
	case *ast.RPCNode:
		p.rpc(n)
	case *ast.RPCTypeNode:
		p.within(n.OpenParen)
		if n.Stream != nil {
			p.within(n.Stream)
			p.space()
		}
		p.within(n.MessageType)
		p.within(n.CloseParen)
	case *ast.RuneNode:
		if strings.ContainsRune("{[(<", n.Rune) {
			p.opened++
		} else if strings.ContainsRune("}])>", n.Rune) {
			p.opened--
		}
		p.text(string(n.Rune))
	case *ast.ServiceNode:
		p.service(n)
	case *ast.SignedFloatLiteralNode:
		p.within(n.Sign)
		p.within(n.Float)
	case *ast.SpecialFloatLiteralNode:
		p.text(n.KeywordNode.Val)
	case *ast.StringLiteralNode:
		p.raw(n)
	case *ast.SyntaxNode:
		p.open(n.Keyword, len(p.info(n).leading) == 0)
		p.space()
		p.within(n.Equals)
		p.space()
		p.within(n.Syntax)
		p.end(n.Semicolon)
	case *ast.UintLiteralNode:
		p.raw(n)
	case *ast.EmptyDeclNode:
	default:
		p.err = errors.Join(p.err, fmt.Errorf("internal error: no writer for %T", n))
	}
}

// raw writes a token's text as the source spells it.
func (p *printer) raw(n ast.Node) { p.text(p.info(n).raw) }

// message writes `message Name {` and its body.
func (p *printer) message(n *ast.MessageNode) {
	p.open(n.Keyword, false)
	p.space()
	p.within(n.Name)
	p.space()
	p.typeBody(n.OpenBrace, n.CloseBrace, decls(n.Decls))
}

func (p *printer) enum(n *ast.EnumNode) {
	p.open(n.Keyword, false)
	p.space()
	p.within(n.Name)
	p.space()
	p.typeBody(n.OpenBrace, n.CloseBrace, decls(n.Decls))
}

func (p *printer) extend(n *ast.ExtendNode) {
	p.open(n.Keyword, false)
	p.space()
	p.within(n.Extendee)
	p.space()
	p.typeBody(n.OpenBrace, n.CloseBrace, decls(n.Decls))
}

func (p *printer) service(n *ast.ServiceNode) {
	p.open(n.Keyword, false)
	p.space()
	p.within(n.Name)
	p.space()
	p.typeBody(n.OpenBrace, n.CloseBrace, decls(n.Decls))
}

func (p *printer) oneof(n *ast.OneofNode) {
	p.open(n.Keyword, false)
	p.space()
	p.within(n.Name)
	p.space()
	p.typeBody(n.OpenBrace, n.CloseBrace, decls(n.Decls))
}

// rpc writes `rpc Name(In) returns (Out)` and `;` or its body.
func (p *printer) rpc(n *ast.RPCNode) {
	p.open(n.Keyword, false)
	p.space()
	p.within(n.Name)
	p.within(n.Input)
	p.space()
	p.within(n.Returns)
	p.space()
	p.within(n.Output)
	if n.OpenBrace == nil {
		p.end(n.Semicolon)
		return
	}
	p.space()
	p.typeBody(n.OpenBrace, n.CloseBrace, decls(n.Decls))
}

// group writes `[label] group Name = tag [options] {` and its body.
func (p *printer) group(n *ast.GroupNode) {
	if n.Label.KeywordNode != nil {
		p.open(n.Label, false)
		p.space()
		p.within(n.Keyword)
	} else {
		p.open(n.Keyword, false)
	}
	p.space()
	p.within(n.Name)
	p.space()
	p.within(n.Equals)
	p.space()
	p.within(n.Tag)
	if n.Options != nil {
		p.space()
		p.node(n.Options)
	}
	p.space()
	p.typeBody(n.OpenBrace, n.CloseBrace, decls(n.Decls))
}

// field writes `[label] type name = tag [options];`.
func (p *printer) field(n *ast.FieldNode) {
	if n.Label.KeywordNode != nil {
		p.open(n.Label, false)
		p.space()
		p.within(n.FldType)
	} else if c, ok := n.FldType.(*ast.CompoundIdentNode); ok {
		p.compoundIdentOpening(c)
	} else {
		p.open(n.FldType, false)
	}
	p.space()
	p.within(n.Name)
	p.space()
	if n.Equals != nil {
		p.within(n.Equals)
		p.space()
	}
	if n.Tag != nil {
		p.within(n.Tag)
	}
	if n.Options != nil {
		p.space()
		p.node(n.Options)
	}
	p.end(n.Semicolon)
}

func (p *printer) mapField(n *ast.MapFieldNode) {
	p.node(n.MapType)
	p.space()
	p.within(n.Name)
	p.space()
	p.within(n.Equals)
	p.space()
	p.within(n.Tag)
	if n.Options != nil {
		p.space()
		p.node(n.Options)
	}
	p.end(n.Semicolon)
}

func (p *printer) mapType(n *ast.MapTypeNode) {
	p.open(n.Keyword, false)
	p.within(n.OpenAngle)
	p.within(n.KeyType)
	p.within(n.Comma)
	p.space()
	p.within(n.ValueType)
	p.within(n.CloseAngle)
}

func (p *printer) enumValue(n *ast.EnumValueNode) {
	p.open(n.Name, false)
	p.space()
	p.within(n.Equals)
	p.space()
	p.within(n.Number)
	if n.Options != nil {
		p.space()
		p.node(n.Options)
	}
	p.end(n.Semicolon)
}

// extensions writes `extensions ranges [options];`.
func (p *printer) extensions(n *ast.ExtensionRangeNode) {
	p.open(n.Keyword, false)
	p.space()
	for k, r := range n.Ranges {
		if k > 0 {
			p.within(n.Commas[k-1])
			p.space()
		}
		p.node(r)
	}
	if n.Options != nil {
		p.space()
		p.node(n.Options)
	}
	p.end(n.Semicolon)
}

// reserved writes `reserved entries;`, the entries ranges, names or
// identifiers.
func (p *printer) reserved(n *ast.ReservedNode) {
	p.open(n.Keyword, false)
	var entries []ast.Node
	switch {
	case n.Names != nil:
		for _, s := range n.Names {
			entries = append(entries, s)
		}
	case n.Identifiers != nil:
		for _, id := range n.Identifiers {
			entries = append(entries, id)
		}
	case n.Ranges != nil:
		for _, r := range n.Ranges {
			entries = append(entries, r)
		}
	}
	p.space()
	for k, e := range entries {
		if k > 0 {
			p.within(n.Commas[k-1])
			p.space()
		}
		p.within(e)
	}
	p.end(n.Semicolon)
}

func (p *printer) rangeNode(n *ast.RangeNode) {
	p.within(n.StartVal)
	if n.To != nil {
		p.space()
		p.within(n.To)
	}
	switch {
	case n.EndVal != nil:
		p.space()
		p.within(n.EndVal)
	case n.Max != nil:
		p.space()
		p.within(n.Max)
	}
}

func (p *printer) compoundIdent(n *ast.CompoundIdentNode) {
	if n.LeadingDot != nil {
		p.within(n.LeadingDot)
	}
	for k, c := range n.Components {
		if k > 0 {
			p.within(n.Dots[k-1])
		}
		p.within(c)
	}
}

// compoundIdentOpening writes a qualified name that opens a line, a
// field's type: its first token's comments on lines of their own.
func (p *printer) compoundIdentOpening(n *ast.CompoundIdentNode) {
	if n.LeadingDot != nil {
		p.open(n.LeadingDot, false)
	}
	for k, c := range n.Components {
		if k == 0 && n.LeadingDot == nil {
			p.open(c, false)
			continue
		}
		if k > 0 {
			p.within(n.Dots[k-1])
		}
		p.within(c)
	}
}

// decls is a body's declarations as a writer, nil for none; an empty
// declaration, a bare `;`, is none (REQ-format-normalization).
func decls[T ast.Node](ds []T) func(*printer) {
	var kept []ast.Node
	for _, d := range ds {
		if _, empty := ast.Node(d).(*ast.EmptyDeclNode); !empty {
			kept = append(kept, d)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return func(p *printer) {
		for _, d := range kept {
			p.node(d)
		}
	}
}

// typeBody writes a declaration's body: `{}` where it has no element
// and no comment inside, else the opener ending its line, the
// elements, and the closer on a line of its own ending the
// declaration.
func (p *printer) typeBody(open, closeBrace *ast.RuneNode, elements func(*printer)) {
	p.body(open, closeBrace, elements, p.openerLine, p.close)
}

// valueBody writes a value's body — compact options, an array or
// message literal — the closer followed by the rest of the statement
// on its line.
func (p *printer) valueBody(open, closeBrace *ast.RuneNode, elements func(*printer)) {
	p.body(open, closeBrace, elements, p.openerLine, p.closeInline)
}

func (p *printer) body(open, closeBrace *ast.RuneNode, elements func(*printer), opening func(ast.Node), closing func(ast.Node, bool)) {
	if elements == nil && !p.interiorComments(open, closeBrace) {
		p.within(open)
		closing(closeBrace, true)
		return
	}
	opening(open)
	if elements != nil {
		elements(p)
	}
	closing(closeBrace, false)
}

// openerLine writes an opening delimiter ending its line: its leading
// comments in line before it, its trailing comments after it as they
// are.
func (p *printer) openerLine(open ast.Node) {
	defer func() { p.prev = open }()
	i := p.info(open)
	p.leadIn(i)
	p.node(open)
	if len(i.trailing) > 0 {
		p.endComments(i.trailing)
	} else {
		p.line("")
	}
}

// openerOwnLine writes an opening delimiter on a line of its own, a
// message literal's inside an array: its leading comments on lines
// above it.
func (p *printer) openerOwnLine(open ast.Node) {
	defer func() { p.prev = open }()
	i := p.info(open)
	p.flush()
	p.ownLines(i.leading)
	p.indent(open)
	p.node(open)
	if len(i.trailing) > 0 {
		p.endComments(i.trailing)
	} else {
		p.line("")
	}
}
