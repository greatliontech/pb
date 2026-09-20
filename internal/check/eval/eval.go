// Package eval evaluates rules over a checked schema (check-rules.md):
// each rule compiled under environment 1 and bound to its population
// — a lint rule to every entity of its target in the checked files, a
// breaking rule to every aligned pair — and a false verdict turned
// into one finding at the entity's declaration, unless a suppression
// comment on the flagged line or the line before names the rule.
// Positions are the compiler's source locations recounted in code
// points against the source text, which the suppression check reads
// too, so a checked file's source is what the evaluation needs beside
// the schema; a finding in the comparison base is located in the
// base's text and suppressed by configuration alone.
package eval

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/check/env1"
	"github.com/greatliontech/pb/internal/check/rules"
)

// ErrSource is wrapped when a checked file's source cannot be read.
var ErrSource = errors.New("source unavailable")

// Source reads a checked file's source by its path.
type Source func(path string) ([]byte, error)

// Lint evaluates lint rules over the checked files, by path, of the
// environment's set. A rule that fails to compile or to evaluate
// fails the run naming it; a source that cannot be read fails the
// run naming the file.
func Lint(env *env1.Env, checked []string, source Source, rs []rules.Rule) (*check.Report, error) {
	set, _ := env.Sides()
	return run(env, source, nil, rs, func(t check.Target) ([]env1.Binding, error) { return set.Population(t, checked) })
}

// Breaking evaluates breaking rules over the pairs the two sides'
// checked files align (REQ-break-pairing), the sides the
// environment's: a finding sits at the new side's declaration, or at
// the old side's in the base where the new side is absent — located
// in the base's text, marked, and suppressed by configuration alone.
func Breaking(env *env1.Env, oldChecked, newChecked []string, source, baseSource Source, rs []rules.Rule) (*check.Report, error) {
	new, old := env.Sides()
	if old == nil {
		// A lint environment has no old side; a lint rule among the
		// given would compile under it and pair against nothing.
		return nil, errors.New("breaking evaluation needs an environment over two sides")
	}
	return run(env, source, baseSource, rs, func(t check.Target) ([]env1.Binding, error) { return env1.Pairs(t, old, new, oldChecked, newChecked) })
}

// textKey names a source: its path on one side.
type textKey struct {
	base bool
	path string
}

// run is the evaluation every kind shares: each rule compiled and
// bound to its population — drawn once per target — a false verdict
// a finding located and, off the base, judged for suppression.
func run(env *env1.Env, source, baseSource Source, rs []rules.Rule, population func(check.Target) ([]env1.Binding, error)) (*check.Report, error) {
	r := &check.Report{Rules: len(rs), Findings: []check.Finding{}}
	texts := map[textKey]*text{}
	textOf := func(path string, base bool) (*text, error) {
		k := textKey{base, path}
		if t := texts[k]; t != nil {
			return t, nil
		}
		read := source
		if base {
			read = baseSource
		}
		data, err := read(path)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrSource, path, err)
		}
		t := newText(data)
		texts[k] = t
		return t, nil
	}
	populations := map[check.Target][]env1.Binding{}
	for _, rule := range rs {
		prg, err := env.Compile(rule)
		if err != nil {
			return nil, err
		}
		bindings, drawn := populations[rule.Target]
		if !drawn {
			bindings, err = population(rule.Target)
			if err != nil {
				return nil, err
			}
			populations[rule.Target] = bindings
		}
		for _, b := range bindings {
			ok, err := prg.Eval(b.Vars)
			if err != nil {
				return nil, err
			}
			if ok {
				continue
			}
			base := b.Base
			f := check.Finding{RuleID: rule.ID, Severity: rule.Severity, Message: rule.Message, Path: b.Path, Base: base}
			if desc := b.Located(); desc != nil {
				t, err := textOf(b.Path, base)
				if err != nil {
					return nil, err
				}
				loc := desc.ParentFile().SourceLocations().ByDescriptor(desc)
				f.Line = loc.StartLine + 1
				f.Column = t.column(loc.StartLine, loc.StartColumn)
				if !base && t.suppresses(f.Line, rule.ID) {
					continue
				}
			}
			r.Findings = append(r.Findings, f)
		}
	}
	return r, nil
}

// text is a checked file's source scanned once: each line's bytes,
// and the line comment it carries, if any, found past string literals
// and block comments — a block comment spanning lines is tracked
// across them — with whether the comment stands alone on its line.
type text struct {
	lines []line
}

type line struct {
	bytes   string
	comment string // the text after //, where the line has a line comment
	has     bool
	alone   bool // nothing but whitespace before the //
}

// newText scans the source. Strings never span a line (the compiler
// refuses one that does), so a quote resets at a line end; a block
// comment runs until its close.
func newText(data []byte) *text {
	t := &text{}
	var quote byte
	block := false
	src := string(data)
	for start := 0; start < len(src); {
		end := strings.IndexByte(src[start:], '\n')
		if end < 0 {
			end = len(src)
		} else {
			end += start
		}
		l := line{bytes: strings.TrimSuffix(src[start:end], "\r")}
		quote = 0
		for i := 0; i < len(l.bytes); i++ {
			c := l.bytes[i]
			switch {
			case block:
				if c == '*' && i+1 < len(l.bytes) && l.bytes[i+1] == '/' {
					block = false
					i++
				}
			case quote != 0:
				if c == '\\' {
					i++
				} else if c == quote {
					quote = 0
				}
			case c == '"' || c == '\'':
				quote = c
			case c == '/' && i+1 < len(l.bytes) && l.bytes[i+1] == '*':
				block = true
				i++
			case c == '/' && i+1 < len(l.bytes) && l.bytes[i+1] == '/':
				l.comment, l.has = l.bytes[i+2:], true
				l.alone = strings.TrimSpace(l.bytes[:i]) == ""
				i = len(l.bytes)
			}
		}
		t.lines = append(t.lines, l)
		start = end + 1
	}
	return t
}

func (t *text) line(n int) (line, bool) { // n is 1-based
	if n < 1 || n > len(t.lines) {
		return line{}, false
	}
	return t.lines[n-1], true
}

// column recounts the compiler's column — code points with a tab
// advancing to the next multiple of eight, a code point being each
// byte that starts a UTF-8 sequence — as a 1-based count of code
// points on the line (REQ-rules-finding-location). A line the
// source does not hold, or a column past its end, yields the
// compiler's count plus one.
func (t *text) column(line0, col0 int) int {
	l, ok := t.line(line0 + 1)
	if !ok {
		return col0 + 1
	}
	col, points := 0, 0
	for i := 0; i < len(l.bytes); i++ {
		if col >= col0 {
			break
		}
		switch {
		case l.bytes[i] == '\t':
			col += 8 - col%8
		case utf8.RuneStart(l.bytes[i]):
			col++
		default:
			continue
		}
		points++
	}
	return points + 1
}

// suppresses reports whether a comment `// pb:ignore <id>` sits on
// the line, or alone on the one before it (REQ-lint-suppression): the
// comment's text opens with the marker, the id its next word,
// anything after being the reason. A trailing comment on the line
// before belongs to that line's declaration.
func (t *text) suppresses(n int, id string) bool {
	if l, ok := t.line(n); ok && l.has && ignores(l.comment, id) {
		return true
	}
	if l, ok := t.line(n - 1); ok && l.has && l.alone && ignores(l.comment, id) {
		return true
	}
	return false
}

// The suppression marker.
const marker = "pb:ignore"

func ignores(comment, id string) bool {
	fields := strings.Fields(comment)
	return len(fields) >= 2 && fields[0] == marker && fields[1] == id
}
