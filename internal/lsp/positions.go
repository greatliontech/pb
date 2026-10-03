package lsp

import (
	"bytes"
	"unicode/utf8"

	"github.com/greatliontech/lsp/protocol"
)

// encoding is the position encoding selected for the connection
// (lsp.md, the position encoding term): the unit a column is counted
// in on the wire.
type encoding int

const (
	utf16 encoding = iota // the protocol's default
	utf8e
	utf32
)

// kind is the encoding as the protocol names it.
func (e encoding) kind() protocol.PositionEncodingKind {
	switch e {
	case utf8e:
		return protocol.PositionEncodingKindUTF8
	case utf32:
		return protocol.PositionEncodingKindUTF32
	}
	return protocol.PositionEncodingKindUTF16
}

// selectEncoding picks the encoding from what the client offers, in
// the term's order: utf-8, utf-32, utf-16, which is also the choice
// where the client offers nothing.
func selectEncoding(offered []protocol.PositionEncodingKind) encoding {
	has := func(k protocol.PositionEncodingKind) bool {
		for _, o := range offered {
			if o == k {
				return true
			}
		}
		return false
	}
	switch {
	case has(protocol.PositionEncodingKindUTF8):
		return utf8e
	case has(protocol.PositionEncodingKindUTF32):
		return utf32
	}
	return utf16
}

// offsetOf is the byte offset in text of pb's position: a one-based
// line, lines ending at `\n` as pb counts them, and a one-based
// column in code points, a tab one point (check-rules.md
// REQ-rules-finding-location). A line past the text's end is the
// text's end; a column past the line's end is the line's end.
func offsetOf(text []byte, line, col int) int {
	off := 0
	for l := 1; l < line; l++ {
		i := bytes.IndexByte(text[off:], '\n')
		if i < 0 {
			return len(text)
		}
		off += i + 1
	}
	end := len(text)
	if i := bytes.IndexByte(text[off:], '\n'); i >= 0 {
		end = off + i
	}
	for points := 1; points < col && off < end; points++ {
		_, n := utf8.DecodeRune(text[off:end])
		off += n
	}
	return off
}

// position is the protocol's position of a byte offset in text
// (lsp.md REQ-lsp-positions): a zero-based line, lines ending at
// `\n`, `\r\n` or a lone `\r` as the protocol counts them, and the
// column from the line's start in the encoding's units. An offset
// past the text is the text's end; one inside a multi-byte sequence
// is that sequence's start.
func position(text []byte, off int, enc encoding) protocol.Position {
	if off > len(text) {
		off = len(text)
	}
	line, start := 0, 0
	for i := 0; i < off; i++ {
		switch text[i] {
		case '\n':
			line, start = line+1, i+1
		case '\r':
			if i+1 < len(text) && text[i+1] == '\n' {
				continue // the pair ends the line at the \n
			}
			line, start = line+1, i+1
		}
	}
	// The pair's \n at the offset itself: the position is the line's
	// end, before the pair.
	end := off
	if off > 0 && off < len(text) && text[off] == '\n' && text[off-1] == '\r' {
		end = off - 1
	}
	count := uint32(0)
	for i := start; i < end; {
		r, n := utf8.DecodeRune(text[i:])
		if i+n > end {
			break
		}
		count += units(r, n, enc)
		i += n
	}
	return protocol.Position{Line: uint32(line), Character: count}
}

// units is the width of one character — the rune and its byte
// length — in the encoding's column units: its bytes under utf-8,
// one under utf-32, one or two under utf-16.
func units(r rune, n int, enc encoding) uint32 {
	switch enc {
	case utf8e:
		return uint32(n)
	case utf32:
		return 1
	}
	if r >= 0x10000 {
		return 2
	}
	return 1
}

// tokenEnd is the offset past the identifier starting at off — a run
// of letters, digits, underscores and dots, as a protobuf name is
// spelled — or off where no identifier starts there, so a range
// names the token where the server knows it and the position alone
// where it does not.
func tokenEnd(text []byte, off int) int {
	end := off
	for end < len(text) {
		c := text[end]
		if c == '_' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			end++
			continue
		}
		break
	}
	return end
}

// rangeAt is the protocol range from one byte offset to another in
// text under the encoding.
func rangeAt(text []byte, start, end int, enc encoding) protocol.Range {
	if end < start {
		end = start
	}
	return protocol.Range{Start: position(text, start, enc), End: position(text, end, enc)}
}

// offsetAt is the byte offset in text of a protocol position: the
// line counted at `\n`, `\r\n` or a lone `\r`, the column in the
// encoding's units from the line's start, clipped to the line's end;
// a line past the text is the text's end (REQ-lsp-positions).
func offsetAt(text []byte, pos protocol.Position, enc encoding) int {
	i := 0
	for line := uint32(0); line < pos.Line && i < len(text); {
		switch {
		case text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n':
			i += 2
			line++
		case text[i] == '\n' || text[i] == '\r':
			i++
			line++
		default:
			i++
		}
	}
	count := uint32(0)
	for i < len(text) && count < pos.Character {
		if text[i] == '\n' || text[i] == '\r' {
			break
		}
		r, n := utf8.DecodeRune(text[i:])
		count += units(r, n, enc)
		i += n
	}
	return i
}
