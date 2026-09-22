package lines

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// The reading both the checker and the migration take: a `//` comment
// opens past string literals and block comments, a block comment
// spanning lines keeps its lines the comment's and never blank, a
// string resets at the line's end, a carriage return before the
// terminator is no part of the line, and the first byte of code is
// past whatever comments precede it.
func TestScanGolden(t *testing.T) {
	type want struct {
		code, blank bool
		comment     string // text after //, or "-"
		first       string // text from the first byte of code, or "-"
		blocks      []string
	}
	for src, w := range map[string]want{
		`string a = 1; // c`:                  {true, false, " c", "string a = 1; // c", nil},
		`string a = 1 [(o) = "// no"];`:       {true, false, "-", `string a = 1 [(o) = "// no"];`, nil},
		`string a = 1 [(o) = 'it\'s // no'];`: {true, false, "-", `string a = 1 [(o) = 'it\'s // no'];`, nil},
		`x = "esc\" // no"; //yes`:            {true, false, "yes", `x = "esc\" // no"; //yes`, nil},
		`// c`:                                {false, false, " c", "-", nil},
		`/* a note */`:                        {false, false, "-", "-", []string{" a note "}},
		`/* a */ // yes`:                      {false, false, " yes", "-", []string{" a "}},
		`/* // pb:ignore X */ string a = 1;`:  {true, false, "-", "string a = 1;", []string{" // pb:ignore X "}},
		`/* x */ }`:                           {true, false, "-", "}", []string{" x "}},
		`option/* c */allow_alias = true;`:    {true, false, "-", "option/* c */allow_alias = true;", []string{" c "}},
		`a / b // c`:                          {true, false, " c", "a / b // c", nil},
		`  `:                                  {false, true, "-", "-", nil},
		"\t\r":                                {false, true, "-", "-", nil},
		" \r ":                                {false, true, "-", "-", nil},
		"\f\v":                                {false, true, "-", "-", nil},
		`/*`:                                  {false, false, "-", "-", []string{""}},
		`x /*`:                                {true, false, "-", "x /*", []string{""}},
		`"/* no */"`:                          {true, false, "-", `"/* no */"`, nil},
		`/* a */"str"`:                        {true, false, "-", `"str"`, []string{" a "}},
		`}`:                                   {true, false, "-", "}", nil},
		`"s"`:                                 {true, false, "-", `"s"`, nil},
		`"unterminated // no`:                 {true, false, "-", `"unterminated // no`, nil},
		`/* one */ x /* two */ // three`:      {true, false, " three", "x /* two */ // three", []string{" one ", " two "}},
	} {
		ls := Scan([]byte(src))
		if len(ls) != 1 {
			t.Errorf("%q: %d lines", src, len(ls))
			continue
		}
		l := ls[0]
		comment, first := "-", "-"
		if l.Comment >= 0 {
			comment = src[l.Comment+2 : l.End]
		}
		if l.First >= 0 {
			first = src[l.First:l.End]
		}
		var blocks []string
		for _, b := range l.Blocks {
			blocks = append(blocks, src[b.Start:b.End])
		}
		if l.Code != w.code || l.Blank != w.blank || comment != w.comment || first != w.first || strings.Join(blocks, "|") != strings.Join(w.blocks, "|") {
			t.Errorf("%q: code=%v blank=%v comment=%q first=%q blocks=%q", src, l.Code, l.Blank, comment, first, blocks)
		}
	}
	// An empty source has no lines; a source ending in a newline has
	// one per newline; a trailing fragment is a line of its own.
	if ls := Scan(nil); len(ls) != 0 {
		t.Fatalf("empty: %d lines", len(ls))
	}
	for src, n := range map[string]int{"x\n": 1, "x\n\n": 2, "x": 1, "x\ny": 2, "\n": 1, "x\r\ny\r\n": 2} {
		if ls := Scan([]byte(src)); len(ls) != n {
			t.Errorf("%q: %d lines, want %d", src, len(ls), n)
		}
	}
	// A block comment spanning lines: its inner lines are the comment's
	// — never blank, holding the segment, a // inside no comment — and
	// the line closing it holds the code after the close.
	ls := Scan([]byte("/* opens\n // inside\n\n */ string a = 1; // after\n// pb:ignore X\n"))
	if len(ls) != 5 {
		t.Fatalf("%d lines", len(ls))
	}
	for i, l := range ls[:3] {
		if l.Code || l.Blank || l.Comment >= 0 || len(l.Blocks) != 1 {
			t.Errorf("line %d inside the block: %+v", i+1, l)
		}
	}
	if l := ls[3]; !l.Code || l.Comment < 0 || l.First != len("/* opens\n // inside\n\n */ ") {
		t.Errorf("the closing line: %+v", l)
	}
	if l := ls[4]; l.Code || l.Comment < 0 {
		t.Errorf("the line after: %+v", l)
	}
	// A string resets at the line's end: an unbalanced quote never
	// swallows the next line.
	ls = Scan([]byte("a = \"x // no;\r\n// yes\r\n"))
	if l := ls[1]; l.Comment < 0 || l.Code {
		t.Errorf("after an unbalanced quote: %+v", l)
	}
	// A block comment opening at a line's end and closing at the
	// next's start: both lines are the comment's, neither blank.
	ls = Scan([]byte("/*\n*/\n"))
	for i, l := range ls {
		if l.Code || l.Blank || !l.Any() || len(l.Blocks) != 1 {
			t.Errorf("line %d of a two-line block: %+v", i+1, l)
		}
	}
}

// For any source assembled from the lexical pieces — code words,
// spaces, tabs, quotes with escapes, comment markers, both line
// terminators — the lines partition the source between terminators,
// and each line's reading is consistent: blank means no code and no
// comment, code means a first byte of code that is no whitespace and
// outside every block segment, a comment opens on `//`, and block
// segments lie within the line in order.
func TestScanProperty(t *testing.T) {
	pieces := []string{"x", "option", "}", " ", "\t", "\r", "\f", "\"", "'", "\\", "/*", "*/", "//", "/", "*", "\n", "\r\n", ";"}
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(0, 40).Draw(rt, "n")
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(rapid.SampledFrom(pieces).Draw(rt, "piece"))
		}
		src := b.String()
		ls := Scan([]byte(src))
		// Partition: one line per newline, plus a trailing fragment.
		wantN := strings.Count(src, "\n")
		if !strings.HasSuffix(src, "\n") && len(src) > 0 {
			wantN++
		}
		if len(ls) != wantN {
			rt.Fatalf("%q: %d lines, want %d", src, len(ls), wantN)
		}
		pos := 0
		for i, l := range ls {
			if l.Start != pos {
				rt.Fatalf("%q: line %d starts at %d, want %d", src, i+1, l.Start, pos)
			}
			text := src[l.Start:l.End]
			if strings.ContainsAny(text, "\n") {
				rt.Fatalf("%q: line %d holds a terminator: %q", src, i+1, text)
			}
			// The terminator: a \n, with a \r before it, follows End.
			next := l.End
			if next < len(src) && src[next] == '\r' {
				next++
			}
			if next < len(src) {
				if src[next] != '\n' {
					rt.Fatalf("%q: line %d ends at %d before %q", src, i+1, l.End, src[next])
				}
				next++
			}
			pos = next
			if l.Blank != (!l.Code && !l.Any()) || l.Blank && strings.Trim(text, " \t\r\v\f") != "" {
				rt.Fatalf("%q: line %d blank=%v yet %+v", src, i+1, l.Blank, l)
			}
			// Every byte of the line is accounted for by one class:
			// whitespace, a block segment or its markers, the line
			// comment, or code — a string literal's bytes code, its
			// markers no comment.
			for _, s := range l.Blocks {
				if strings.Contains(src[s.Start:s.End], "*/") {
					rt.Fatalf("%q: line %d segment holds a close", src, i+1)
				}
			}
			if l.Comment >= 0 {
				for _, s := range l.Blocks {
					if l.Comment >= s.Start && l.Comment < s.End {
						rt.Fatalf("%q: line %d comment inside a segment", src, i+1)
					}
				}
			}
			if l.Code != (l.First >= 0) {
				rt.Fatalf("%q: line %d code=%v first=%d", src, i+1, l.Code, l.First)
			}
			if l.First >= 0 {
				c := src[l.First]
				if l.First < l.Start || l.First >= l.End || c == ' ' || c == '\t' {
					rt.Fatalf("%q: line %d first=%d", src, i+1, l.First)
				}
				for _, s := range l.Blocks {
					if l.First >= s.Start && l.First < s.End {
						rt.Fatalf("%q: line %d first inside a block segment", src, i+1)
					}
				}
			}
			if l.Comment >= 0 && (l.Comment < l.Start || l.Comment+2 > l.End || src[l.Comment:l.Comment+2] != "//") {
				rt.Fatalf("%q: line %d comment=%d", src, i+1, l.Comment)
			}
			last := l.Start
			for _, s := range l.Blocks {
				if s.Start < last || s.End < s.Start || s.End > l.End {
					rt.Fatalf("%q: line %d segment %+v", src, i+1, s)
				}
				last = s.End
			}
		}
		if pos != len(src) {
			rt.Fatalf("%q: lines end at %d of %d", src, pos, len(src))
		}
	})
}
