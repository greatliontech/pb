// Package lintfile parses pb.lint.yaml, the lint file at the resolution
// root (check-rules.md §Configuration and suppression): the rulesets to
// import, the rule selection and severity overrides, the path ignores,
// and the breaking-change base. It selects the enabled rules from the
// imported rulesets' rule files, finds the rulesets among the build's
// modules, and judges a finding's path against the ignores; the verbs
// assemble the rest.
package lintfile

import (
	"errors"
	"fmt"
	"sort"

	"github.com/goccy/go-yaml/ast"
	"github.com/greatliontech/glob"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/check/rules"
	"github.com/greatliontech/pb/internal/contractfile"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/module/workspace"
	"github.com/greatliontech/pb/internal/proto/modfiles"
)

// FileName is the lint file's name at the resolution root.
const FileName = "pb.lint.yaml"

// ErrInvalid is wrapped by every schema rejection (REQ-lint-config-
// schema).
var ErrInvalid = errors.New("invalid lint file")

// ErrSelection is wrapped when the selection names what no imported
// rule declares, or the imported rulesets declare an id twice
// (REQ-lint-selection).
var ErrSelection = errors.New("invalid rule selection")

// ErrRuleset is wrapped when a ruleset the lint file names is no
// module of the build, or its rule files fail (REQ-lint-rulesets-
// declared, REQ-rules-file-discovery).
var ErrRuleset = errors.New("ruleset")

// File is a parsed lint file, every part optional.
type File struct {
	Rulesets []string
	Enable   []string // rule ids or tags; nil means every imported rule
	Exclude  []string
	Severity map[string]check.Severity
	Ignore   []Ignore
	Breaking *Breaking
}

// Ignore excludes rules under path globs: every rule where Rules is
// nil.
type Ignore struct {
	Paths []*glob.Pattern
	Rules []string
}

// Breaking is the breaking-change configuration.
type Breaking struct {
	Base Base
}

// Base is the comparison base: its one form and, for a reference or a
// version, the value (REQ-break-base).
type Base struct {
	Form  BaseForm
	Value string
}

// BaseForm is one of the base's three forms.
type BaseForm string

// The forms, as the lint file spells them.
const (
	BaseRef     BaseForm = "ref"
	BaseVersion BaseForm = "version"
	BasePinned  BaseForm = "pinned"
)

// Parse decodes and validates a lint file (REQ-lint-config-schema).
func Parse(data []byte) (*File, error) {
	m, err := contractfile.Doc(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	f := &File{}
	if m == nil { // an empty file: every part absent
		return f, nil
	}
	list := func(name string, into *[]string) contractfile.Field {
		return contractfile.Field{Name: name, Read: func(n ast.Node) error {
			l, err := contractfile.Strings(n, name, ErrInvalid)
			*into = l
			return err
		}}
	}
	err = contractfile.Mapping(m, "", ErrInvalid,
		contractfile.Field{Name: "rulesets", Read: func(n ast.Node) error {
			paths, err := contractfile.Strings(n, "rulesets", ErrInvalid)
			if err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, p := range paths {
				if err := module.ValidatePath(p); err != nil {
					return fmt.Errorf("%w: rulesets: %v", ErrInvalid, err)
				}
				if seen[p] {
					return fmt.Errorf("%w: rulesets: %s listed twice", ErrInvalid, p)
				}
				seen[p] = true
			}
			f.Rulesets = paths
			return nil
		}},
		list("enable", &f.Enable),
		list("exclude", &f.Exclude),
		contractfile.Field{Name: "severity", Read: func(n ast.Node) error {
			sm, ok := n.(*ast.MappingNode)
			if !ok {
				return fmt.Errorf("%w: severity must be a mapping from rule id to severity", ErrInvalid)
			}
			f.Severity = map[string]check.Severity{}
			for _, skv := range sm.Values {
				id := contractfile.Key(skv.Key)
				text, ok := contractfile.Line(skv.Value)
				sv, valid := check.ParseSeverity(text)
				if !ok || !valid {
					return fmt.Errorf("%w: severity.%s must be %s or %s", ErrInvalid, id, check.SeverityError, check.SeverityWarning)
				}
				f.Severity[id] = sv
			}
			return nil
		}},
		contractfile.Field{Name: "ignore", Read: func(n ast.Node) error {
			igs, err := parseIgnores(n)
			f.Ignore = igs
			return err
		}},
		contractfile.Field{Name: "breaking", Read: func(n ast.Node) error {
			b, err := parseBreaking(n)
			f.Breaking = b
			return err
		}},
	)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func parseIgnores(n ast.Node) ([]Ignore, error) {
	out := []Ignore{}
	err := contractfile.Sequence(n, "ignore", ErrInvalid, func(i int, en ast.Node) error {
		where := fmt.Sprintf("ignore[%d]", i)
		var ig Ignore
		list := func(name string, set func([]string) error) contractfile.Field {
			return contractfile.Field{Name: name, Read: func(n ast.Node) error {
				l, err := contractfile.Strings(n, where+"."+name, ErrInvalid)
				if err != nil {
					return err
				}
				if len(l) == 0 {
					hint := ""
					if name == "rules" {
						hint = " (omit rules to ignore every rule)"
					}
					return fmt.Errorf("%w: %s.%s must not be empty%s", ErrInvalid, where, name, hint)
				}
				return set(l)
			}}
		}
		err := contractfile.Mapping(en, where, ErrInvalid,
			list("paths", func(ps []string) error {
				for _, p := range ps {
					g, err := glob.Compile(p)
					if err != nil {
						return fmt.Errorf("%w: %s.paths: %q: %v", ErrInvalid, where, p, err)
					}
					ig.Paths = append(ig.Paths, g)
				}
				return nil
			}),
			list("rules", func(rs []string) error { ig.Rules = rs; return nil }),
		)
		if err != nil {
			return err
		}
		if len(ig.Paths) == 0 {
			return fmt.Errorf("%w: %s has no paths", ErrInvalid, where)
		}
		out = append(out, ig)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func parseBreaking(n ast.Node) (*Breaking, error) {
	b := &Breaking{}
	err := contractfile.Mapping(n, "breaking", ErrInvalid,
		contractfile.Field{Name: "base", Required: true, Read: func(n ast.Node) error {
			forms := 0
			value := func(form BaseForm) contractfile.Field {
				return contractfile.Field{Name: string(form), Read: func(n ast.Node) error {
					forms++
					text, ok := contractfile.Line(n)
					if !ok || text == "" {
						return fmt.Errorf("%w: breaking.base.%s must be a non-empty string", ErrInvalid, form)
					}
					if form == BaseVersion {
						if _, err := version.Parse(text); err != nil {
							return fmt.Errorf("%w: breaking.base.version: %v", ErrInvalid, err)
						}
					}
					b.Base = Base{Form: form, Value: text}
					return nil
				}}
			}
			err := contractfile.Mapping(n, "breaking.base", ErrInvalid,
				value(BaseRef),
				value(BaseVersion),
				contractfile.Field{Name: string(BasePinned), Read: func(n ast.Node) error {
					forms++
					// The one spelling: the unquoted word true.
					tok := n.GetToken()
					if _, isBool := n.(*ast.BoolNode); !isBool || tok.Value != "true" {
						return fmt.Errorf("%w: breaking.base.pinned must be true", ErrInvalid)
					}
					b.Base = Base{Form: BasePinned}
					return nil
				}},
			)
			if err != nil {
				return err
			}
			if forms != 1 {
				return fmt.Errorf("%w: breaking.base must hold exactly one of ref, version, pinned", ErrInvalid)
			}
			return nil
		}},
	)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Ignored reports whether a finding of the rule at the path — a
// module-relative proto path — is excluded by an ignore entry: a
// path matching one of its globs, and the rule among its rules or the
// entry naming none (REQ-lint-config-schema's ignore). A finding
// without a path is never ignored here.
func (f *File) Ignored(path, ruleID string) bool {
	if path == "" {
		return false
	}
	for _, ig := range f.Ignore {
		if ig.Rules != nil && !contains(ig.Rules, ruleID) {
			continue
		}
		for _, g := range ig.Paths {
			if g.Match(path) {
				return true
			}
		}
	}
	return false
}

// Declared reports whether a module path may be a ruleset of the
// root: a workspace module, or a dependency some workspace module
// declares (REQ-lint-rulesets-declared).
func Declared(root *workspace.Root, path string) bool {
	for _, m := range root.Modules {
		if m.File.Module == path {
			return true
		}
		if _, ok := m.File.Deps[path]; ok {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Ruleset is an imported ruleset: the module and its rule files.
type Ruleset struct {
	Path  string
	Files []rules.Located
}

// Rulesets finds each ruleset the lint file names among the build's
// modules and reads its rule files (REQ-lint-rulesets-declared,
// REQ-rules-file-discovery): a path must be a workspace module or a
// dependency some workspace module declares, and be among the
// modules; a bad rule file fails naming the ruleset and the file.
func Rulesets(f *File, root *workspace.Root, mods []modfiles.Module) ([]Ruleset, error) {
	byPath := map[string]modfiles.Module{}
	for _, m := range mods {
		byPath[m.Path] = m
	}
	out := make([]Ruleset, 0, len(f.Rulesets))
	for _, p := range f.Rulesets {
		m, inBuild := byPath[p]
		if !Declared(root, p) || !inBuild {
			return nil, fmt.Errorf("%w %s: not a workspace module nor a dependency a workspace module declares", ErrRuleset, p)
		}
		files, err := rules.Discover(m.Rules)
		if err != nil {
			return nil, fmt.Errorf("%w %s: %w", ErrRuleset, p, err)
		}
		out = append(out, Ruleset{Path: p, Files: files})
	}
	return out, nil
}

// Select is the enabled rules (REQ-lint-selection): every rule of
// every imported ruleset when enable is absent, else the rules enable
// names by id or tag; less those exclude names; each at the severity
// the file overrides for it. The order is the rulesets', then the
// rule files' by path, then declaration. An id or tag that enable,
// exclude or severity names and no imported rule declares, and an id
// two rule files declare, fail naming it.
func Select(f *File, sets []Ruleset) ([]rules.Rule, error) {
	var all []rules.Rule
	declaredIn := map[string]string{}
	for _, s := range sets {
		for _, rf := range s.Files {
			for _, r := range rf.File.Rules {
				where := s.Path + "/" + rf.Path
				if prior, dup := declaredIn[r.ID]; dup {
					return nil, fmt.Errorf("%w: rule %s declared by %s and %s", ErrSelection, r.ID, prior, where)
				}
				declaredIn[r.ID] = where
				all = append(all, r)
			}
		}
	}
	names := map[string]bool{}
	for _, r := range all {
		names[r.ID] = true
		for _, t := range r.Tags {
			names[t] = true
		}
	}
	known := func(what string, list []string) error {
		for _, n := range list {
			if !names[n] {
				return fmt.Errorf("%w: %s names %q, which no imported rule declares as its id or a tag", ErrSelection, what, n)
			}
		}
		return nil
	}
	if err := known("enable", f.Enable); err != nil {
		return nil, err
	}
	if err := known("exclude", f.Exclude); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(f.Severity))
	for id := range f.Severity {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, ok := declaredIn[id]; !ok {
			return nil, fmt.Errorf("%w: severity names %q, which no imported rule declares as its id", ErrSelection, id)
		}
	}
	named := func(r rules.Rule, list []string) bool {
		for _, n := range list {
			if n == r.ID || contains(r.Tags, n) {
				return true
			}
		}
		return false
	}
	out := []rules.Rule{}
	for _, r := range all {
		if f.Enable != nil && !named(r, f.Enable) {
			continue
		}
		if named(r, f.Exclude) {
			continue
		}
		if sv, ok := f.Severity[r.ID]; ok {
			r.Severity = sv
		}
		out = append(out, r)
	}
	return out, nil
}

// String spells a base for messages: its form and, where one is
// known, its value — the pinned form's once resolved.
func (b Base) String() string {
	if b.Value == "" {
		return string(b.Form)
	}
	return string(b.Form) + " " + b.Value
}
