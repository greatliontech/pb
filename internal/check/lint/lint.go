// Package lint evaluates lint rules over a checked schema
// (check-rules.md): each rule compiled under environment 1, bound to
// every entity of its target in the checked files, and a false
// verdict turned into one finding at the entity's declaration —
// unless a suppression comment on the flagged line or the line
// before names the rule. Positions are the compiler's source
// locations recounted in code points against the source text, which
// the suppression check reads too, so a checked file's source is
// what the evaluation needs beside the schema.
package lint

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

// Finding is one rule's false verdict (REQ-rules-verdict), located
// per REQ-rules-finding-location: Line and Column 1-based, the column
// in code points; Line zero for a finding without a position (a
// package rule's, at the package's first checked file); Path empty
// for a finding without a location (a set rule's).
type Finding struct {
	RuleID   string
	Severity check.Severity
	Message  string
	Path     string
	Line     int
	Column   int
}

// Report is a run's outcome: the findings in evaluation order — rules
// in the order given, entities in population order — and the number
// of rules enabled, the ones given (REQ-rules-no-defaults: zero rules,
// zero findings).
type Report struct {
	Findings []Finding
	Rules    int
}

// Source reads a checked file's source by its path.
type Source func(path string) ([]byte, error)

// Run evaluates the rules over the checked files, by path, of the set
// under the environment. A rule that fails to
// compile or to evaluate fails the run naming it; a source that
// cannot be read fails the run naming the file.
func Run(env *env1.Env, set *env1.Set, checked []string, source Source, rs []rules.Rule) (*Report, error) {
	r := &Report{Rules: len(rs), Findings: []Finding{}}
	sources := map[string]*text{}
	textOf := func(path string) (*text, error) {
		if t := sources[path]; t != nil {
			return t, nil
		}
		data, err := source(path)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrSource, path, err)
		}
		t := newText(data)
		sources[path] = t
		return t, nil
	}
	for _, rule := range rs {
		prg, err := env.Compile(rule)
		if err != nil {
			return nil, err
		}
		bindings, err := set.Population(rule.Target, checked)
		if err != nil {
			return nil, err
		}
		for _, b := range bindings {
			ok, err := prg.Eval(b.Vars)
			if err != nil {
				return nil, err
			}
			if ok {
				continue
			}
			f := Finding{RuleID: rule.ID, Severity: rule.Severity, Message: rule.Message, Path: b.Path}
			if b.Desc != nil {
				t, err := textOf(b.Path)
				if err != nil {
					return nil, err
				}
				loc := b.Desc.ParentFile().SourceLocations().ByDescriptor(b.Desc)
				f.Line = loc.StartLine + 1
				f.Column = t.column(loc.StartLine, loc.StartColumn)
				if t.suppresses(f.Line, rule.ID) {
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
