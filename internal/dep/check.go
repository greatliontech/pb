package dep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"

	"github.com/bufbuild/protocompile/linker"
	"github.com/go-git/go-billy/v6/helper/iofs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/go-git/go-git/v6"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

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
	files  linker.Files // the checked schema as compiled, the set's files
}

// prepare assembles a check run of one kind (REQ-check-lint-verb,
// REQ-check-breaking-verb): the build list and the modules' files,
// the lint file, the rulesets it imports read as written, the
// checked modules grouped by the selection governing each with its
// enabled rules of the kind, the checked schema compiled.
func prepare(ctx context.Context, s *Session, kind check.Kind) (*checkRun, error) {
	_, mods, err := s.Modules(ctx)
	if err != nil {
		return nil, err
	}
	lf, err := s.LintFile()
	if err != nil {
		return nil, err
	}
	// Each import read as written, an external one pinned as a
	// ruleset on the way and the pins saved before anything follows
	// (REQ-lint-rulesets-imported, REQ-lock-first-use).
	sets, err := lintfile.Rulesets(ctx, lf, s.Root, iofs.New(s.WS), s.Client.RulesetZip)
	if err := savePins(s, err); err != nil {
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
	run := &checkRun{kind: kind, lint: lf, sel: sel, mods: mods, set: env1.NewSet(result.Files), files: result.Files, group: map[string]*checkGroup{}}
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

// setBase is a descriptor set standing as a base: its files linked
// once, read per module under check as the files the module provides
// now, and once more as the files no module of the build provides —
// a deletion or a dropped dependency, judged under the root's
// selection (REQ-break-base-materialized).
type setBase struct {
	set      *descriptorpb.FileDescriptorSet
	registry *protoregistry.Files
	label    string
}

func newSetBase(set *descriptorpb.FileDescriptorSet, label string) (*setBase, error) {
	registry, err := protodesc.NewFiles(set)
	if err != nil {
		return nil, fmt.Errorf("the base %s: %w", label, err)
	}
	return &setBase{set: set, registry: registry, label: label}, nil
}

// attribute tells, for every file of the set, the module of the build
// it is a base file of, or none: the module providing it now, by
// name, else the one local module declaring its package now — a
// file deleted from that module, as the version form would hold it
// — and none where no module provides it and no local module, or
// several, declare its package: a dropped dependency, or a package
// deleted whole, told apart by nothing in the set. A well-known
// import is no module's, a copy of one no file of the build.
func (b *setBase) attribute(mods []modfiles.Module, files linker.Files) map[string]int {
	// The packages the local modules declare now, by the modules
	// declaring them.
	moduleOf := map[string]int{}
	for j, m := range mods {
		if !m.Local {
			continue
		}
		for _, p := range m.Protos() {
			moduleOf[p] = j
		}
	}
	declares := map[string]map[int]bool{}
	for _, f := range files {
		if j, local := moduleOf[f.Path()]; local && f.Package() != "" {
			pkg := string(f.Package())
			if declares[pkg] == nil {
				declares[pkg] = map[int]bool{}
			}
			declares[pkg][j] = true
		}
	}
	out := map[string]int{}
	for _, fd := range b.set.File {
		p := fd.GetName()
		out[p] = -1
		if modfiles.WellKnown(p) {
			continue
		}
		// One module at most provides a path: two would have failed
		// the build before this.
		for j, m := range mods {
			if _, own := m.Files[p]; own {
				out[p] = j
			}
		}
		// A file declaring no package is of no module.
		if out[p] == -1 && fd.GetPackage() != "" && len(declares[fd.GetPackage()]) == 1 {
			for j := range declares[fd.GetPackage()] {
				out[p] = j
			}
		}
	}
	return out
}

// files is the set's files named, linked, in the set's order, as a
// base's old side.
func (b *setBase) files(keep func(name string) bool) (linker.Files, []string, error) {
	var checked []string
	var files linker.Files
	for _, fd := range b.set.File {
		p := fd.GetName()
		if !keep(p) {
			continue
		}
		fd, err := b.registry.FindFileByPath(p)
		if err != nil {
			return nil, nil, fmt.Errorf("the base %s: %w", b.label, err)
		}
		f, err := linker.NewFileRecursive(fd)
		if err != nil {
			return nil, nil, fmt.Errorf("the base %s: %w", b.label, err)
		}
		files = append(files, f)
		checked = append(checked, p)
	}
	return files, checked, nil
}

// ofModule is the set's files attributed to the module at i.
func (b *setBase) ofModule(attributed map[string]int, i int) (linker.Files, []string, error) {
	return b.files(func(p string) bool { return attributed[p] == i })
}

// unattributed is the set's files attributed to no module and no
// well-known import.
func (b *setBase) unattributed(attributed map[string]int) (linker.Files, []string, error) {
	return b.files(func(p string) bool { return attributed[p] == -1 && !modfiles.WellKnown(p) })
}

// noText is a base with no source at hand: a finding there carries
// the recorded column.
func noText(string) ([]byte, error) { return nil, nil }

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
	if run.lint.Breaking == nil {
		return fmt.Errorf("breaking: %s names no breaking base (breaking.base: one of ref, version, pinned, file)", lintfile.FileName)
	}
	// Zero rules enabled: under a descriptor set the root's rules
	// count too, the set's files no module provides being judged
	// under them whatever the modules' own entries enable.
	fromSet := run.lint.Breaking.Base.Form == lintfile.BaseFile
	if run.rules == 0 && !(fromSet && len(run.groups[0].rules) != 0) {
		fmt.Fprintln(diag, "pb breaking: zero breaking rules enabled")
		return nil
	}
	sources := breaking.Sources{Repo: deps.Repo, Zip: s.Client.Zip, Lock: s.Lock, Read: func(p string) ([]byte, error) {
		return util.ReadFile(s.WS, path.Join(s.Root.Dir, p))
	}}
	// A descriptor set is one base for the whole build, read once and
	// its files attributed once.
	var set *setBase
	var attributed map[string]int
	if fromSet {
		base, err := breaking.Materialize(ctx, run.lint.Breaking.Base, breaking.Module{}, sources)
		if err != nil {
			return fmt.Errorf("breaking: %w", err)
		}
		if set, err = newSetBase(base.Set, base.Label); err != nil {
			return fmt.Errorf("breaking: %w", err)
		}
		attributed = set.attribute(run.mods, run.files)
	}
	var findings []check.Finding
	for i, m := range run.mods {
		// A module under check with no protobuf files has nothing to
		// pair and needs no base; nor does one whose selection enables
		// no breaking rule.
		if !m.Local || len(m.Protos()) == 0 || len(run.rulesOf(m.Dir)) == 0 {
			continue
		}
		var oldFiles linker.Files
		var oldChecked []string
		var baseSource eval.Source
		if set != nil {
			// The set's files the module provides now, as compiled
			// (REQ-break-base-materialized).
			oldFiles, oldChecked, err = set.ofModule(attributed, i)
			if err != nil {
				return fmt.Errorf("breaking: %s: %w", m.Path, err)
			}
			baseSource = noText
		} else {
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
			// The base's files of the build, as the module under check's:
			// the one enumeration, a copy of a well-known path in neither.
			oldFiles, oldChecked = compiled.Files, baseMods[i].Protos()
			baseSource = func(p string) ([]byte, error) {
				if b, ok := base.Files[p]; ok {
					return b, nil
				}
				return nil, fmt.Errorf("%s is no file of the base %s", p, base.Label)
			}
		}
		judged, err := run.judge(oldFiles, oldChecked, m.Protos(), baseSource, run.group[m.Dir], eval.Breaking)
		if err != nil {
			return fmt.Errorf("breaking: %s: %w", m.Path, err)
		}
		findings = append(findings, judged...)
	}
	// The set's files of no module — a package deleted whole, or
	// dependencies the build no longer holds, told apart by nothing
	// in the set — once more as a base of their own, their
	// declarations paired by name with everything the build holds
	// now, additions left aside, judged under the root's selection
	// (REQ-break-base-materialized).
	if root := run.groups[0]; set != nil && len(root.rules) != 0 {
		oldFiles, oldChecked, err := set.unattributed(attributed)
		if err != nil {
			return fmt.Errorf("breaking: %w", err)
		}
		if len(oldChecked) != 0 {
			var newChecked []string
			for _, m := range run.mods {
				if m.Local {
					newChecked = append(newChecked, m.Protos()...)
				}
			}
			judged, err := run.judge(oldFiles, oldChecked, newChecked, noText, root, eval.BreakingOldSide)
			if err != nil {
				return fmt.Errorf("breaking: %s: %w", set.label, err)
			}
			findings = append(findings, judged...)
		}
	}
	return run.report(findings, out)
}

// judge pairs an old side with the checked new files and evaluates
// the group's rules over the pairs, the findings admitted as the
// group's selection has them.
func (r *checkRun) judge(oldFiles linker.Files, oldChecked, newChecked []string, baseSource eval.Source, g *checkGroup, evaluate func(*env1.Env, []string, []string, eval.Source, eval.Source, []rules.Rule) (*check.Report, error)) ([]check.Finding, error) {
	env, err := env1.New(r.set, env1.NewSet(oldFiles))
	if err != nil {
		return nil, err
	}
	report, err := evaluate(env, oldChecked, newChecked, r.source, baseSource, g.rules)
	if err != nil {
		return nil, err
	}
	return g.admit(r.sel, r.kind, report.Findings), nil
}
