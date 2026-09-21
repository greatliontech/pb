// Package lintfile parses pb.lint.yaml, the lint file at the resolution
// root (check-rules.md §Configuration and suppression): the rulesets to
// import, the rule selection and severity overrides — the root's, and a
// module's own by its directory — the path ignores, and the
// breaking-change base. It selects the enabled rules from the imported
// rulesets' rule files, finds the rulesets among the build's modules,
// and judges a finding's path against the ignores; the verbs assemble
// the rest.
package lintfile

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/greatliontech/glob"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/check/rules"
	"github.com/greatliontech/pb/internal/contractfile"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/module/workspace"
	"github.com/greatliontech/pb/internal/proto/modfiles"
	"github.com/greatliontech/pb/internal/rootpath"
)

// FileName is the lint file's name at the resolution root.
const FileName = "pb.lint.yaml"

// ErrInvalid is wrapped by every schema rejection (REQ-lint-config-
// schema).
var ErrInvalid = errors.New("invalid lint file")

// ErrSelection is wrapped when the selection names what no imported
// rule declares, a bare spelling several rulesets declare, a ruleset
// declaring a name twice, or two spellings of one rule
// (REQ-lint-selection).
var ErrSelection = errors.New("invalid rule selection")

// ErrRuleset is wrapped when a ruleset the lint file names is no
// module of the build, or its rule files fail (REQ-lint-rulesets-
// declared, REQ-rules-file-discovery).
var ErrRuleset = errors.New("ruleset")

// File is a parsed lint file, every part optional.
type File struct {
	Rulesets []string
	Enable   []string // rule names, ids or tags; nil means every imported rule
	Exclude  []string
	Severity map[string]check.Severity
	Ignore   []Ignore
	Breaking *Breaking
	// Per-module selections by the module's directory, each replacing
	// the root's enable, exclude and severity for that module's files.
	Modules map[string]ModuleSelection
}

// ModuleSelection is one module's own selection, its ignores over its
// own files beside the root's.
type ModuleSelection struct {
	Enable   []string
	Exclude  []string
	Severity map[string]check.Severity
	Ignore   []Ignore
}

// Ignore excludes rules under path globs: every rule where Rules is
// nil.
type Ignore struct {
	Paths []*glob.Pattern
	Rules []string
	Kind  check.Kind // the kind whose findings alone the entry excludes; "" for either
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
	selection := func(where string, enable, exclude *[]string, severity *map[string]check.Severity) []contractfile.Field {
		list := func(name string, into *[]string) contractfile.Field {
			return contractfile.Field{Name: name, Read: func(n ast.Node) error {
				l, err := contractfile.Strings(n, where+name, ErrInvalid)
				*into = l
				return err
			}}
		}
		return []contractfile.Field{
			list("enable", enable),
			list("exclude", exclude),
			{Name: "severity", Read: func(n ast.Node) error {
				sm, ok := n.(*ast.MappingNode)
				if !ok {
					return fmt.Errorf("%w: %sseverity must be a mapping from rule name or id to severity", ErrInvalid, where)
				}
				*severity = map[string]check.Severity{}
				for _, skv := range sm.Values {
					id := contractfile.Key(skv.Key)
					text, ok := contractfile.Line(skv.Value)
					sv, valid := check.ParseSeverity(text)
					if !ok || !valid {
						return fmt.Errorf("%w: %sseverity.%s must be %s or %s", ErrInvalid, where, id, check.SeverityError, check.SeverityWarning)
					}
					(*severity)[id] = sv
				}
				return nil
			}},
		}
	}
	root := selection("", &f.Enable, &f.Exclude, &f.Severity)
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
		root[0], root[1], root[2],
		contractfile.Field{Name: "modules", Read: func(n ast.Node) error {
			mm, ok := n.(*ast.MappingNode)
			if !ok {
				return fmt.Errorf("%w: modules must be a mapping from module directory to selection", ErrInvalid)
			}
			f.Modules = map[string]ModuleSelection{}
			for _, mkv := range mm.Values {
				dir := contractfile.Key(mkv.Key)
				if err := rootpath.Check(dir, "the workspace root"); err != nil {
					return fmt.Errorf("%w: modules: %v", ErrInvalid, err)
				}
				var ms ModuleSelection
				fields := selection("modules."+dir+".", &ms.Enable, &ms.Exclude, &ms.Severity)
				fields = append(fields, contractfile.Field{Name: "ignore", Read: func(n ast.Node) error {
					igs, err := parseIgnores(n, "modules."+dir+".ignore")
					ms.Ignore = igs
					return err
				}})
				if err := contractfile.Mapping(mkv.Value, "modules."+dir, ErrInvalid, fields...); err != nil {
					return err
				}
				f.Modules[dir] = ms
			}
			return nil
		}},
		contractfile.Field{Name: "ignore", Read: func(n ast.Node) error {
			igs, err := parseIgnores(n, "ignore")
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

func parseIgnores(n ast.Node, key string) ([]Ignore, error) {
	out := []Ignore{}
	err := contractfile.Sequence(n, key, ErrInvalid, func(i int, en ast.Node) error {
		where := fmt.Sprintf("%s[%d]", key, i)
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
			contractfile.Field{Name: "kind", Read: func(n ast.Node) error {
				v, ok := contractfile.String(n)
				if !ok || (check.Kind(v) != check.KindLint && check.Kind(v) != check.KindBreaking) {
					return fmt.Errorf("%w: %s.kind must be lint or breaking", ErrInvalid, where)
				}
				ig.Kind = check.Kind(v)
				return nil
			}},
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

// Selection is a lint file's selection over the imported rulesets:
// the enabled rules under the root's selection, each module's own
// under its entry by directory, and the ignore entries with every
// rule spelled by its name.
type Selection struct {
	Rules   []rules.Rule
	Modules map[string][]rules.Rule
	ignore  []Ignore            // the root's, over every module
	ignores map[string][]Ignore // a module's own, over its files, by directory
}

// RulesFor is the rules governing a module at its directory: its own
// entry's where the file holds one, the root's otherwise.
func (s Selection) RulesFor(dir string) []rules.Rule {
	if rs, ok := s.Modules[dir]; ok {
		return rs
	}
	return s.Rules
}

// Ignored reports whether a finding of the named rule, of the kind,
// at the path — a module-relative file path, or a module's directory
// for a finding located there — is excluded: by the root's ignores,
// which reach every module, or by the module's own, at its directory,
// which reach its files alone; an entry naming rules excludes those
// alone, one naming a kind that kind's findings alone (REQ-lint-config-schema,
// REQ-lint-selection). A finding without a path is never ignored.
func (s Selection) Ignored(dir, path, name string, kind check.Kind) bool {
	if path == "" {
		return false
	}
	for _, list := range [][]Ignore{s.ignore, s.ignores[dir]} {
		for _, ig := range list {
			if ig.Rules != nil && !contains(ig.Rules, name) {
				continue
			}
			if ig.Kind != "" && ig.Kind != kind {
				continue
			}
			for _, g := range ig.Paths {
				if g.Match(path) {
					return true
				}
			}
		}
	}
	return false
}

// Select is the enabled rules (REQ-lint-selection): every rule of
// every imported ruleset when enable is absent, else the rules enable
// names — by name, by qualified tag, or by a bare id or tag exactly
// one ruleset declares — less those exclude names; each at the
// severity the file overrides for it, severity and ignore naming
// rules alone. The order is the rulesets', then the rule files' by
// path, then declaration. A spelling no imported rule declares, a
// bare spelling several rulesets declare, a name a ruleset declares
// twice or as both an id and a tag, and two severity spellings of one
// rule, fail naming them.
func Select(f *File, sets []Ruleset) (Selection, error) {
	var all []rules.Rule
	for _, s := range sets {
		declaredIn := map[string]string{}
		tagsIn := map[string]string{}
		for _, rf := range s.Files {
			for _, r := range rf.File.Rules {
				r.Ruleset = s.Path
				where := s.Path + "/" + rf.Path
				if prior, dup := declaredIn[r.ID]; dup {
					return Selection{}, fmt.Errorf("%w: %s declared by %s and %s", ErrSelection, r.Name(), prior, where)
				}
				declaredIn[r.ID] = where
				for _, t := range r.Tags {
					if _, tagged := tagsIn[t]; !tagged {
						tagsIn[t] = where
					}
				}
				all = append(all, r)
			}
		}
		for _, r := range all {
			if r.Ruleset != s.Path {
				continue
			}
			if by, clash := tagsIn[r.ID]; clash {
				return Selection{}, fmt.Errorf("%w: %s is both a rule, declared by %s, and a tag, first carried by a rule of %s", ErrSelection, r.Name(), declaredIn[r.ID], by)
			}
		}
	}
	n := index(all)
	rootRules, err := n.pick(all, f.Enable, f.Exclude, f.Severity, "")
	if err != nil {
		return Selection{}, err
	}
	sel := Selection{Rules: rootRules}
	dirs := make([]string, 0, len(f.Modules))
	for d := range f.Modules {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		ms := f.Modules[d]
		rs, err := n.pick(all, ms.Enable, ms.Exclude, ms.Severity, "modules."+d+".")
		if err != nil {
			return Selection{}, err
		}
		if sel.Modules == nil {
			sel.Modules = map[string][]rules.Rule{}
		}
		sel.Modules[d] = rs
		igs, err := n.ignores(ms.Ignore, "modules."+d+".ignore")
		if err != nil {
			return Selection{}, err
		}
		if len(igs) > 0 {
			if sel.ignores == nil {
				sel.ignores = map[string][]Ignore{}
			}
			sel.ignores[d] = igs
		}
	}
	igs, err := n.ignores(f.Ignore, "ignore")
	if err != nil {
		return Selection{}, err
	}
	sel.ignore = igs
	return sel, nil
}

// ignores is a list of ignore entries with each rule spelling
// resolved to the one rule name it means.
func (n names) ignores(list []Ignore, where string) ([]Ignore, error) {
	var out []Ignore
	for _, ig := range list {
		canonical := Ignore{Paths: ig.Paths, Kind: ig.Kind}
		if ig.Rules != nil {
			canonical.Rules = []string{}
			for _, spelling := range ig.Rules {
				names, err := n.resolve(where, spelling, true)
				if err != nil {
					return nil, err
				}
				canonical.Rules = append(canonical.Rules, names[0])
			}
		}
		out = append(out, canonical)
	}
	return out, nil
}

// pick is one selection over the imported rules: every rule when
// enable is nil, else the rules it names, less the excluded, each at
// its overridden severity; where names the spellings in the file.
func (n names) pick(all []rules.Rule, enable, exclude []string, severity map[string]check.Severity, where string) ([]rules.Rule, error) {
	resolve := func(what string, list []string) (map[string]bool, error) {
		out := map[string]bool{}
		for _, spelling := range list {
			names, err := n.resolve(where+what, spelling, false)
			if err != nil {
				return nil, err
			}
			for _, name := range names {
				out[name] = true
			}
		}
		return out, nil
	}
	enabled, err := resolve("enable", enable)
	if err != nil {
		return nil, err
	}
	excluded, err := resolve("exclude", exclude)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(severity))
	for k := range severity {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	overrides := map[string]check.Severity{}
	spelled := map[string]string{}
	for _, k := range keys {
		names, err := n.resolve(where+"severity", k, true)
		if err != nil {
			return nil, err
		}
		if prior, twice := spelled[names[0]]; twice {
			return nil, fmt.Errorf("%w: %sseverity names %s twice, as %q and %q", ErrSelection, where, names[0], prior, k)
		}
		spelled[names[0]] = k
		overrides[names[0]] = severity[k]
	}
	out := []rules.Rule{}
	for _, r := range all {
		if enable != nil && !enabled[r.Name()] {
			continue
		}
		if excluded[r.Name()] {
			continue
		}
		if sv, ok := overrides[r.Name()]; ok {
			r.Severity = sv
		}
		out = append(out, r)
	}
	return out, nil
}

// names indexes the imported rules' spellings (the rule name term):
// every rule by its name; every tag, qualified by its ruleset, by the
// names of the rules that carry it; and every bare id and tag by the
// qualified spellings it may mean — a ruleset's ids and tags one
// namespace, checked apart, so a qualified spelling is a rule or a
// tag, never both.
type names struct {
	rule map[string]bool
	tag  map[string][]string
	bare map[string][]string
}

func index(all []rules.Rule) names {
	n := names{rule: map[string]bool{}, tag: map[string][]string{}, bare: map[string][]string{}}
	seen := map[string]bool{}
	add := func(bare, qualified string) {
		if !seen[bare+"\x00"+qualified] {
			seen[bare+"\x00"+qualified] = true
			n.bare[bare] = append(n.bare[bare], qualified)
		}
	}
	for _, r := range all {
		n.rule[r.Name()] = true
		add(r.ID, r.Name())
		for _, t := range r.Tags {
			q := r.Ruleset + ":" + t
			n.tag[q] = append(n.tag[q], r.Name())
			add(t, q)
		}
	}
	return n
}

// resolve is the rule names a spelling means: a rule name itself; a
// qualified tag, the rules carrying it; a bare id or tag, whatever it
// means in exactly one ruleset — an unknown spelling, an ambiguous
// one, and a tag where a rule alone is admitted fail naming them
// (REQ-lint-selection).
func (n names) resolve(what, spelling string, rulesOnly bool) ([]string, error) {
	qualified := []string{spelling}
	if !strings.Contains(spelling, ":") {
		qualified = n.bare[spelling]
		if len(qualified) > 1 {
			return nil, fmt.Errorf("%w: %s names %q, which several imported rulesets declare: %s", ErrSelection, what, spelling, strings.Join(qualified, ", "))
		}
	}
	if len(qualified) == 1 {
		q := qualified[0]
		if n.rule[q] {
			return []string{q}, nil
		}
		if rs, ok := n.tag[q]; ok {
			if rulesOnly {
				return nil, fmt.Errorf("%w: %s names %q, a tag where a rule is named", ErrSelection, what, spelling)
			}
			return rs, nil
		}
	}
	return nil, fmt.Errorf("%w: %s names %q, which no imported rule declares as its id or a tag", ErrSelection, what, spelling)
}

// String spells a base for messages: its form and, where one is
// known, its value — the pinned form's once resolved.
func (b Base) String() string {
	if b.Value == "" {
		return string(b.Form)
	}
	return string(b.Form) + " " + b.Value
}

// Encode renders the file canonically (REQ-lint-emission): the keys in
// their order, each absent where it holds nothing — save enable,
// whose empty list means what its absence does not and is spelled
// `[]`; an ignore's empty rules list, which the schema forbids, is
// spelled the same so Parse refuses it by name — enable and exclude
// sorted, severity by key, ignore entries by their sorted paths then
// their sorted rules, modules by directory; each scalar spelled as
// contractfile.Spell has it. The rendering is
// validated first through Parse — Encode never emits what Parse
// rejects, nor what Parse reads as a different file.
func Encode(f *File) ([]byte, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: no file", ErrInvalid)
	}
	return contractfile.Emit(func(w *contractfile.Writer) {
		list := func(key string, values []string, sorted bool) {
			if values == nil {
				return
			}
			vs := slices.Clone(values)
			if sorted {
				slices.Sort(vs)
			}
			w.List(key, vs)
		}
		severity := func(sv map[string]check.Severity) {
			if len(sv) == 0 {
				return
			}
			w.Mapping("severity", func() {
				for _, k := range slices.Sorted(maps.Keys(sv)) {
					w.Scalar(k, string(sv[k]))
				}
			})
		}
		ignores := func(igs []Ignore) {
			if len(igs) == 0 {
				return
			}
			forms := ignoreForms(igs)
			w.Sequence("ignore", len(forms), func(i int) {
				w.List("paths", forms[i].paths)
				list("rules", forms[i].rules, false)
				if forms[i].kind != "" {
					w.Scalar("kind", string(forms[i].kind))
				}
			})
		}
		list("rulesets", orNil(f.Rulesets), false)
		list("enable", f.Enable, true)
		list("exclude", orNil(f.Exclude), true)
		severity(f.Severity)
		ignores(f.Ignore)
		if f.Breaking != nil {
			w.Mapping("breaking", func() {
				w.Mapping("base", func() {
					if f.Breaking.Base.Form == BasePinned {
						w.Literal("pinned", "true")
					} else {
						w.Scalar(string(f.Breaking.Base.Form), f.Breaking.Base.Value)
					}
				})
			})
		}
		if len(f.Modules) > 0 {
			w.Mapping("modules", func() {
				for _, d := range slices.Sorted(maps.Keys(f.Modules)) {
					ms := f.Modules[d]
					if ms.Enable == nil && len(ms.Exclude) == 0 && len(ms.Severity) == 0 && len(ms.Ignore) == 0 {
						w.Empty(d)
						continue
					}
					w.Mapping(d, func() {
						list("enable", ms.Enable, true)
						list("exclude", orNil(ms.Exclude), true)
						severity(ms.Severity)
						ignores(ms.Ignore)
					})
				}
			})
		}
	}, func(out []byte) (form, error) {
		again, err := Parse(out)
		if err != nil {
			return form{}, err
		}
		return formOf(again), nil
	}, formOf(f), func(a, b form) bool { return reflect.DeepEqual(a, b) }, ErrInvalid)
}

// orNil is the list, or nil for an empty one: a list whose absence
// means what its emptiness does.
func orNil(l []string) []string {
	if len(l) == 0 {
		return nil
	}
	return l
}

// ignoreForm is an ignore entry as the rendering orders it: the
// paths' spellings sorted, the rules sorted, nil rules kept nil, the
// kind as given.
type ignoreForm struct {
	paths, rules []string
	kind         check.Kind
}

// ignoreForms is the entries in canonical order: each by its sorted
// paths, then its sorted rules, then its kind.
func ignoreForms(igs []Ignore) []ignoreForm {
	out := make([]ignoreForm, len(igs))
	for i, ig := range igs {
		out[i].kind = ig.Kind
		for _, g := range ig.Paths {
			out[i].paths = append(out[i].paths, g.String())
		}
		slices.Sort(out[i].paths)
		if ig.Rules != nil {
			// Cloned, not collected: an empty list stays one.
			out[i].rules = slices.Clone(ig.Rules)
			slices.Sort(out[i].rules)
		}
	}
	slices.SortFunc(out, func(a, b ignoreForm) int {
		return cmp.Or(slices.Compare(a.paths, b.paths), slices.Compare(a.rules, b.rules), cmp.Compare(a.kind, b.kind))
	})
	return out
}

// form is what a file means, free of the spellings that mean nothing:
// list order where order means nothing, an empty list where absence
// means the same. Two files of one form render identically.
type form struct {
	Rulesets, Enable, Exclude []string
	Severity                  map[string]check.Severity
	Ignore                    []ignoreForm
	Breaking                  *Breaking
	Modules                   map[string]form
}

// formOf is the file's form, over every field of File and
// ModuleSelection: the unkeyed literals below stop compiling when a
// field is added, so none is left out of the form unnoticed.
var (
	_ = File{nil, nil, nil, nil, nil, nil, nil}
	_ = ModuleSelection{nil, nil, nil, nil}
)

func formOf(f *File) form {
	sorted := func(l []string) []string {
		if len(l) == 0 {
			return nil
		}
		return slices.Sorted(slices.Values(l))
	}
	severity := func(sv map[string]check.Severity) map[string]check.Severity {
		if len(sv) == 0 {
			return nil
		}
		return sv
	}
	ignores := func(igs []Ignore) []ignoreForm {
		if len(igs) == 0 {
			return nil
		}
		return ignoreForms(igs)
	}
	enable := func(l []string) []string {
		if l == nil {
			return nil
		}
		// Cloned, not collected: an empty enable stays one.
		l = slices.Clone(l)
		slices.Sort(l)
		return l
	}
	out := form{Rulesets: orNil(f.Rulesets), Enable: enable(f.Enable), Exclude: sorted(f.Exclude), Severity: severity(f.Severity), Ignore: ignores(f.Ignore), Breaking: f.Breaking}
	if len(f.Modules) > 0 {
		out.Modules = map[string]form{}
		for d, ms := range f.Modules {
			out.Modules[d] = form{Enable: enable(ms.Enable), Exclude: sorted(ms.Exclude), Severity: severity(ms.Severity), Ignore: ignores(ms.Ignore)}
		}
	}
	return out
}
