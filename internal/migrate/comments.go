package migrate

import "bytes"

// Rewrite is a proto file's suppression comments rewritten
// (REQ-migrate-comments): the text after; the count of directives
// rewritten to pb's form; the lines (1-based) of rewritten directives
// pb does not read — any but the last of its block, a block inside a
// declaration continued from the line before or leading a statement
// declaring no entity but the file's, or a file rule's anywhere but
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

// line is one line of the file as the scan reads it: its bytes' span
// (the terminator aside), whether it holds code — anything outside a
// comment or blank — where a `//` comment opens on it, whether a
// block comment segment on it opens with a lint directive, and
// whether it holds any comment at all.
type line struct {
	start, end int
	code       bool
	comment    int // offset of `//`, or -1
	block      bool
	any        bool
}

// RewriteComments rewrites a proto file's suppression comments as buf
// read them (REQ-migrate-comments). buf honors a lint directive on
// any line of the comment block leading an element — the comment-only
// lines directly above a line of code — so each `//` directive on
// such a line is rewritten to pb's form, `buf:lint:ignore` becoming
// `pb:ignore`, the whitespace, id and any trailing text kept and the
// file otherwise byte-for-byte as it was; a directive there but not
// on the block's last line, the one pb reads, or in a block inside a
// continued declaration, is reported displaced, one naming a package
// or set rule reported unplaced, and one in a block comment reported
// as such and left. A directive buf never honored — trailing code on
// its line, in a block no code follows, breaking's, or parted from
// its id by anything but one space — is left as it was and counted.
// The scan
// follows the language's lexical structure: a `//` inside a string
// literal or a block comment opens no line comment.
func RewriteComments(src []byte) Rewrite {
	lines := scanLines(src)
	firstCode := -1
	for k, l := range lines {
		if l.code {
			firstCode = k
			break
		}
	}
	var out bytes.Buffer
	out.Grow(len(src))
	var r Rewrite
	prev := 0
	i := 0
	for i < len(lines) {
		l := lines[i]
		if l.code || !l.any {
			// A line of code, or a blank one: a `//` directive trailing
			// code is inert.
			if l.comment >= 0 {
				if _, _, ok := directive(src[l.comment:l.end], lintForm); ok {
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
		for j < len(lines) && !lines[j].code && lines[j].any {
			j++
		}
		leading := j < len(lines) && lines[j].code
		// A block inside a declaration continued from the line before
		// — the code line above the run ending with no `;`, `{` or `}`
		// — sits below the declaration's first line, where the finding
		// is; so does one leading a statement that declares no entity,
		// an option, reserved or extensions: every directive of it is
		// displaced. A file rule's finding sits at the file's first
		// lexical element, so its directive is placed by the block
		// leading the file's first line of code alone.
		continued := leading && i > 0 && lines[i-1].code && !endsStatement(src, lines[i-1])
		nothing := leading && declaresNothing(src, lines[j])
		first := leading && j == firstCode
		for k := i; k < j; k++ {
			l := lines[k]
			r.Inert += breakingDirectives(src, l)
			if l.block {
				if leading {
					r.Block = append(r.Block, k+1)
				} else {
					r.Inert++
				}
			}
			if l.comment < 0 {
				continue
			}
			lead, id, ok := directive(src[l.comment:l.end], lintForm)
			if !ok {
				continue
			}
			if !leading || id == "" {
				r.Inert++
				continue
			}
			r.Rewritten++
			displaced := k != j-1 || continued
			switch {
			case fileRule(id):
				displaced = displaced || !first
			case positionless(id):
				// Reported unplaced: no line carries its finding.
			default:
				displaced = displaced || nothing
			}
			if displaced {
				r.Displaced = append(r.Displaced, k+1)
			}
			if positionless(id) {
				r.Unplaced = append(r.Unplaced, k+1)
			}
			at := l.comment + 2 + lead
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
// finding sits at: an option, a reserved range or name, an extensions
// range, an import, the package, the syntax or edition. The keyword
// ends the line or is followed by whitespace or a parenthesis, as
// protoc tokenizes it.
func declaresNothing(src []byte, l line) bool {
	code := bytes.TrimLeft(src[l.start:l.end], " \t")
	for _, word := range []string{"option", "reserved", "extensions", "import", "package", "syntax", "edition"} {
		if !bytes.HasPrefix(code, []byte(word)) {
			continue
		}
		if len(code) == len(word) {
			return true
		}
		switch code[len(word)] {
		case ' ', '\t', '\r', '(':
			return true
		}
	}
	return false
}

// endsStatement reports whether a line of code ends a statement or
// opens or closes a body — its code, a trailing `//` comment aside,
// ending with `;`, `{` or `}` — so the line after it may open a
// declaration.
func endsStatement(src []byte, l line) bool {
	code := src[l.start:l.end]
	if l.comment >= 0 {
		code = src[l.start:l.comment]
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
func breakingDirectives(src []byte, l line) int {
	if l.comment < 0 {
		return 0
	}
	if _, _, ok := directive(src[l.comment:l.end], breakingForm); ok {
		return 1
	}
	return 0
}

// scanLines reads the file line by line following its lexical
// structure: a string literal runs to its closing quote or the line's
// end, escapes honored; a block comment runs across lines to its
// close; a `//` comment runs to the line's end. A block comment
// segment's text, its markers stripped and trimmed as buf reads a
// leading comment's lines, marks the line where it opens with a lint
// directive.
func scanLines(src []byte) []line {
	var lines []line
	inBlock := false
	for start := 0; start <= len(src); {
		end := bytes.IndexByte(src[start:], '\n')
		if end < 0 {
			end = len(src)
		} else {
			end += start
		}
		if start == len(src) && len(src) > 0 && src[len(src)-1] == '\n' {
			break
		}
		l := line{start: start, end: end, comment: -1}
		i := start
		for i < end {
			if inBlock {
				l.any = true
				seg := end
				if close := bytes.Index(src[i:end], []byte("*/")); close >= 0 {
					seg = i + close
					inBlock = false
				}
				if blockDirective(src[i:seg]) {
					l.block = true
				}
				if inBlock {
					i = end
				} else {
					i = seg + 2
				}
				continue
			}
			c := src[i]
			switch {
			case c == ' ' || c == '\t' || c == '\r':
				i++
			case c == '"' || c == '\'':
				l.code = true
				i++
				for i < end && src[i] != c {
					if src[i] == '\\' && i+1 < end {
						i++
					}
					i++
				}
				if i < end {
					i++
				}
			case c == '/' && i+1 < end && src[i+1] == '*':
				l.any = true
				inBlock = true
				i += 2
			case c == '/' && i+1 < end && src[i+1] == '/':
				l.any = true
				l.comment = i
				i = end
			default:
				l.code = true
				i++
			}
		}
		lines = append(lines, l)
		if end == len(src) {
			break
		}
		start = end + 1
	}
	return lines
}

// blockDirective reports whether a block comment segment, trimmed as
// buf trims a leading comment's line, opens with a lint directive.
func blockDirective(seg []byte) bool {
	text := bytes.Trim(seg, " \t\r")
	return bytes.HasPrefix(text, []byte(lintForm+" ")) || bytes.HasPrefix(text, []byte(lintForm+"\t"))
}
