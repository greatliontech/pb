package migrate

import (
	"bytes"

	"github.com/greatliontech/pb/internal/proto/lines"
)

// Rewrite is a proto file's suppression comments rewritten
// (REQ-migrate-comments): the text after; the count of directives
// rewritten to pb's form; the lines (1-based) of rewritten directives
// pb does not read — a block inside a declaration continued from the
// line before, leading a statement declaring no entity but the
// file's or a body's closing brace, or a file rule's anywhere but
// the block leading the file's first line of code; the lines of
// directives in block comments, which buf honored and pb reads not,
// left as they were; the lines of rewritten directives naming a rule
// whose finding carries no position, a package or set rule, which no
// comment suppresses; and the count of directives left as they were
// because buf honored none — trailing a line of code, parted from any
// element by a blank line or the file's end, breaking's, or spelled
// with anything but one space before the id.
type Rewrite struct {
	Text      []byte
	Rewritten int
	Displaced []int
	Block     []int
	Unplaced  []int
	Inert     int
}

// The forms buf spells a directive in, each followed by whitespace
// and an id; buf honors the lint form alone.
const (
	lintForm     = "buf:lint:ignore"
	breakingForm = "buf:breaking:ignore"
	pbForm       = "pb:ignore"
)

// blockDirective reports whether a line's block comment segments,
// each trimmed as buf trims a leading comment's line, hold one that
// opens with a lint directive.
func blockDirective(src []byte, l lines.Line) bool {
	for _, seg := range l.Blocks {
		text := bytes.Trim(src[seg.Start:seg.End], " \t\r")
		if bytes.HasPrefix(text, []byte(lintForm+" ")) || bytes.HasPrefix(text, []byte(lintForm+"\t")) {
			return true
		}
	}
	return false
}

// RewriteComments rewrites a proto file's suppression comments as buf
// read them (REQ-migrate-comments). buf honors a lint directive on
// any line of the comment block leading an element — the comment-only
// lines directly above a line of code — so each `//` directive on
// such a line is rewritten to pb's form, `buf:lint:ignore` becoming
// `pb:ignore`, the whitespace, id and any trailing text kept and the
// file otherwise byte-for-byte as it was; a directive in a block
// inside a continued declaration, where pb reads the block leading
// the declaration's first line, is reported displaced, one naming a package
// or set rule reported unplaced, and one in a block comment reported
// as such and left. A directive buf never honored — trailing code on
// its line, in a block no code follows, breaking's, or parted from
// its id by anything but one space — is left as it was and counted.
// The scan is the checker's own (internal/proto/lines): a `//`
// inside a string literal or a block comment opens no line comment,
// and what the migration reports as placed is what the checker
// reads.
func RewriteComments(src []byte) Rewrite {
	scanned := lines.Scan(src)
	firstCode := -1
	for k, l := range scanned {
		if l.Code {
			firstCode = k
			break
		}
	}
	var out bytes.Buffer
	out.Grow(len(src))
	var r Rewrite
	prev := 0
	i := 0
	for i < len(scanned) {
		l := scanned[i]
		if l.Code || !l.Any() {
			// A line of code, or a blank one: a `//` directive trailing
			// code is inert.
			if l.Comment >= 0 {
				if _, _, ok := directive(src[l.Comment:l.End], lintForm); ok {
					r.Inert++
				}
			}
			r.Inert += breakingDirectives(src, l)
			i++
			continue
		}
		// A run of comment-only lines: a leading block where a line of
		// code follows it directly.
		j := i
		for j < len(scanned) && !scanned[j].Code && scanned[j].Any() {
			j++
		}
		leading := j < len(scanned) && scanned[j].Code
		// A block inside a declaration continued from the line before
		// — the code line above the run ending with no `;`, `{` or `}`
		// — sits below the declaration's first line, where the finding
		// is; so does one leading a statement that declares no entity,
		// an option, reserved or extensions: every directive of it is
		// displaced. A file rule's finding sits at the file's first
		// lexical element, so its directive is placed by the block
		// leading the file's first line of code alone.
		continued := leading && i > 0 && scanned[i-1].Code && !endsStatement(src, scanned[i-1])
		nothing := leading && declaresNothing(src, scanned[j])
		first := leading && j == firstCode
		for k := i; k < j; k++ {
			l := scanned[k]
			r.Inert += breakingDirectives(src, l)
			if blockDirective(src, l) {
				if leading {
					r.Block = append(r.Block, k+1)
				} else {
					r.Inert++
				}
			}
			if l.Comment < 0 {
				continue
			}
			lead, id, ok := directive(src[l.Comment:l.End], lintForm)
			if !ok {
				continue
			}
			if !leading || id == "" {
				r.Inert++
				continue
			}
			r.Rewritten++
			displaced := continued
			switch {
			case positionless(id):
				// Unplaced, whatever the line: no line carries its
				// finding, so its place is no matter.
				r.Unplaced = append(r.Unplaced, k+1)
				displaced = false
			case fileRule(id):
				displaced = displaced || !first
			default:
				displaced = displaced || nothing
			}
			if displaced {
				r.Displaced = append(r.Displaced, k+1)
			}
			at := l.Comment + 2 + lead
			out.Write(src[prev:at])
			out.WriteString(pbForm)
			prev = at + len(lintForm)
		}
		i = j
	}
	out.Write(src[prev:])
	r.Text = out.Bytes()
	return r
}

// directive reports whether a `//` comment's text, its two slashes
// included, opens with the form, whitespace and an id: the count of
// spaces and tabs between the slashes and the form, which buf trims,
// and the id where the form and the id are parted by exactly one
// space, as buf reads it — the id empty where the parting is any
// other whitespace, a directive buf never honored.
func directive(comment []byte, form string) (lead int, id string, ok bool) {
	text := comment[2:]
	for lead < len(text) && (text[lead] == ' ' || text[lead] == '\t') {
		lead++
	}
	rest := text[lead:]
	if !bytes.HasPrefix(rest, []byte(form)) || len(rest) == len(form) {
		return 0, "", false
	}
	if c := rest[len(form)]; c != ' ' && c != '\t' {
		return 0, "", false
	}
	fields := bytes.Fields(rest[len(form):])
	if len(fields) == 0 {
		return 0, "", false
	}
	if rest[len(form)] != ' ' || rest[len(form)+1] == ' ' || rest[len(form)+1] == '\t' {
		return lead, "", true
	}
	return lead, string(fields[0]), true
}

// declaresNothing reports whether a line of code opens a statement
// that declares no entity but the file's, which a file rule's
// finding sits at — an option, a reserved range or name, an
// extensions range, an import, the package, the syntax or edition,
// the keyword ending the line or followed by whitespace, a
// parenthesis or a comment, as protoc tokenizes it — or closes a
// body, on whose line no finding ever sits. The line's first token
// is read past its comments, so a statement behind a block comment
// on its line is read as the statement it is.
func declaresNothing(src []byte, l lines.Line) bool {
	if l.First < 0 {
		return false
	}
	code := src[l.First:l.End]
	if bytes.HasPrefix(code, []byte("}")) {
		return true
	}
	for _, word := range []string{"option", "reserved", "extensions", "import", "package", "syntax", "edition"} {
		if !bytes.HasPrefix(code, []byte(word)) {
			continue
		}
		if len(code) == len(word) {
			return true
		}
		switch code[len(word)] {
		case ' ', '\t', '(', '/':
			return true
		}
	}
	return false
}

// endsStatement reports whether a line of code ends a statement or
// opens or closes a body — its code, a trailing `//` comment aside,
// ending with `;`, `{` or `}` — so the line after it may open a
// declaration.
func endsStatement(src []byte, l lines.Line) bool {
	code := src[l.Start:l.End]
	if l.Comment >= 0 {
		code = src[l.Start:l.Comment]
	}
	code = bytes.TrimRight(code, " \t\r")
	if len(code) == 0 {
		return true
	}
	switch code[len(code)-1] {
	case ';', '{', '}':
		return true
	}
	return false
}

// breakingDirectives counts a line's breaking-form directives in a
// `//` comment, which buf never honored.
func breakingDirectives(src []byte, l lines.Line) int {
	if l.Comment < 0 {
		return 0
	}
	if _, _, ok := directive(src[l.Comment:l.End], breakingForm); ok {
		return 1
	}
	return 0
}
