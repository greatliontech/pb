package dep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"

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
// the lint file and its selection, the checked modules grouped by the
// selection governing each, the count of rules of one kind enabled
// across the groups that evaluate, and the compiled schema's
// environment set.
type checkRun struct {
	kind   check.Kind
	lint   *lintfile.File
	sel    lintfile.Selection
	mods   []modfiles.Module
	groups []*checkGroup          // the root's first, then each module with its own selection
	group  map[string]*checkGroup // by module directory
	rules  int                    // enabled rules of the kind, every evaluating group's
	set    *env1.Set
}

// prepare assembles a check run of one kind (REQ-check-lint-verb,
// REQ-check-breaking-verb): the build list and the modules' files,
// the lint file, the rulesets it names found among the build's
// modules, the checked modules grouped by the selection governing
// each with its enabled rules of the kind, the checked schema
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
	local := map[string]bool{}
	for _, m := range mods {
		if m.Local {
			local[m.Dir] = true
		}
	}
	named := make([]string, 0, len(sel.Modules))
	for d := range sel.Modules {
		named = append(named, d)
	}
	sort.Strings(named)
	for _, d := range named {
		if !local[d] {
			return nil, fmt.Errorf("%s: modules names %q, which is no workspace module", lintfile.FileName, d)
		}
	}
	result, err := compile.Compile(ctx, mods)
	if err != nil {
		return nil, err
	}
	run := &checkRun{kind: kind, lint: lf, sel: sel, mods: mods, set: env1.NewSet(result.Files), group: map[string]*checkGroup{}}
	// The root's selection governs every module without an entry,
	// as one group; a module with an entry is a group of its own,
	// located at its directory.
	root := &checkGroup{}
	run.groups = append(run.groups, root)
	for _, m := range mods {
		if !m.Local {
			continue
		}
		g := root
		if _, own := sel.Modules[m.Dir]; own {
			g = &checkGroup{dir: m.Dir}
			run.groups = append(run.groups, g)
		}
		g.checked = append(g.checked, m.Protos()...)
		run.group[m.Dir] = g
	}
	for _, g := range run.groups {
		for _, r := range sel.RulesFor(g.dir) {
			if r.Kind == kind {
				g.rules = append(g.rules, r)
			}
		}
		if g.evaluates() {
			run.rules += len(g.rules)
		}
	}
	return run, nil
}

// checkGroup is the files one selection governs and the rules of the
// run's kind it enables: the root's, or a module's own, located at
// the module's directory.
type checkGroup struct {
	dir     string // the module's directory for its own selection; "" for the root's
	checked []string
	rules   []rules.Rule
}

// evaluates reports whether the group has anything to judge: files
// under its selection and rules of the kind enabled for them; the
// run counts rules over such groups alone.
func (g *checkGroup) evaluates() bool { return len(g.checked) != 0 && len(g.rules) != 0 }

// admit is a group's findings the lint file keeps, each located as
// the group's selection has it: under a module's own, a finding
// without a position — a set rule's, or a package rule's — at the
// module's directory (REQ-rules-finding-location); less those the
// root's ignores exclude and, under a module's own selection, the
// module's, an entry naming a kind excluding the run's kind alone
// (REQ-lint-selection). The group is the finding's module:
// its files are the ones the group checks, on either side of a
// breaking run.
func (g *checkGroup) admit(sel lintfile.Selection, kind check.Kind, findings []check.Finding) []check.Finding {
	kept := findings[:0:0]
	for _, f := range findings {
		if g.dir != "" && f.Line == 0 {
			f.Path = g.dir
		}
		if sel.Ignored(g.dir, f.Path, f.Rule, kind) {
			continue
		}
		kept = append(kept, f)
	}
	return kept
}

// rulesOf is the rules of the run's kind governing a module.
func (r *checkRun) rulesOf(dir string) []rules.Rule {
	if g := r.group[dir]; g != nil {
		return g.rules
	}
	return nil
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

// report prints the admitted findings, sorted, and returns
// ErrFindings where any of severity error remains
// (REQ-check-findings-output, REQ-check-exit-status).
func (r *checkRun) report(kept []check.Finding, out io.Writer) error {
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

// Lint evaluates every enabled lint rule over the checked modules,
// each module's files under the selection governing it, and reports
// the findings (REQ-check-lint-verb); with zero lint rules enabled
// under every selection it says so on diag and reports nothing.
func Lint(ctx context.Context, s *Session, out, diag io.Writer) error {
	run, err := prepare(ctx, s, check.KindLint)
	if err != nil {
		return fmt.Errorf("lint: %w", err)
	}
	if run.rules == 0 {
		fmt.Fprintln(diag, "pb lint: zero lint rules enabled")
		return nil
	}
	env, err := env1.New(run.set, nil)
	if err != nil {
		return fmt.Errorf("lint: %w", err)
	}
	var findings []check.Finding
	for _, g := range run.groups {
		if !g.evaluates() {
			continue
		}
		report, err := eval.Lint(env, g.checked, run.source, g.rules)
		if err != nil {
			return fmt.Errorf("lint: %w", err)
		}
		findings = append(findings, g.admit(run.sel, run.kind, report.Findings)...)
	}
	return run.report(findings, out)
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
	if run.rules == 0 {
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
		// pair and needs no base; nor does one whose selection enables
		// no breaking rule.
		if !m.Local || len(m.Files) == 0 || len(run.rulesOf(m.Dir)) == 0 {
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
		compiled, err := compile.CompileFiles(ctx, baseMods, i, baseMods[i].Protos())
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
		report, err := eval.Breaking(env, oldChecked, m.Protos(), run.source, baseSource, run.rulesOf(m.Dir))
		if err != nil {
			return fmt.Errorf("breaking: %s: %w", m.Path, err)
		}
		findings = append(findings, run.group[m.Dir].admit(run.sel, run.kind, report.Findings)...)
	}
	return run.report(findings, out)
}
