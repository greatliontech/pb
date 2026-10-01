// Package format writes a protobuf file in its canonical form
// (format.md): a fixed style, buf's, that is a pure function of the
// file's tokens and comments. The file is parsed into protocompile's
// AST, which carries every token and every comment with the whitespace
// before it, and printed again by a writer that lays each
// declaration out as the spec says, keeping only the blank-line facts
// of the source. The printer is line-oriented: a declaration's first
// token opens a line under the current indentation, tokens within a
// line are separated by requested spaces that the token classes
// admit, and a line ends through the token that closes it; comments
// ride their tokens, on lines of their own before a line-opening
// token and in line everywhere else. The comments are read into the
// printer's own records first, so a comment attached to a token the
// canonical form drops can move to the token that stands for it.
package format

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bufbuild/protocompile/ast"
	"github.com/bufbuild/protocompile/parser"
	"github.com/bufbuild/protocompile/reporter"
)

// ErrParse is wrapped by a formatting refused for a file that does
// not parse as protobuf.
var ErrParse = errors.New("does not parse")

// Format returns the canonical form of a protobuf file's source
// (REQ-format-pure, REQ-format-idempotent, REQ-format-tokens), name
// naming the file in a parse error.
func Format(name string, src []byte) ([]byte, error) {
	var errs []error
	handler := reporter.NewHandler(reporter.NewReporter(
		func(err reporter.ErrorWithPos) error { errs = append(errs, err); return nil },
		func(reporter.ErrorWithPos) {},
	))
	file, err := parser.Parse(name, bytes.NewReader(src), handler)
	if err != nil || len(errs) > 0 {
		if len(errs) > 0 {
			err = errors.Join(errs...)
		}
		return nil, fmt.Errorf("%w: %w", ErrParse, err)
	}
	p := &printer{file: file, leading: map[ast.Node][]comment{}, trailing: map[ast.Node][]comment{}}
	p.relocate()
	p.writeFile()
	if p.err != nil {
		return nil, p.err
	}
	return p.out.Bytes(), nil
}

// printer lays a parsed file out. The writer keeps the little state
// the layout needs: the indentation, the last byte written, whether a
// space is wanted before the next text, whether tokens are being
// written in line (which decides which delimiters refuse a space
// beside them), the net count of delimiters opened on the current
// line (which indents the lines that follow), and the node written
// last (whose kind decides whether a blank line may precede the
// next).
type printer struct {
	file  *ast.FileNode
	out   bytes.Buffer
	err   error
	level int  // indentation, in levels of two spaces
	last  rune // the last byte written; 0 before any
	// A space wanted before the next text, written unless the token
	// classes on either side refuse it.
	spaceWanted bool
	// Tokens are being written in line: an opening delimiter refuses
	// a space after it and a closing one before it.
	inline bool
	// Delimiters opened less delimiters closed on the current line:
	// positive indents the next lines, negative outdents them.
	opened int
	// The last node whose comments were written; an opening delimiter
	// here means the next node is the first of a body.
	prev ast.Node
	// Comments placed on a node by the printer, in place of the
	// source's: those of a dropped token moved to the token standing
	// for it (REQ-format-comments).
	leading, trailing map[ast.Node][]comment
	// Compact options are being written, their first token's comments
	// already placed.
	inCompactOptions bool
	// Comments carried past the end of a line — a token's second and
	// further trailing comments, which the parser would read as the
	// leading comments of what follows — written on lines of their
	// own when the next line opens, at that line's level.
	pending []comment
}

// comment is one comment of the source: its text as written and the
// whitespace before it, whose newlines are the blank-line facts.
type comment struct {
	text string
	ws   string
}

// nodeInfo is what the printer knows of a node: the comments before
// and after it, the whitespace before it (after its last leading
// comment, where it has one) and its text as written.
type nodeInfo struct {
	leading, trailing []comment
	ws, raw           string
}

// info reads a node's facts from the file: the leading comments its
// first token's, the trailing ones its last token's, whatever node a
// writer asks by — a declaration, a value, a label or the token
// itself — so a comment the printer moved onto a token is found by
// every reader of the node holding it.
func (p *printer) info(n ast.Node) nodeInfo {
	n = token(n)
	i := p.file.NodeInfo(n)
	first, last := firstToken(n), lastToken(n)
	out := nodeInfo{ws: i.LeadingWhitespace(), raw: i.RawText()}
	if moved, ok := p.leading[first]; ok {
		out.leading = moved
	} else {
		out.leading = comments(i.LeadingComments())
	}
	if moved, ok := p.trailing[last]; ok {
		out.trailing = moved
	} else {
		out.trailing = comments(i.TrailingComments())
	}
	return out
}

// token is a node by its own identity: a field label, a value holding
// its keyword, stands for the keyword node.
func token(n ast.Node) ast.Node {
	if l, ok := n.(ast.FieldLabel); ok && l.KeywordNode != nil {
		return l.KeywordNode
	}
	return n
}

func comments(cs ast.Comments) []comment {
	out := make([]comment, 0, cs.Len())
	for k := range cs.Len() {
		c := cs.Index(k)
		out = append(out, comment{text: c.RawText(), ws: c.LeadingWhitespace()})
	}
	return out
}

// moveSeparator gives an element's value every comment of the
// separator the canonical form drops after it, the separator's
// leading and trailing comments after the value's own trailing ones
// (REQ-format-comments).
func (p *printer) moveSeparator(sep, value ast.Node) {
	i := p.info(sep)
	p.appendTrailing(value, append(append([]comment(nil), i.leading...), i.trailing...))
}

// appendTrailing adds comments after a node's trailing ones, on its
// last token, where every reader of the node finds them.
func (p *printer) appendTrailing(to ast.Node, cs []comment) {
	if len(cs) == 0 {
		return
	}
	own := p.info(to).trailing
	moved := make([]comment, 0, len(own)+len(cs))
	moved = append(moved, own...)
	moved = append(moved, cs...)
	p.trailing[lastToken(token(to))] = moved
}

// relocate moves the comments of every empty declaration — a bare
// `;`, which the canonical form drops — before the leading comments
// of the declaration after it, or of the closing delimiter of its
// body, or of the end of the file (REQ-format-comments).
func (p *printer) relocate() {
	var walk func(decls []ast.Node, closer ast.Node)
	walk = func(decls []ast.Node, closer ast.Node) {
		var carried []comment
		for _, d := range decls {
			if e, ok := d.(*ast.EmptyDeclNode); ok {
				i := p.info(e.Semicolon)
				carried = append(carried, i.leading...)
				carried = append(carried, i.trailing...)
				continue
			}
			if len(carried) > 0 {
				first := firstToken(d)
				p.leading[first] = append(carried, p.info(first).leading...)
				carried = nil
			}
			if body, ok := bodyOf(d); ok {
				walk(body.decls, body.closer)
			}
		}
		if len(carried) > 0 && closer != nil {
			p.leading[closer] = append(carried, p.info(closer).leading...)
		}
	}
	decls := make([]ast.Node, len(p.file.Decls))
	for k, d := range p.file.Decls {
		decls[k] = d
	}
	var eof ast.Node
	if p.file.EOF != nil {
		eof = p.file.EOF
	}
	walk(decls, eof)
}

// body is a declaration's elements and closing delimiter.
type body struct {
	decls  []ast.Node
	closer ast.Node
}

// bodyOf is the body of a declaration that has one.
func bodyOf(n ast.Node) (body, bool) {
	switch n := n.(type) {
	case *ast.MessageNode:
		return body{nodes(n.Decls), n.CloseBrace}, true
	case *ast.GroupNode:
		return body{nodes(n.Decls), n.CloseBrace}, true
	case *ast.EnumNode:
		return body{nodes(n.Decls), n.CloseBrace}, true
	case *ast.ServiceNode:
		return body{nodes(n.Decls), n.CloseBrace}, true
	case *ast.RPCNode:
		if n.OpenBrace == nil {
			return body{}, false
		}
		return body{nodes(n.Decls), n.CloseBrace}, true
	case *ast.OneofNode:
		return body{nodes(n.Decls), n.CloseBrace}, true
	case *ast.ExtendNode:
		return body{nodes(n.Decls), n.CloseBrace}, true
	}
	return body{}, false
}

// nodes widens a slice of declarations to nodes.
func nodes[T ast.Node](ds []T) []ast.Node {
	out := make([]ast.Node, len(ds))
	for k, d := range ds {
		out[k] = d
	}
	return out
}

// firstToken is the first terminal token under a node.
func firstToken(n ast.Node) ast.Node {
	for {
		c, ok := n.(ast.CompositeNode)
		if !ok {
			return n
		}
		children := c.Children()
		if len(children) == 0 {
			return n
		}
		n = children[0]
	}
}

// lastToken is the last terminal token under a node.
func lastToken(n ast.Node) ast.Node {
	for {
		c, ok := n.(ast.CompositeNode)
		if !ok {
			return n
		}
		children := c.Children()
		if len(children) == 0 {
			return n
		}
		n = children[len(children)-1]
	}
}

// text writes s, a space before it where one is wanted and admitted:
// never at the start of a line, after a space, or before a newline,
// `,` or `;`; in line, never after an opening delimiter or before a
// closing one either (REQ-format-layout).
func (p *printer) text(s string) {
	if p.spaceWanted {
		p.spaceWanted = false
		first, _ := utf8.DecodeRuneInString(s)
		after, before := "\x00 \t\n", "\n;,"
		if p.inline {
			after, before = "\x00 \t\n<[{(", "\n;,)]}>"
		}
		if !strings.ContainsRune(after, p.last) && !strings.ContainsRune(before, first) {
			p.out.WriteByte(' ')
		}
	}
	if s == "" {
		return
	}
	p.last, _ = utf8.DecodeLastRuneInString(s)
	p.out.WriteString(s)
}

// line writes s as the rest of the current line — indented where it
// opens the line and is not blank — and ends the line, the lines that
// follow indented by the delimiters this one opened. A blank line
// before anything is written is no line: the file never opens with
// one.
func (p *printer) line(s string) {
	if strings.TrimSpace(s) != "" {
		p.indent(nil)
		p.text(s)
	} else if p.last == 0 {
		return
	}
	p.text("\n")
	if p.opened > 0 {
		p.level++
	} else if p.opened < 0 {
		p.deeper(-1)
	}
	p.opened = 0
}

// space asks for a space before the next text.
func (p *printer) space() { p.spaceWanted = true }

// deeper moves the indentation by n levels, never below zero.
func (p *printer) deeper(n int) {
	p.level += n
	if p.level < 0 {
		p.level = 0
		p.err = errors.Join(p.err, errors.New("internal error: indentation below zero"))
	}
}

// indent writes the indentation at the start of a line, one level
// less for a closing delimiter, which stands at its opener's level;
// elsewhere on a line it writes nothing.
func (p *printer) indent(next ast.Node) {
	if p.last != '\n' {
		return
	}
	level := p.level
	if r, ok := next.(*ast.RuneNode); ok && level > 0 && strings.ContainsRune("}])>", r.Rune) {
		level--
	}
	p.text(strings.Repeat("  ", level))
}

// opener reports whether a node is an opening delimiter: the node
// before the first element of a body.
func opener(n ast.Node) bool {
	r, ok := n.(*ast.RuneNode)
	return ok && (r.Rune == '{' || r.Rune == '[' || r.Rune == '<')
}

// blankLineBefore reports whether the source had a blank line between
// a node's previous token and the node, before its leading comments
// or between them and the node.
func (p *printer) blankLineBefore(n ast.Node) bool {
	i := p.info(n)
	for _, c := range i.leading {
		if newlines(c.ws) > 1 {
			return true
		}
	}
	return newlines(i.ws) > 1
}

// blankLineOpens reports whether the source had a blank line before
// the first of a node's leading comments, or before the node where it
// has none: what separates the declaration, comments and all, from
// what precedes it.
func (p *printer) blankLineOpens(n ast.Node) bool {
	i := p.info(n)
	if len(i.leading) > 0 {
		return newlines(i.leading[0].ws) > 1
	}
	return newlines(i.ws) > 1
}

func newlines(s string) int { return strings.Count(s, "\n") }

// hasComment reports whether any comment is attached to a node, a
// nil node carrying none.
func (p *printer) hasComment(n ast.Node) bool {
	if n == nil || isNil(n) {
		return false
	}
	i := p.info(n)
	return len(i.leading) > 0 || len(i.trailing) > 0
}

// isNil reports a typed nil pointer behind the interface.
func isNil(n ast.Node) bool {
	switch v := n.(type) {
	case *ast.KeywordNode:
		return v == nil
	case *ast.RuneNode:
		return v == nil
	case *ast.IdentNode:
		return v == nil
	case *ast.StringLiteralNode:
		return v == nil
	case *ast.CompoundStringLiteralNode:
		return v == nil
	}
	return false
}

// interiorComments reports whether, among consecutive nodes, any but
// the first has leading comments or any but the last has trailing
// ones: a comment between the nodes, which keeps them off one line.
func (p *printer) interiorComments(nodes ...ast.Node) bool {
	for k, n := range nodes {
		i := p.info(n)
		if k > 0 && len(i.leading) > 0 {
			return true
		}
		if k < len(nodes)-1 && len(i.trailing) > 0 {
			return true
		}
	}
	return false
}

// open writes a node that opens a line (REQ-format-comments): its
// leading comments on lines of their own above it — a blank line
// kept where the source had one before a comment, or between the
// last comment and the node, never before the first node of a body,
// never where compact says so — then the node, indented, then its
// trailing comments in line; a composite node's children place their
// own comments. ignoreBlank drops the blank-line fact before the
// node: the header's first node never follows one.
func (p *printer) open(n ast.Node, ignoreBlank bool) {
	p.openMaybeCompact(n, false, ignoreBlank)
}

func (p *printer) openMaybeCompact(n ast.Node, compact, ignoreBlank bool) {
	p.flush()
	defer func() { p.prev = n }()
	i := p.info(n)
	blank := newlines(i.ws) > 1
	tight := compact || opener(p.prev)
	if ignoreBlank {
		blank = false
	}
	if len(i.leading) > 0 {
		p.ownLinesMaybeCompact(i.leading, compact)
		if !compact && blank {
			// Between the last comment and the node: a blank line the
			// source had, a block comment consuming no newline.
			p.line("")
		}
	} else if !tight && blank {
		p.line("")
	}
	p.indent(n)
	p.node(n)
	if _, composite := n.(ast.CompositeNode); !composite {
		p.inlineComments(i.trailing)
	}
}

// within writes a node in line: its leading comments in line before
// it, a space after them where the source had whitespace, the node,
// its trailing comments in line after it. A composite node writes
// its own children's comments.
func (p *printer) within(n ast.Node) {
	inline := p.inline
	p.inline = true
	defer func() { p.inline = inline }()
	if _, composite := n.(ast.CompositeNode); composite {
		p.node(n)
		return
	}
	defer func() { p.prev = n }()
	i := p.info(n)
	p.leadIn(i)
	p.node(n)
	p.inlineComments(i.trailing)
}

// leadIn writes a node's leading comments in line before it, a space
// after them where the source had whitespace between the last and
// the node.
func (p *printer) leadIn(i nodeInfo) {
	if len(i.leading) > 0 {
		p.inlineComments(i.leading)
		if i.ws != "" {
			p.space()
		}
	}
}

// close writes a body's closing delimiter: its leading comments on
// lines of their own above it, the delimiter at its opener's level,
// its trailing comments after it — as they are and ending the line
// where the delimiter ends the statement, in line where more of the
// statement follows (`];`, `},`); for an empty body (tight) the
// leading comments go in line before it instead.
func (p *printer) close(n ast.Node, tight bool) { p.closer(n, tight, true) }

// closeInline writes a closing delimiter more of the statement
// follows on the line.
func (p *printer) closeInline(n ast.Node, tight bool) { p.closer(n, tight, false) }

func (p *printer) closer(n ast.Node, tight, ends bool) {
	if _, composite := n.(ast.CompositeNode); composite {
		p.node(n)
		if ends && p.last != '\n' {
			p.line("")
		}
		return
	}
	defer func() { p.prev = n }()
	i := p.info(n)
	if tight {
		p.leadIn(i)
	} else {
		p.flush()
		p.ownLines(i.leading)
		p.indent(n)
	}
	p.node(n)
	if ends {
		p.endComments(i.trailing)
	} else {
		p.inlineComments(i.trailing)
	}
}

// flush writes the comments carried past the end of the last line,
// on lines of their own, where the next line opens.
func (p *printer) flush() {
	if len(p.pending) == 0 {
		return
	}
	cs := p.pending
	p.pending = nil
	p.ownLines(cs)
}

// entry writes a node as a whole line of a body: as close writes a
// closer, the node standing alone on its line.
func (p *printer) entry(n ast.Node) { p.close(n, false) }

// end writes the token that ends a line (`;`, a list entry's `,` or
// last value): its leading comments in line, the token, its trailing
// comments as they are, then the end of the line. A composite value
// ending a line writes its children: a compound string ends its own
// line through its last token; a signed number's and a literal's
// last token has its trailing comments held back to end the line —
// as a token's would, or in line for a literal's closing delimiter —
// any further comment carried to the next line.
func (p *printer) end(n ast.Node) {
	if _, composite := n.(ast.CompositeNode); composite {
		if _, compound := n.(*ast.CompoundStringLiteralNode); compound {
			p.node(n)
			return
		}
		last := lastToken(n)
		cs := p.info(n).trailing
		p.trailing[last] = nil
		p.node(n)
		if _, delimiter := last.(*ast.RuneNode); delimiter {
			// The closer's one comment in line, unless a `//` comment
			// holding `*/`, which ends the line as it is.
			if len(cs) > 0 && strings.HasPrefix(cs[0].text, "//") && strings.Contains(cs[0].text, "*/") {
				p.space()
				p.text(strings.TrimSpace(cs[0].text))
			} else {
				p.inlineComments(cs[:min(1, len(cs))])
			}
			p.line("")
			p.pending = append(p.pending, cs[min(1, len(cs)):]...)
			return
		}
		p.space()
		p.endComments(cs)
		return
	}
	defer func() { p.prev = n }()
	i := p.info(n)
	p.leadIn(i)
	p.node(n)
	p.space()
	p.endComments(i.trailing)
}

// ownLines writes comments on lines of their own, a blank line kept
// between two where the source had one (REQ-format-comments).
func (p *printer) ownLines(cs []comment) { p.ownLinesMaybeCompact(cs, false) }

func (p *printer) ownLinesMaybeCompact(cs []comment, compact bool) {
	tight := compact || opener(p.prev)
	for _, c := range cs {
		if !tight && newlines(c.ws) > 1 {
			p.line("")
		}
		tight = false
		p.comment(c.text)
		p.text("\n")
	}
}

// inlineComments writes comments in line as block comments: a `//`
// comment as `/* text */`, a block comment spanning lines joined on
// one, a space before each but the first where the source had
// whitespace before it, or after a `;` or `}`. A `//` comment whose
// text holds `*/` can be no block comment: it is written as it is,
// ending its line, the rest of the statement continuing on the next
// (REQ-format-comments).
func (p *printer) inlineComments(cs []comment) {
	for k, c := range cs {
		if k > 0 || c.ws != "" || p.last == ';' || p.last == '}' {
			p.space()
		}
		text := c.text
		if strings.HasPrefix(text, "//") {
			if strings.Contains(text, "*/") {
				p.text(strings.TrimSpace(text))
				p.line("")
				p.indent(nil)
				continue
			}
			text = "/* " + strings.TrimSpace(strings.TrimPrefix(text, "//")) + " */"
		} else {
			lines := strings.Split(text, "\n")
			for j := range lines {
				lines[j] = strings.TrimSpace(lines[j])
			}
			text = strings.Join(lines, " ")
		}
		p.text(text)
	}
}

// endComments writes a line's trailing comments: the first as it is
// after the line's last token, a space before it where the source
// had whitespace, ending the line; any further comment — moved here
// from a dropped token, since the parser attaches one trailing
// comment to a token — carried to the next line, where it opens on a
// line of its own at that line's level, as the parser will read it
// back, the leading comment of what follows (REQ-format-comments).
// The comments end the line whatever is being written in line
// around them.
func (p *printer) endComments(cs []comment) {
	inline := p.inline
	p.inline = false
	defer func() { p.inline = inline }()
	if len(cs) > 0 {
		if cs[0].ws != "" {
			p.space()
		}
		p.comment(cs[0].text)
	}
	p.line("")
	if len(cs) > 1 {
		p.pending = append(p.pending, cs[1:]...)
	}
}

// comment writes one comment's text: a `//` comment or a one-line
// block comment trimmed; a block comment spanning lines re-indented
// (REQ-format-block-comment): the least-indented interior line's
// indentation dropped from each, every interior line then set three
// spaces in — or one, keeping a prefix character every interior line
// shares — and the closing `*/` on its own line set one space in
// where the prefix is `*`.
func (p *printer) comment(text string) {
	if !strings.HasPrefix(text, "/*") || newlines(text) == 0 {
		p.indent(nil)
		p.text(strings.TrimSpace(text))
		return
	}
	lines := strings.Split(text, "\n")
	least := -1
	prefix := ""
	for j := 1; j < len(lines); j++ {
		if n, ok := indentation(lines[j]); ok && (least == -1 || n < least) {
			least = n
		}
		if j > 1 && prefix == "" {
			continue
		}
		l := strings.TrimSpace(lines[j])
		if l == "*/" {
			continue
		}
		var lp string
		if l != "" && !unicode.IsLetter(rune(l[0])) && !unicode.IsNumber(rune(l[0])) {
			lp = l[:1]
		}
		if j == 1 {
			prefix = lp
		} else if lp != prefix {
			prefix = ""
		}
	}
	if least < 0 {
		least = 0
	}
	for j, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || trimmed == "*/" || prefix != "" {
			l = trimmed
		} else {
			l = strings.TrimRightFunc(unindent(l, least), unicode.IsSpace)
		}
		if j > 0 && l != "*/" {
			if prefix == "" {
				l = "   " + l
			} else {
				l = " " + l
			}
		}
		if l == "*/" && prefix == "*" {
			l = " " + l
		}
		if j != len(lines)-1 {
			p.line(l)
		} else {
			p.indent(nil)
			p.text(l)
		}
	}
}

// indentation is a line's leading whitespace in columns, a tab
// reaching the next multiple of eight; false for a blank line or a
// bare `*/`.
func indentation(s string) (int, bool) {
	if strings.TrimSpace(s) == "*/" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 8 - n%8
		default:
			return n, true
		}
	}
	return 0, false
}

// unindent drops n columns of leading whitespace from a line, a tab
// crossing the mark replaced by the spaces beyond it.
func unindent(s string, n int) string {
	col := 0
	for k, r := range s {
		if col == n {
			return s[k:]
		}
		if col > n {
			return strings.Repeat(" ", col-n) + s[k:]
		}
		switch r {
		case ' ':
			col++
		case '\t':
			col += 8 - col%8
		default:
			return s[k:]
		}
	}
	return ""
}
