package format

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// The unified diff is diff -u's: the two labels, hunks of three
// context lines merged where closer than six equal lines, the ranges
// one-based with a count but for one line, a missing final newline
// noted (REQ-format-verb). Held against the host's diff over hand
// cases with one shortest script, and, over generated pairs, applied
// back to the old text to yield the new.
func TestUnified(t *testing.T) {
	cases := [][2]string{
		{"a\nb\nc\n", "a\nc\nd"},
		{"", "x\n"},
		{"x\n", ""},
		{"a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\nm\n", "a\nB\nc\nd\ne\nf\ng\nh\ni\nj\nk\nL\nm\n"},
		{"a\nb\nc\nd\ne\nf\ng\nh\ni\n", "a\nB\nc\nd\ne\nf\ng\nH\ni\n"},
		{"same\n", "same\n"},
		{"no newline", "no newline\n"},
		{"one\n", "one\ntwo\nthree\n"},
		{"a\nb\nc\nd\ne\nf\ng\nh\n", "a\nb\nc\nd\ne\nf\ng\nh\ni\n"},
		// A run of removals and insertions is printed removals first,
		// however the shortest script interleaves them.
		{"s\np\nmessage   Loose {\n      string name=1;  }\n", "s\np\n\nmessage Loose {\n  string name = 1;\n}\n"},
		// Two changes six equal lines apart share a hunk; seven apart
		// make two.
		{"a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n", "a\nB\nc\nd\ne\nf\ng\nh\nI\nj\n"},
		{"a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\n", "a\nB\nc\nd\ne\nf\ng\nh\ni\nJ\nk\n"},
	}
	if _, err := exec.LookPath("diff"); err == nil {
		dir := t.TempDir()
		for _, c := range cases {
			got, want := Unified("p", []byte(c[0]), []byte(c[1])), hostDiff(t, dir, c[0], c[1])
			if !bytes.Equal(got, want) {
				t.Fatalf("%q -> %q:\n got:\n%s\nwant:\n%s", c[0], c[1], got, want)
			}
		}
	}
	rapid.Check(t, func(rt *rapid.T) {
		gen := rapid.SliceOfN(rapid.SampledFrom([]string{"a", "b", "c", "d", ""}), 0, 24)
		join := func(ls []string, newline bool) string {
			s := ""
			for k, l := range ls {
				s += l
				if k < len(ls)-1 || newline {
					s += "\n"
				}
			}
			return s
		}
		old := join(gen.Draw(rt, "old"), rapid.Bool().Draw(rt, "old newline"))
		new := join(gen.Draw(rt, "new"), rapid.Bool().Draw(rt, "new newline"))
		d := Unified("p", []byte(old), []byte(new))
		if (len(d) == 0) != (old == new) {
			rt.Fatalf("%q -> %q: diff %q", old, new, d)
		}
		if got := apply(rt, old, d); got != new {
			rt.Fatalf("%q -> %q: the diff applied gives %q:\n%s", old, new, got, d)
		}
	})
}

// apply replays a unified diff over the old text: each hunk's
// context and removed lines must stand in old at the hunk's range,
// the result the kept and inserted lines.
func apply(t *rapid.T, old string, d []byte) string {
	if len(d) == 0 {
		return old
	}
	oldLines := lines([]byte(old))
	var out strings.Builder
	at := 0 // lines of old consumed
	text := strings.Split(strings.TrimSuffix(string(d), "\n"), "\n")
	if len(text) < 2 || text[0] != "--- p" || text[1] != "+++ p" {
		t.Fatalf("header: %q", d)
	}
	emit := func(l line) {
		out.WriteString(l.text)
		if l.newline {
			out.WriteString("\n")
		}
	}
	for k := 2; k < len(text); {
		h := text[k]
		var os, oc, ns, nc int
		if n, err := fmt.Sscanf(h, "@@ -%d,%d +%d,%d @@", &os, &oc, &ns, &nc); n != 4 || err != nil {
			// A one-line side spells no count.
			var fields []string
			if _, err := fmt.Sscanf(h, "@@ %s %s @@", new(string), new(string)); err != nil {
				t.Fatalf("hunk header %q", h)
			}
			fields = strings.Fields(strings.Trim(h, "@ "))
			os, oc = sideRange(t, fields[0][1:])
			ns, nc = sideRange(t, fields[1][1:])
		}
		_, _ = ns, nc
		start := os - 1
		if oc == 0 {
			start = os
		}
		for at < start {
			emit(oldLines[at])
			at++
		}
		k++
		for ; k < len(text) && !strings.HasPrefix(text[k], "@@"); k++ {
			l := text[k]
			newline := true
			if k+1 < len(text) && text[k+1] == `\ No newline at end of file` {
				newline = false
			}
			switch {
			case l == `\ No newline at end of file`:
				continue
			case strings.HasPrefix(l, " "):
				if at >= len(oldLines) || oldLines[at] != (line{l[1:], newline}) {
					t.Fatalf("context %q does not stand in old at line %d", l, at+1)
				}
				emit(oldLines[at])
				at++
			case strings.HasPrefix(l, "-"):
				if at >= len(oldLines) || oldLines[at] != (line{l[1:], newline}) {
					t.Fatalf("removed %q does not stand in old at line %d", l, at+1)
				}
				at++
			case strings.HasPrefix(l, "+"):
				emit(line{l[1:], newline})
			default:
				t.Fatalf("line %q", l)
			}
		}
	}
	for ; at < len(oldLines); at++ {
		emit(oldLines[at])
	}
	return out.String()
}

// sideRange reads `start,count` or `start`.
func sideRange(t *rapid.T, s string) (int, int) {
	start, count := 0, 1
	if _, err := fmt.Sscanf(s, "%d,%d", &start, &count); err != nil {
		if _, err := fmt.Sscanf(s, "%d", &start); err != nil {
			t.Fatalf("range %q", s)
		}
	}
	return start, count
}

// hostDiff runs the host's diff -u over the two texts under one
// label, its output empty where they are equal.
func hostDiff(t *testing.T, dir, old, new string) []byte {
	t.Helper()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(new), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("diff", "-u", "--label", "p", "--label", "p", a, b).Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 1 {
			t.Fatal(err)
		}
	}
	return out
}
