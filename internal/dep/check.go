package dep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	"github.com/go-git/go-billy/v6/helper/iofs"
	"github.com/go-git/go-git/v6"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/check/breaking"
	"github.com/greatliontech/pb/internal/check/env1"
	"github.com/greatliontech/pb/internal/check/eval"
	"github.com/greatliontech/pb/internal/check/lintfile"
	"github.com/greatliontech/pb/internal/check/rules"
	"github.com/greatliontech/pb/internal/proto/compile"
	"github.com/greatliontech/pb/internal/proto/modfiles"
)

// ErrFindings is the check verbs' failing exit: findings of severity
// error remain after suppression (REQ-check-exit-status). It carries
// no message of its own — the findings were printed.
var ErrFindings = errors.New("findings of severity error")

// LintFile reads the resolution root's lint file, the empty
// configuration where none exists (check-rules.md, the lint file
// term).
func (s *Session) LintFile() (*lintfile.File, error) {
	p := path.Join(s.Root.Dir, lintfile.FileName)
	b, err := fs.ReadFile(iofs.New(s.WS), p)
	if errors.Is(err, fs.ErrNotExist) {
		return &lintfile.File{}, nil
	}
	if err != nil {
		return nil, err
	}
	lf, err := lintfile.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return lf, nil
}

// checkRun is what the two check verbs share: the build's modules,
// the lint file, the enabled rules of one kind, the compiled schema
// and its environment set, and the checked modules' files.
type checkRun struct {
	lint    *lintfile.File
	sel     lintfile.Selection
	mods    []modfiles.Module
	rules   []rules.Rule
	set     *env1.Set
	checked []string // every checked module's files, module-relative
}

// prepare assembles a check run of one kind (REQ-check-lint-verb,
// REQ-check-breaking-verb): the build list and the modules' files,
// the lint file, the rulesets it names found among the build's
// modules, the enabled rules of the kind, the checked schema
// compiled.
func prepare(ctx context.Context, s *Session, kind check.Kind) (*checkRun, error) {
	_, mods, err := s.Modules(ctx)
	if err != nil {
		return nil, err
	}
	lf, err := s.LintFile()
	if err != nil {
		return nil, err
	}
	sets, err := lintfile.Rulesets(lf, s.Root, mods)
	if err != nil {
		return nil, err
	}
	sel, err := lintfile.Select(lf, sets)
	if err != nil {
		return nil, err
	}
	var rs []rules.Rule
	for _, r := range sel.Rules {
		if r.Kind == kind {
			rs = append(rs, r)
		}
	}
	result, err := compile.Compile(ctx, mods)
	if err != nil {
		return nil, err
	}
	run := &checkRun{lint: lf, sel: sel, mods: mods, rules: rs, set: env1.NewSet(result.Files)}
	for _, m := range mods {
		if m.Local {
			run.checked = append(run.checked, m.Protos()...)
		}
	}
	return run, nil
}

// source reads a checked file's text from the workspace modules.
func (r *checkRun) source(p string) ([]byte, error) {
	for _, m := range r.mods {
		if !m.Local {
			continue
		}
		if b, ok := m.Files[p]; ok {
			return b, nil
		}
	}
	return nil, fmt.Errorf("%s is no checked file", p)
}

// report prints the findings the lint file does not ignore, sorted,
// and returns ErrFindings where any of severity error remains
// (REQ-check-findings-output, REQ-check-exit-status).
func (r *checkRun) report(findings []check.Finding, out io.Writer) error {
	kept := findings[:0:0]
	for _, f := range findings {
		if !r.sel.Ignored(f.Path, f.Rule) {
			kept = append(kept, f)
		}
	}
	check.Sort(kept)
	for _, f := range kept {
		if _, err := fmt.Fprintln(out, f); err != nil {
			return err
		}
	}
	if check.Failing(kept) {
		return ErrFindings
	}
	return nil
}

// Lint evaluates every enabled lint rule over the checked modules and
// reports the findings (REQ-check-lint-verb); with zero lint rules
// enabled it says so on diag and reports nothing.
func Lint(ctx context.Context, s *Session, out, diag io.Writer) error {
	run, err := prepare(ctx, s, check.KindLint)
	if err != nil {
		return fmt.Errorf("lint: %w", err)
	}
	if len(run.rules) == 0 {
		fmt.Fprintln(diag, "pb lint: zero lint rules enabled")
		return nil
	}
	env, err := env1.New(run.set, nil)
	if err != nil {
		return fmt.Errorf("lint: %w", err)
	}
	report, err := eval.Lint(env, run.checked, run.source, run.rules)
	if err != nil {
		return fmt.Errorf("lint: %w", err)
	}
	return run.report(report.Findings, out)
}

// BreakingDeps is what the breaking verb draws on beside the session:
// the repository the workspace root lies in, opened on demand for a
// reference base.
type BreakingDeps struct {
	Repo func() (*git.Repository, string, error)
}

// Breaking materializes each module under check's base, pairs it with
// the module's checked schema, evaluates every enabled breaking rule
// over the pairs, and reports every module's findings as one stream
// (REQ-check-breaking-verb); with zero breaking rules enabled it says
// so on diag and reports nothing. A version base is pinned as any
// dependency, the lockfile saved as each is materialized.
func Breaking(ctx context.Context, s *Session, deps BreakingDeps, out, diag io.Writer) error {
	run, err := prepare(ctx, s, check.KindBreaking)
	if err != nil {
		return fmt.Errorf("breaking: %w", err)
	}
	if len(run.rules) == 0 {
		fmt.Fprintln(diag, "pb breaking: zero breaking rules enabled")
		return nil
	}
	if run.lint.Breaking == nil {
		return fmt.Errorf("breaking: %s names no breaking base (breaking.base: one of ref, version, pinned)", lintfile.FileName)
	}
	sources := breaking.Sources{Repo: deps.Repo, Zip: s.Client.Zip, Lock: s.Lock}
	var findings []check.Finding
	for i, m := range run.mods {
		// A module under check with no protobuf files has nothing to
		// pair and needs no base.
		if !m.Local || len(m.Files) == 0 {
			continue
		}
		base, err := breaking.Materialize(ctx, run.lint.Breaking.Base, breaking.Module{Path: m.Path, Dir: m.Dir}, sources)
		// A version base was pinned on the way; the pin is the record
		// whatever follows.
		if err := savePins(s, err); err != nil {
			return fmt.Errorf("breaking: %s: %w", m.Path, err)
		}
		// The base's files in place of the module under check's, the
		// build's other modules resolving its imports and none other a
		// target (REQ-break-base-materialized).
		baseMods := append([]modfiles.Module(nil), run.mods...)
		baseMods[i] = modfiles.Module{Path: m.Path, Local: true, Dir: m.Dir, Files: base.Files}
		compiled, err := compile.CompileOnly(ctx, baseMods, i)
		if err != nil {
			return fmt.Errorf("breaking: %s: the base %s: %w", m.Path, base.Label, err)
		}
		oldSet := env1.NewSet(compiled.Files)
		env, err := env1.New(run.set, oldSet)
		if err != nil {
			return fmt.Errorf("breaking: %w", err)
		}
		baseSource := func(p string) ([]byte, error) {
			if b, ok := base.Files[p]; ok {
				return b, nil
			}
			return nil, fmt.Errorf("%s is no file of the base %s", p, base.Label)
		}
		oldChecked := make([]string, 0, len(base.Files))
		for p := range base.Files {
			oldChecked = append(oldChecked, p)
		}
		report, err := eval.Breaking(env, oldChecked, m.Protos(), run.source, baseSource, run.rules)
		if err != nil {
			return fmt.Errorf("breaking: %s: %w", m.Path, err)
		}
		findings = append(findings, report.Findings...)
	}
	return run.report(findings, out)
}
