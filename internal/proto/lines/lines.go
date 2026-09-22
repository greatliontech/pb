// Package lines reads a proto source's lines by their lexical
// structure — string literals, block comments, line comments — once,
// for every reader that judges a line by what it holds: the checker,
// reading the comment block leading a flagged line (check-rules.md
// REQ-lint-suppression), and the migration, rewriting buf's directives
// and saying which the checker will not read where they stand
// (migrate.md REQ-migrate-comments). The migration's claim is about
// the checker's reading, so one scan serves both and the two cannot
// disagree.
package lines

import "bytes"

// Line is one line of the source as the scan reads it: its bytes'
// span, the terminator and a carriage return before it aside; whether
// it holds code — anything outside a comment and whitespace, which is
// a space, a tab, a carriage return, a vertical tab or a form feed,
// as the compiler skips them — and where the first such byte stands; whether it holds nothing at all,
// a line inside a block comment spanning lines being the comment's
// and never blank; where a `//` comment opens on it, past string
// literals and block comments; and the block comment segments on it,
// each the text between its markers or the line's bounds. A string
// literal never spans a line — the compiler refuses one that does —
// so a quote resets at the line's end; a block comment runs across
// lines to its close.
type Line struct {
	Start, End int
	Code       bool
	First      int // offset of the first byte outside comments and whitespace; -1 where none
	Blank      bool
	Comment    int // offset of the `//`; -1 where none
	Blocks     []Span
}

// Span is a half-open byte range of the source.
type Span struct{ Start, End int }

// Any reports whether the line holds any comment: a `//` comment or a
// block comment's segment, a line inside a block comment spanning
// lines included.
func (l Line) Any() bool { return l.Comment >= 0 || len(l.Blocks) > 0 }

// Scan reads the source into its lines: one per `\n`, plus the bytes
// after the last where any stand; an empty source has none.
func Scan(src []byte) []Line {
	var lines []Line
	inBlock := false
	for start := 0; start < len(src); {
		end := bytes.IndexByte(src[start:], '\n')
		next := len(src)
		if end < 0 {
			end = len(src)
		} else {
			end += start
			next = end + 1
		}
		if end > start && src[end-1] == '\r' {
			end--
		}
		l := Line{Start: start, End: end, First: -1, Comment: -1}
		// A block comment open on this line — spanning from before it,
		// or opened on it — records its segment here even where no
		// byte follows: the line is the comment's, its segment the
		// empty one.
		pending := inBlock
		i := start
		for i < end || pending {
			if inBlock {
				pending = false
				seg := Span{Start: i, End: end}
				if close := bytes.Index(src[i:end], []byte("*/")); close >= 0 {
					seg.End = i + close
					inBlock = false
				}
				l.Blocks = append(l.Blocks, seg)
				if inBlock {
					i = end
				} else {
					i = seg.End + 2
				}
				continue
			}
			c := src[i]
			switch {
			case c == ' ' || c == '\t' || c == '\r' || c == '\v' || c == '\f':
				i++
			case c == '"' || c == '\'':
				l.mark(i)
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
				inBlock, pending = true, true
				i += 2
			case c == '/' && i+1 < end && src[i+1] == '/':
				l.Comment = i
				i = end
			default:
				l.mark(i)
				i++
			}
		}
		l.Blank = !l.Code && !l.Any()
		lines = append(lines, l)
		start = next
	}
	return lines
}

// mark records a byte of code, the first of the line where none stood
// before it.
func (l *Line) mark(i int) {
	if !l.Code {
		l.First = i
	}
	l.Code = true
}
