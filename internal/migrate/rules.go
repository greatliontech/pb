package migrate

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/greatliontech/glob"
	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/check/lintfile"
	"github.com/greatliontech/pb/internal/check/rules"
	"github.com/greatliontech/pb/internal/migrate/bufconfig"
	"github.com/greatliontech/pb/internal/rootpath"
)

var kinds = []check.Kind{check.KindLint, check.KindBreaking}

// bufDefaultUse is the category buf applies to a kind where a
// section or its use is absent: lint's DEFAULT (v1) or STANDARD
// (v2), which the ruleset tags STANDARD, and breaking's FILE.
func bufDefaultUse(kind check.Kind, version string) string {
	if kind == check.KindBreaking {
		return "FILE"
	}
	if version == "v1" {
		return "DEFAULT"
	}
	return "STANDARD"
}

// ruleOption is one of buf's rule-shaping options: the default the
// option holds where the file says nothing, the ruleset's rules it
// reshapes — every breaking rule for ignore_unstable_packages,
// spelled by none — and, for a value-bearing option, the ruleset's
// function reading the value, the target a rule over it declares
// and the binding the rule passes (REQ-migrate-rule-options).
type ruleOption struct {
	def                 string
	rules               []string
	fn, target, binding string
}

var ruleOptions = map[string]ruleOption{
	"enum_zero_value_suffix":                    {def: "_UNSPECIFIED", rules: []string{"ENUM_ZERO_VALUE_SUFFIX"}, fn: "enumZeroValueSuffix", target: "enum-value", binding: "enumValue"},
	"service_suffix":                            {def: "Service", rules: []string{"SERVICE_SUFFIX"}, fn: "serviceSuffix", target: "service", binding: "service"},
	"rpc_allow_same_request_response":           {def: "false", rules: []string{"RPC_REQUEST_RESPONSE_UNIQUE"}},
	"rpc_allow_google_protobuf_empty_requests":  {def: "false", rules: []string{"RPC_REQUEST_RESPONSE_UNIQUE", "RPC_REQUEST_STANDARD_NAME"}},
	"rpc_allow_google_protobuf_empty_responses": {def: "false", rules: []string{"RPC_REQUEST_RESPONSE_UNIQUE", "RPC_RESPONSE_STANDARD_NAME"}},
	"ignore_unstable_packages":                  {def: "false"},
}

// variant reports whether a rule id or tag is one of the ruleset's
// variants — a name buf never spelled, which no buf.yaml names: the
// `_STABLE` reading of a breaking id or tag the ruleset declares, or
// the `_ALLOW_...` reading of a lint rule it declares (the ruleset's
// naming of its variants); a buf id merely spelling `_ALLOW_`, as
// ENUM_NO_ALLOW_ALIAS does, is none.
func variant(s string) bool {
	if base, ok := strings.CutSuffix(s, "_STABLE"); ok {
		_, rule := rulesetRules[base]
		return rule || rulesetTags[check.KindBreaking][base]
	}
	if base, _, ok := strings.Cut(s, "_ALLOW_"); ok {
		r, rule := rulesetRules[base]
		return rule && r.kind == check.KindLint
	}
	return false
}

// isTrue and isFalse read a boolean option in the spellings buf's
// YAML decoder reads as a boolean: YAML 1.1's, y, yes, on, true and
// their opposites, in the cases it takes.
func isTrue(v string) bool {
	switch v {
	case "y", "Y", "yes", "Yes", "YES", "on", "On", "ON", "true", "True", "TRUE":
		return true
	}
	return false
}

func isFalse(v string) bool {
	switch v {
	case "n", "N", "no", "No", "NO", "off", "Off", "OFF", "false", "False", "FALSE":
		return true
	}
	return false
}

// honorsComments reports whether buf honored suppression comments
// under a lint section (REQ-migrate-comments): v1 only with
// allow_comment_ignores true, v2 unless disallow_comment_ignores is
// true.
func honorsComments(version string, sec *bufconfig.Section) bool {
	if version == "v1" {
		return sec != nil && isTrue(sec.Options["allow_comment_ignores"])
	}
	return sec == nil || !isTrue(sec.Options["disallow_comment_ignores"])
}

// selection is one module's enabled and excluded rules, qualified and
// sorted, and its ignore entries, as the lint file holds them; the
// root's, for a module declaring nothing of its own. enable is never
// nil: an empty one enables nothing, which the lint file spells `[]`,
// where nil would mean every rule.
type selection struct {
	enable, exclude []string
	ignores         []lintfile.Ignore
}

// same reports whether a selection is the root's with no ignores: a
// module of such a selection needs no entry.
func (s selection) same(root selection) bool {
	return slices.Equal(s.enable, root.enable) && slices.Equal(s.exclude, root.exclude) && len(s.ignores) == 0 && len(root.ignores) == 0
}

// kindConfig is one kind's section as buf reads it for one module:
// the section, the key path the report cites it by, the file's
// version, the module's directory relative to the section's paths
// ("." for v1, whose paths are module-relative), and whether the
// section is the module's own — a v2 module's, whose paths must lie
// within the module, or a v1 directory's file's — or the file's,
// shared.
type kindConfig struct {
	kind    check.Kind
	sec     *bufconfig.Section // nil where the file has none
	from    string
	version string
	dir     string
	own     bool
}

// empty reports whether a section says nothing, as buf's v2 reader
// tests a module's own section before letting it stand in for the
// file's: by value, an option spelled empty or false saying nothing.
func empty(s *bufconfig.Section) bool {
	if s == nil {
		return true
	}
	if len(s.Use) != 0 || len(s.Except) != 0 || len(s.Ignore) != 0 || len(s.IgnoreOnly) != 0 {
		return false
	}
	for _, v := range s.Options {
		if v != "" && !isFalse(v) {
			return false
		}
	}
	return true
}

// Rules builds the lint file (REQ-migrate-rules): the ruleset
// imported; the root's selection what buf applies to a module
// declaring no section of its own — a file's top-level sections,
// buf's defaults for a v1 workspace's directories — and, where the
// configuration declares several modules, an entry for each module
// whose own sections, or the top-level ignores lying within it, give
// it a selection or ignores of its own, the entry carrying its whole
// selection; a lone module's selection and ignores at the root. A
// category or id is spelled as the ruleset's qualified tag or rule
// name, DEFAULT read as STANDARD, one the ruleset lacks an unmapped
// fact; an ignore path becomes an entry over its subtree,
// module-relative, of the section's kind; an ignore equal to a
// module's directory disables the kind for that module, as buf reads
// it, no rule of the kind enabled for it; a top-level ignore in no
// module is unmapped. The rule-shaping options are mapped over the
// selection where set to anything but their default, a value-bearing
// one an unmapped fact, the comment switches mapped facts deciding
// whether the module's comments are rewritten
// (REQ-migrate-rule-options, REQ-migrate-comments). The file and the
// comment switches are set on the layout, the facts returned: the
// shared sections' first, each reported once and only where a module
// is under it.
func Rules(src *Source, l *Layout) ([]Fact, error) {
	if src == nil || l == nil {
		return nil, fmt.Errorf("no buf configuration read")
	}
	decls, err := declarations(src)
	if err != nil {
		return nil, err
	}
	several := len(decls) > 1
	// The root: the file's own sections where one file governs, buf's
	// defaults for a v1 workspace, whose directories each carry their
	// own file or none.
	root := selection{enable: []string{}}
	top := map[check.Kind]kindConfig{}
	topFacts := map[check.Kind][]Fact{}
	topUsed := map[check.Kind]bool{}
	var shared, own []Fact
	if src.Work == nil {
		for _, kind := range kinds {
			c := kindConfig{kind: kind, sec: sectionOf(src.File, kind), from: bufconfig.FileName + "." + string(kind), version: src.File.Version, dir: "."}
			top[kind] = c
			names, excluded, fs := useOf(c)
			names, excluded, _, ofs := optionMapping(c, names, excluded, true)
			topFacts[kind] = append(fs, ofs...)
			root.enable = append(root.enable, names...)
			root.exclude = append(root.exclude, excluded...)
		}
		// The file's shared ignores in no module, which buf skips, and
		// its ignore_only ids the ruleset lacks: reported once.
		shared = append(shared, sharedIgnoreFacts(top, decls)...)
	} else {
		for _, kind := range kinds {
			name, _ := nameOf(kind, bufDefaultUse(kind, "v1"))
			root.enable = append(root.enable, name)
		}
		// A buf.yaml beside the workspace file: buf reads the
		// directories' files alone.
		if src.File != nil && src.Members["."] != src.File {
			for _, kind := range kinds {
				if sectionOf(src.File, kind) != nil {
					shared = append(shared, mapped(bufconfig.FileName+"."+string(kind), "nothing: beside "+bufconfig.WorkFileName+", buf reads the directories' files alone"))
				}
			}
		}
	}
	canon(&root)
	f := &lintfile.File{Rulesets: []rules.Import{{Path: Ruleset, Version: l.RulesetVersion, Alias: RulesetAlias}}}
	if several {
		f.Modules = map[string]lintfile.ModuleSelection{}
	}
	l.CommentIgnores = map[string]bool{}
	for _, d := range decls {
		sel := selection{enable: []string{}}
		for _, kind := range kinds {
			cfg := configOf(d, kind, top, src.Work != nil)
			// The selection first, for how its options read a name;
			// then the ignores, a path equal to the module's directory
			// disabling the kind, the section's other keys then saying
			// nothing for this module, as buf reads it.
			var names, excluded []string
			var selected []Fact
			var readAs func(string) string
			if cfg.own {
				var fs, ofs []Fact
				names, excluded, fs = useOf(cfg)
				names, excluded, readAs, ofs = optionMapping(cfg, names, excluded, true)
				selected = append(fs, ofs...)
			} else {
				names, excluded = ofKind(root.enable, kind), ofKind(root.exclude, kind)
				// The root's options over this module's own ignores;
				// its selection carries them already.
				names, excluded, readAs, _ = optionMapping(cfg, names, excluded, false)
			}
			entries, disabled, fs, err := ignoresOf(cfg, d, several, readAs)
			if err != nil {
				return nil, err
			}
			own = append(own, fs...)
			if kind == check.KindLint {
				l.CommentIgnores[d.dir] = honorsComments(cfg.version, cfg.sec)
				if cfg.own {
					own = append(own, commentFacts(cfg)...)
				}
			}
			if disabled {
				continue
			}
			own = append(own, selected...)
			if !cfg.own {
				topUsed[kind] = true
			}
			sel.enable = append(sel.enable, names...)
			sel.exclude = append(sel.exclude, excluded...)
			sel.ignores = append(sel.ignores, entries...)
		}
		canon(&sel)
		if !several {
			root = sel
			break
		}
		if !sel.same(root) {
			f.Modules[d.dir] = lintfile.ModuleSelection{Enable: sel.enable, Exclude: sel.exclude, Ignore: sel.ignores}
		}
	}
	f.Enable, f.Exclude, f.Ignore = root.enable, root.exclude, root.ignores
	if _, err := lintfile.Encode(f); err != nil {
		return nil, fmt.Errorf("the lint file: %w", err)
	}
	l.Lint = f
	var facts []Fact
	for _, kind := range kinds {
		if topUsed[kind] {
			facts = append(facts, topFacts[kind]...)
			if kind == check.KindLint && src.Work == nil {
				facts = append(facts, commentFacts(top[kind])...)
			}
		}
	}
	facts = append(facts, shared...)
	return append(facts, own...), nil
}

// sectionOf is a file's section of the kind.
func sectionOf(f *bufconfig.File, kind check.Kind) *bufconfig.Section {
	if f == nil {
		return nil
	}
	if kind == check.KindBreaking {
		return f.Breaking
	}
	return f.Lint
}

// configOf is the kind's configuration governing a declared module:
// a v2 module's own section where it says anything, a v1 directory's
// file's, else the file's top-level, shared, read for this module.
func configOf(d declared, kind check.Kind, top map[check.Kind]kindConfig, work bool) kindConfig {
	if work {
		// A v1 directory's own file, its paths module-relative; a
		// directory with no file, or none of the kind, is under buf's
		// default, reported as such.
		return kindConfig{kind: kind, sec: sectionOf(d.file, kind), from: d.in + "." + string(kind), version: "v1", dir: ".", own: true}
	}
	sec := d.mod.Lint
	if kind == check.KindBreaking {
		sec = d.mod.Breaking
	}
	if d.version == "v2" && !empty(sec) {
		return kindConfig{kind: kind, sec: sec, from: d.from + "." + string(kind), version: "v2", dir: d.dir, own: true}
	}
	shared := top[kind]
	shared.dir = d.dir
	return shared
}

// useOf maps a section's use and except (REQ-migrate-rules): each
// entry a category as the ruleset's tag or an id as its rule, DEFAULT
// read as STANDARD, buf's default where use is absent, except read
// either way; an entry the ruleset lacks an unmapped fact.
func useOf(c kindConfig) (enable, exclude []string, facts []Fact) {
	var use, except []string
	if c.sec != nil {
		use, except = c.sec.Use, c.sec.Except
	}
	if len(use) == 0 {
		def := bufDefaultUse(c.kind, c.version)
		name, _ := nameOf(c.kind, def)
		why := "no use"
		if c.sec == nil {
			why = "no " + string(c.kind) + " section"
		}
		facts = append(facts, mapped(c.from, "enable: "+name+" (buf's default, "+why+")"))
		enable = []string{name}
	}
	for _, s := range use {
		name, ok := nameOf(c.kind, s)
		if !ok {
			facts = append(facts, unmapped(c.from+".use "+s, "no "+string(c.kind)+" rule or category of the ruleset "+Ruleset))
			continue
		}
		enable = append(enable, name)
		facts = append(facts, mapped(c.from+".use "+s, "enable: "+name+read(s)))
	}
	for _, s := range except {
		name, ok := nameOf(c.kind, s)
		if !ok {
			facts = append(facts, unmapped(c.from+".except "+s, "no "+string(c.kind)+" rule or category of the ruleset "+Ruleset))
			continue
		}
		exclude = append(exclude, name)
		facts = append(facts, mapped(c.from+".except "+s, "exclude: "+name+read(s)))
	}
	return enable, exclude, facts
}

// read is the note on a spelling the ruleset reads under another name.
func read(s string) string {
	if s == "DEFAULT" {
		return " (buf's DEFAULT is STANDARD)"
	}
	return ""
}

// nameOf is the ruleset's qualified name for a buf category or id of
// the kind: the tag, or the rule's name; false where the ruleset
// declares neither for the kind.
func nameOf(kind check.Kind, s string) (string, bool) {
	if s == "DEFAULT" && kind == check.KindLint {
		s = "STANDARD"
	}
	if variant(s) {
		return "", false
	}
	if r, ok := rulesetRules[s]; ok && r.kind == kind {
		return qualified(s), true
	}
	if rulesetTags[kind][s] {
		return qualified(s), true
	}
	return "", false
}

// qualified spells a tag or id under the ruleset's alias.
func qualified(s string) string { return RulesetAlias + ":" + s }

// ofKind is the qualified names of the list that are the kind's:
// tags and ids of its rules.
func ofKind(names []string, kind check.Kind) []string {
	var out []string
	for _, n := range names {
		s := strings.TrimPrefix(n, RulesetAlias+":")
		if r, ok := rulesetRules[s]; ok && r.kind == kind || rulesetTags[kind][s] {
			out = append(out, n)
		}
	}
	return out
}

// placement is what an ignore path is to a module: within it, made
// module-relative; its directory itself; or elsewhere, in another
// module or in none.
type placement int

const (
	within placement = iota
	directory
	elsewhere
)

// place decides a section's path for the module the config reads it
// for: cleaned as buf normalizes it (an escaping path buf's error),
// the module's directory, within it, or outside — refused where the
// section is the module's own, as buf refuses it, skipped where the
// section is shared.
func place(c kindConfig, spelled, key string) (rel string, at placement, err error) {
	p, err := rootpath.Clean(spelled, "the configuration's directory")
	if err != nil {
		return "", elsewhere, fmt.Errorf("%s: %w", key, err)
	}
	if p == c.dir {
		return "", directory, nil
	}
	if !rootpath.Contains(c.dir, p) {
		if c.own {
			return "", elsewhere, fmt.Errorf("%s: path %q is not contained within module directory %q", key, p, c.dir)
		}
		return "", elsewhere, nil
	}
	if c.dir == "." {
		return p, within, nil
	}
	return strings.TrimPrefix(p, c.dir+"/"), within, nil
}

// sharedIgnoreFacts reports, once, what the file's shared sections
// hold that no module takes: an ignore or ignore_only path in no
// module, which buf skips, and an ignore_only id the ruleset lacks.
func sharedIgnoreFacts(top map[check.Kind]kindConfig, decls []declared) []Fact {
	inModule := func(p string) bool {
		for _, d := range decls {
			if p == d.dir || rootpath.Contains(d.dir, p) {
				return true
			}
		}
		return false
	}
	var facts []Fact
	for _, kind := range kinds {
		c := top[kind]
		if c.sec == nil {
			continue
		}
		for _, spelled := range c.sec.Ignore {
			if p, err := rootpath.Clean(spelled, "the configuration's directory"); err == nil && !inModule(p) {
				facts = append(facts, unmapped(c.from+".ignore "+spelled, "lies in no module: buf skips it"))
			}
		}
		for _, id := range sortedKeys(c.sec.IgnoreOnly) {
			if _, ok := nameOf(kind, id); !ok {
				for _, spelled := range c.sec.IgnoreOnly[id] {
					facts = append(facts, unmapped(c.from+".ignore_only."+id+" "+spelled, "no "+string(kind)+" rule or category of the ruleset "+Ruleset))
				}
				continue
			}
			for _, spelled := range c.sec.IgnoreOnly[id] {
				if p, err := rootpath.Clean(spelled, "the configuration's directory"); err == nil && !inModule(p) {
					facts = append(facts, unmapped(c.from+".ignore_only."+id+" "+spelled, "lies in no module: buf skips it"))
				}
			}
		}
	}
	return facts
}

// ignoresOf maps a section's ignore and ignore_only for one module: a
// path equal to the module's directory disabling the kind, the
// section's other keys then read for nothing, as buf does; else each
// path within the module made module-relative and spelled as an
// entry over its subtree, of the kind, an ignore_only entry naming
// its rule or the rules its category tags, the module's directory
// under ignore_only every file of the module for the rule. A path
// outside the module is buf's error under the module's own section
// and skipped under a shared one, whose paths in no module Rules
// reports; a shared section's ignore_only id the ruleset lacks Rules
// reports too, an own section's here.
// A rule an ignore_only names is spelled as the selection's options
// read it (optionMapping's readAs): the variant standing in for it,
// its _STABLE reading.
func ignoresOf(c kindConfig, d declared, several bool, readAs func(string) string) (entries []lintfile.Ignore, disabled bool, facts []Fact, err error) {
	if c.sec == nil {
		return nil, false, nil, nil
	}
	where := "ignore"
	if several {
		where = "modules." + d.dir + ".ignore"
	}
	for _, spelled := range c.sec.Ignore {
		key := c.from + ".ignore " + spelled
		if _, at, err := place(c, spelled, key); err != nil {
			return nil, false, nil, err
		} else if at == directory {
			facts = append(facts, mapped(key, string(c.kind)+" disabled for "+d.dir+": no "+string(c.kind)+" rule enabled for it (the ignore is the module's directory, which buf reads as disabling the kind)"))
			return nil, true, facts, nil
		}
	}
	for _, spelled := range c.sec.Ignore {
		key := c.from + ".ignore " + spelled
		rel, at, err := place(c, spelled, key)
		if err != nil {
			return nil, false, nil, err
		}
		if at != within {
			continue
		}
		ig, err := ignoreEntry(glob.Quote(rel)+"/**", nil, c.kind)
		if err != nil {
			return nil, false, nil, fmt.Errorf("%s: %w", key, err)
		}
		entries = append(entries, ig)
		facts = append(facts, mapped(key, where+" paths ["+rel+"/**] kind "+string(c.kind)))
	}
	for _, id := range sortedKeys(c.sec.IgnoreOnly) {
		var rules []string
		if name, ok := nameOf(c.kind, id); ok {
			s := strings.TrimPrefix(name, RulesetAlias+":")
			if _, isRule := rulesetRules[s]; isRule {
				rules = []string{name}
			} else {
				for _, r := range rulesetTagged[c.kind][s] {
					rules = append(rules, qualified(r))
				}
			}
		}
		for _, spelled := range c.sec.IgnoreOnly[id] {
			key := c.from + ".ignore_only." + id + " " + spelled
			if rules == nil {
				if c.own {
					facts = append(facts, unmapped(key, "no "+string(c.kind)+" rule or category of the ruleset "+Ruleset))
				}
				continue
			}
			rel, at, err := place(c, spelled, key)
			if err != nil {
				return nil, false, nil, err
			}
			if at == elsewhere {
				continue
			}
			// The module's directory under ignore_only: buf reads it as
			// the whole module, which is every file, for the rule.
			spec := glob.Quote(rel) + "/**"
			if at == directory {
				spec = "**"
			}
			// A rule whose finding carries no position is reached by
			// `**` in a module's entry alone, the finding located at the
			// module's directory; under the root's selection a set
			// rule's finding has no path and a package rule's the
			// package's first file, which a path reaches.
			var reached []string
			for _, r := range rules {
				id := strings.TrimPrefix(r, RulesetAlias+":")
				switch {
				case !positionless(id):
					reached = append(reached, readAs(r))
				case several && at == directory:
					reached = append(reached, readAs(r))
				case !several && rulesetRules[id].target == check.TargetPackage:
					reached = append(reached, readAs(r))
				case !several:
					facts = append(facts, unmapped(key, r+" is a set rule, whose finding has no path under the root selection: no ignore reaches it"))
				default:
					facts = append(facts, unmapped(key, r+"'s finding is located at the module's directory, which a path reaches not: ignore the module's directory to reach it"))
				}
			}
			if len(reached) == 0 {
				continue
			}
			ig, err := ignoreEntry(spec, reached, "")
			if err != nil {
				return nil, false, nil, fmt.Errorf("%s: %w", key, err)
			}
			entries = append(entries, ig)
			facts = append(facts, mapped(key, where+" paths ["+spec+"] rules ["+strings.Join(reached, ", ")+"]"))
		}
	}
	return entries, false, facts, nil
}

// ignoreEntry is an ignore entry over one glob naming the rules
// given, every rule where nil, and the kind where given: an ignore
// names the section's kind, an ignore_only its rules, whose kind is
// theirs.
func ignoreEntry(spec string, rules []string, kind check.Kind) (lintfile.Ignore, error) {
	g, err := glob.Compile(spec)
	if err != nil {
		return lintfile.Ignore{}, err
	}
	return lintfile.Ignore{Paths: []*glob.Pattern{g}, Rules: rules, Kind: kind}, nil
}

// set reports whether a rule-shaping option is set to anything but
// its default: a value spelled empty is buf's default too, its reader
// filling the suffixes from the defaults where empty.
func (c kindConfig) set(option string) bool {
	if c.sec == nil {
		return false
	}
	v, ok := c.sec.Options[option]
	o := ruleOptions[option]
	return ok && v != "" && v != o.def && !(o.def == "false" && isFalse(v))
}

// stable is a breaking name read as its variant over stable packages
// alone, the name of one already so read kept.
func stable(name string) string {
	if strings.HasSuffix(name, "_STABLE") {
		return name
	}
	return name + "_STABLE"
}

// nothing is the mapped fact of an option shaping a rule the
// selection does not enable.
func nothing(key, rule string) Fact {
	return mapped(key, "nothing: "+rule+" is not enabled, so the option shapes nothing")
}

// celString escapes a value for a single-quoted CEL string literal.
var celString = strings.NewReplacer(`\`, `\\`, `'`, `\'`)

// selects reports whether a selection enables the ruleset's rule by
// its bare id: named, or carried by a tag named, and not excluded.
func selects(kind check.Kind, enable, exclude []string, id string) bool {
	covers := func(names []string) bool {
		for _, n := range names {
			s := strings.TrimPrefix(n, RulesetAlias+":")
			if s == id {
				return true
			}
			for _, r := range rulesetTagged[kind][s] {
				if r == id {
					return true
				}
			}
		}
		return false
	}
	return covers(enable) && !covers(exclude)
}

// optionMapping maps the section's rule-shaping options
// (REQ-migrate-rule-options) over a selection: a boolean option set
// excludes buf's rule and enables the variant reading the option,
// where the selection enables the rule; `ignore_unstable_packages`
// reads every breaking name enabled or excluded as its variant over
// stable packages; a value-bearing option is an unmapped fact naming
// the one-line rule a workspace ruleset declares over the ruleset's
// function. readAs spells a qualified rule name as the mapped
// selection reads it — the variant standing in for it, its _STABLE
// reading — for the ignores naming it. Idempotent, so a shared
// selection mapped once is mapped no further; facts are reported
// where report says.
func optionMapping(c kindConfig, enable, exclude []string, report bool) ([]string, []string, func(string) string, []Fact) {
	var facts []Fact
	note := func(f Fact) {
		if report {
			facts = append(facts, f)
		}
	}
	renames := map[string]string{}
	unstable := false
	readAs := func(name string) string {
		if v, ok := renames[name]; ok {
			name = v
		}
		if unstable {
			name = stable(name)
		}
		return name
	}
	if c.sec == nil {
		return enable, exclude, readAs, nil
	}
	key := func(k string) string { return c.from + "." + k + " " + c.sec.Options[k] }
	for _, k := range sortedKeys(c.sec.Options) {
		if k == "allow_comment_ignores" || k == "disallow_comment_ignores" {
			continue
		}
		if _, known := ruleOptions[k]; !known {
			note(unmapped(key(k), "an option the migration does not model"))
		}
	}
	// A variant in place of the rule it reads: excluded and enabled
	// where the selection enables the rule, and read for the rule by
	// an ignore naming it, so buf's suppression carries over — in a
	// module carrying a selection the variant already stands in too.
	stands := func(keys []string, base, variantID string) {
		b, v := qualified(base), qualified(variantID)
		enabled, already := selects(c.kind, enable, exclude, base), slices.Contains(enable, v)
		if !enabled && !already {
			for _, k := range keys {
				note(nothing(key(k), b))
			}
			return
		}
		if enabled {
			exclude = append(exclude, b)
			enable = append(enable, v)
			for _, k := range keys {
				note(mapped(key(k), "exclude: "+b+", enable: "+v))
			}
		}
		renames[b] = v
	}
	switch c.kind {
	case check.KindLint:
		same, reqs, resps := c.set("rpc_allow_same_request_response"), c.set("rpc_allow_google_protobuf_empty_requests"), c.set("rpc_allow_google_protobuf_empty_responses")
		if same || reqs || resps {
			id := "RPC_REQUEST_RESPONSE_UNIQUE_ALLOW"
			var keys []string
			if same {
				id += "_SAME"
				keys = append(keys, "rpc_allow_same_request_response")
			}
			if reqs || resps {
				id += "_EMPTY"
			}
			if reqs {
				id += "_REQUESTS"
				keys = append(keys, "rpc_allow_google_protobuf_empty_requests")
			}
			if resps {
				id += "_RESPONSES"
				keys = append(keys, "rpc_allow_google_protobuf_empty_responses")
			}
			stands(keys, "RPC_REQUEST_RESPONSE_UNIQUE", id)
		}
		if reqs {
			stands([]string{"rpc_allow_google_protobuf_empty_requests"}, "RPC_REQUEST_STANDARD_NAME", "RPC_REQUEST_STANDARD_NAME_ALLOW_EMPTY")
		}
		if resps {
			stands([]string{"rpc_allow_google_protobuf_empty_responses"}, "RPC_RESPONSE_STANDARD_NAME", "RPC_RESPONSE_STANDARD_NAME_ALLOW_EMPTY")
		}
		// A value-bearing option: the recipe standing in for it where
		// the selection enables the rule, the value spelled as a CEL
		// string.
		for _, option := range sortedKeys(ruleOptions) {
			o := ruleOptions[option]
			if o.fn == "" || !c.set(option) {
				continue
			}
			rule := qualified(o.rules[0])
			if !selects(c.kind, enable, exclude, o.rules[0]) {
				note(nothing(key(option), rule))
				continue
			}
			v := celString.Replace(c.sec.Options[option])
			note(unmapped(key(option), "reshapes "+rule+", which checks "+o.def+": a pb rule has no parameters — exclude "+rule+" and declare, in a workspace ruleset importing "+Ruleset+" as "+RulesetAlias+", a rule over "+o.target+" with cel "+RulesetAlias+"."+o.fn+"("+o.binding+", '"+v+"')"))
		}
	case check.KindBreaking:
		if c.set("ignore_unstable_packages") {
			unstable = true
			for i, n := range enable {
				enable[i] = stable(n)
			}
			for i, n := range exclude {
				exclude[i] = stable(n)
			}
			note(mapped(key("ignore_unstable_packages"), "every breaking name enabled, excluded or ignored read as its _STABLE variant, which skips a package with an unstable version suffix"))
		}
	}
	return enable, exclude, readAs, facts
}

// commentFacts is the lint section's comment switch as a mapped
// fact: whether buf honored suppression comments under it, and so
// whether the verb rewrites them (REQ-migrate-comments). A v1
// section without the switch honored none, a fact all the same; a v2
// section without it honored them, which the per-file report tells.
func commentFacts(c kindConfig) []Fact {
	if c.kind != check.KindLint {
		return nil
	}
	rewritten := "the module's suppression comments are rewritten to pb:ignore"
	kept := "the module's suppression comments are left as they are: buf honored none"
	key, spelled := "allow_comment_ignores", ""
	if c.version != "v1" {
		key = "disallow_comment_ignores"
	}
	if c.sec != nil {
		spelled = c.sec.Options[key]
	}
	honored := honorsComments(c.version, c.sec)
	text := kept
	if honored {
		text = rewritten
	}
	if spelled != "" {
		return []Fact{mapped(c.from+"."+key+" "+spelled, text)}
	}
	if c.version == "v1" {
		return []Fact{mapped(c.from, kept+" without "+key)}
	}
	return nil
}

// canon sorts and dedupes a selection's lists, an empty exclude nil
// and an empty enable kept empty: the two spell differently in the
// lint file.
func canon(s *selection) {
	slices.Sort(s.enable)
	s.enable = slices.Compact(s.enable)
	slices.Sort(s.exclude)
	s.exclude = slices.Compact(s.exclude)
	if len(s.exclude) == 0 {
		s.exclude = nil
	}
}

// sortedKeys is a map's keys in raw-byte order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
