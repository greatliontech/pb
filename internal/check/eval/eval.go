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
	protolines "github.com/greatliontech/pb/internal/proto/lines"
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

// BreakingOldSide evaluates breaking rules over the old side's
// declarations alone: the pairs the two sides' checked files align
// less those with no old side — an addition, judged elsewhere — so a
// base whose files no module provides is judged once against
// everything the build holds now, a declaration that moved to another
// file pairing by its name; a set rule, judging a module's two sets,
// sees no pair here, the old side being no module's set, and a
// package rule only a package the build no longer declares, one it
// still declares being a module's to judge over its own files
// (REQ-break-base-materialized).
func BreakingOldSide(env *env1.Env, oldChecked, newChecked []string, source, baseSource Source, rs []rules.Rule) (*check.Report, error) {
	new, old := env.Sides()
	if old == nil {
		return nil, errors.New("breaking evaluation needs an environment over two sides")
	}
	return run(env, source, baseSource, rs, func(t check.Target) ([]env1.Binding, error) {
		pairs, err := env1.Pairs(t, old, new, oldChecked, newChecked)
		if err != nil {
			return nil, err
		}
		kept := pairs[:0:0]
		if t == check.TargetSet {
			return kept, nil
		}
		// A pair with an old declaration, or one whose new side is
		// absent: a package pair binds no declaration, so only a
		// vanished package stays.
		for _, b := range pairs {
			if b.Old != nil || b.Base {
				kept = append(kept, b)
			}
		}
		return kept, nil
	})
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
	shared := sharedIDs(rs)
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
			f := check.Finding{Rule: rule.Name(), Severity: rule.Severity, Message: rule.Message, Path: b.Path, Base: base}
			if desc := b.Located(); desc != nil {
				t, err := textOf(b.Path, base)
				if err != nil {
					return nil, err
				}
				loc := desc.ParentFile().SourceLocations().ByDescriptor(desc)
				// 1-based, so a located finding's line is never zero:
				// a zero line marks a finding without a position — a
				// set rule's, or a package rule's — which the check
				// run relies on to relocate one under a module's own
				// selection (REQ-rules-finding-location).
				f.Line = loc.StartLine + 1
				f.Column = t.column(loc.StartLine, loc.StartColumn)
				if !base {
					suppressed, err := t.suppresses(f.Line, rule, shared)
					if err != nil {
						return nil, err
					}
					if suppressed {
						continue
					}
				}
			}
			r.Findings = append(r.Findings, f)
		}
	}
	return r, nil
}

// text is a checked file's source scanned once by its lexical
// structure (internal/proto/lines): each line's span, the line
// comment it carries, whether it holds code and whether it holds
// nothing at all — the one reading the migration's placing of a
// directive is a claim about.
type text struct {
	src   string
	lines []protolines.Line
}

// newText scans the source.
func newText(data []byte) *text {
	return &text{src: string(data), lines: protolines.Scan(data)}
}

// bytes is a line's text, its terminator aside.
func (t *text) bytes(l protolines.Line) string { return t.src[l.Start:l.End] }

// comment is a line's comment text after the `//`, and whether it has
// one.
func (t *text) comment(l protolines.Line) (string, bool) {
	if l.Comment < 0 {
		return "", false
	}
	return t.src[l.Comment+2 : l.End], true
}

func (t *text) line(n int) (protolines.Line, bool) { // n is 1-based
	if n < 1 || n > len(t.lines) {
		return protolines.Line{}, false
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
	b := t.bytes(l)
	col, points := 0, 0
	for i := 0; i < len(b); i++ {
		if col >= col0 {
			break
		}
		switch {
		case b[i] == '\t':
			col += 8 - col%8
		case utf8.RuneStart(b[i]):
			col++
		default:
			continue
		}
		points++
	}
	return points + 1
}

// suppresses reports whether a comment `// pb:ignore <name>` sits on
// the line, or on a line of the comment block leading it — the lines
// directly above holding nothing outside comments, up to a line of
// code or a line holding nothing at all, a blank line inside a block
// comment being the comment's (REQ-lint-suppression): the comment's
// text opens with the marker, the rule's name or its bare id as the
// next word, anything after being the reason. A trailing comment on
// the line of code above belongs to that line's declaration, and
// ends the block. A bare id several enabled rules share names none
// of them: the run fails naming them.
func (t *text) suppresses(n int, r rules.Rule, shared map[string][]string) (bool, error) {
	for at := n; ; at-- {
		l, ok := t.line(at)
		if !ok {
			break
		}
		if at != n && (l.Code || l.Blank) {
			break
		}
		comment, has := t.comment(l)
		if !has {
			continue
		}
		word, ok := ignoreWord(comment)
		if !ok {
			continue
		}
		if word == r.Name() {
			return true, nil
		}
		if word == r.ID {
			if names := shared[r.ID]; len(names) > 1 {
				return false, fmt.Errorf("line %d: pb:ignore %s names several enabled rules: %s", at, word, strings.Join(names, ", "))
			}
			return true, nil
		}
	}
	return false, nil
}

// The suppression marker.
const marker = "pb:ignore"

// ignoreWord is the rule spelling a suppression comment names, if
// the comment is one.
func ignoreWord(comment string) (string, bool) {
	fields := strings.Fields(comment)
	if len(fields) >= 2 && fields[0] == marker {
		return fields[1], true
	}
	return "", false
}

// sharedIDs indexes the enabled rules' names by bare id, so a bare id
// several rules share is known.
func sharedIDs(rs []rules.Rule) map[string][]string {
	out := map[string][]string{}
	for _, r := range rs {
		out[r.ID] = append(out[r.ID], r.Name())
	}
	return out
}
