package lsp

import (
	"testing"

	"go.lsp.dev/protocol"
)

// Positions go through the byte offset: pb's line at `\n` and
// code-point column with a tab one point to the offset, the offset
// to the protocol's line at `\n`, `\r\n` or a lone `\r` and the
// column in the encoding's units (REQ-lsp-positions).
func TestPositions(t *testing.T) {
	text := []byte("a\tb\r\nc\rdé\n\x80f𐐀g\n")
	for _, tc := range []struct {
		line, col int
		off       int
		enc       encoding
		want      protocol.Position
	}{
		{1, 1, 0, utf16, protocol.Position{Line: 0, Character: 0}},
		{1, 3, 2, utf16, protocol.Position{Line: 0, Character: 2}}, // the tab one point, one unit
		{1, 9, 4, utf16, protocol.Position{Line: 0, Character: 3}}, // a column past the line's end is its end, the \r part of pb's line; the protocol's position before the pair
		{2, 1, 5, utf16, protocol.Position{Line: 1, Character: 0}}, // pb's line two begins after the \r\n; the protocol's line one too
		{2, 3, 7, utf16, protocol.Position{Line: 2, Character: 0}}, // pb reads "c\rdé" as one line; the lone \r ends a protocol line
		{2, 4, 8, utf8e, protocol.Position{Line: 2, Character: 1}},
		{2, 4, 8, utf32, protocol.Position{Line: 2, Character: 1}},
		{2, 5, 10, utf16, protocol.Position{Line: 2, Character: 2}}, // é: one point and one unit, two bytes
		{2, 5, 10, utf8e, protocol.Position{Line: 2, Character: 3}},
		{3, 3, 13, utf16, protocol.Position{Line: 3, Character: 2}}, // an invalid byte one unit
		{3, 4, 17, utf16, protocol.Position{Line: 3, Character: 4}}, // a supplementary character two utf-16 units
		{3, 4, 17, utf8e, protocol.Position{Line: 3, Character: 6}},
		{3, 4, 17, utf32, protocol.Position{Line: 3, Character: 3}},
		{9, 1, 19, utf16, protocol.Position{Line: 4, Character: 0}}, // a line past the end is the text's end
	} {
		if off := offsetOf(text, tc.line, tc.col); off != tc.off {
			t.Errorf("offsetOf(%d, %d) = %d, want %d", tc.line, tc.col, off, tc.off)
		}
		if got := position(text, tc.off, tc.enc); got != tc.want {
			t.Errorf("position(%d, %v) = %v, want %v", tc.off, tc.enc, got, tc.want)
		}
	}
	if end := tokenEnd([]byte("a.Thing thing"), 0); end != 7 {
		t.Errorf("tokenEnd over a name = %d, want 7", end)
	}
	if end := tokenEnd([]byte(" x"), 0); end != 0 {
		t.Errorf("tokenEnd where no name starts = %d, want 0", end)
	}
	if got := selectEncoding(nil); got != utf16 {
		t.Errorf("the encoding where the client offers nothing: %v", got)
	}
	if got := selectEncoding([]protocol.PositionEncodingKind{protocol.PositionEncodingKindUTF16, protocol.PositionEncodingKindUTF32}); got != utf32 {
		t.Errorf("the encoding where the client offers utf-32: %v", got)
	}
}
