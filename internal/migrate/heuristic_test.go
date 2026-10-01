package migrate

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/goccy/go-yaml"
	"github.com/greatliontech/glob"
	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/check/lintfile"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/module/workspace"
	"github.com/greatliontech/pb/internal/plugin/genfile"
	"pgregory.net/rapid"
)

// The verb synthesizes no value it was not given
// (REQ-migrate-no-heuristic): over generated buf configurations whose
// scalars are marker tokens no table holds, each marker bound to the
// role it was drawn for, every value of every file the verb writes is
// one given for that role — a module's path the flag's, or the flag's
// under the module's directory; a dependency's path a table's, a
// closure's, a replacement's or the ruleset's, its version the one
// discovery or the flag gave that path; a rule the qualified spelling
// of a category or id the input named or buf's default, or the
// variant a boolean rule-shaping option the input set names — the
// uniqueness variant named for the allowances set together, the
// `_STABLE` reading of a breaking name where a section ignores
// unstable packages — beside the rule it stands in for; an ignore
// the subtree of a path the input named under its module; a plugin
// reference the flag's or the catalog's at the version the input
// named, or at the highest tag the registry listed where it named
// none; an override's value and path the input's. Containment by
// role, no oracle: a module path made from a BSR name, a dependency
// guessed for an unknown name, a version taken from another path, a
// plugin at a version neither named nor listed, a computed option
// declared as a value, each fails it. A BSR name's tokens, buf's computed-option
// values, a rule-shaping option's value and an ignore outside every
// module reach no written file; a marker the input never drew
// appears in no written file and no line of the report; every
// module's file and the lint file are checked each run, so the
// property is never vacuous.
//
// The grammar spans v2 files alone: a v1 buf.yaml, a buf.work.yaml
// workspace, v1's generation forms and managed-mode option forms,
// excludes and includes, opt lists, a local plugin with arguments, a
// --dep over a table name, and the module path derived from the
// repository's origin remote are not generated here — each pinned by
// the steps' example tests, none a source of a value beyond those the
// roles here admit.
func TestNoHeuristicProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		g := newGiven()
		g.modulePath = g.draw(rt, "h") + "." + g.draw(rt, "h") + "/" + g.draw(rt, "s")
		var b strings.Builder
		b.WriteString("version: v2\n")
		n := rapid.IntRange(1, 3).Draw(rt, "modules")
		if n == 1 && rapid.Bool().Draw(rt, "at root") {
			g.dirs = []string{"."}
		} else {
			for len(g.dirs) < n {
				if d := g.draw(rt, "d"); !contains(g.dirs, d) {
					g.dirs = append(g.dirs, d)
				}
			}
		}
		b.WriteString("modules:\n")
		for i, d := range g.dirs {
			fmt.Fprintf(&b, "  - path: %s\n", d)
			if rapid.Bool().Draw(rt, "name") {
				owner, mod := g.draw(rt, "own"), fmt.Sprintf("mod%d", i)
				g.used[mod] = true
				g.forbidden[owner], g.forbidden[mod] = true, true // a BSR name is no place pb fetches from
				fmt.Fprintf(&b, "    name: buf.build/%s/%s\n", owner, mod)
			}
			if rapid.Bool().Draw(rt, "own lint") {
				g.section(rt, &b, "    ", "lint", d)
			}
			if rapid.Bool().Draw(rt, "own breaking") {
				g.section(rt, &b, "    ", "breaking", d)
			}
		}
		// Dependencies: table names, their closures with them, and
		// marker names a --dep may map.
		var deps []string
		repl := Replacements{}
		latest := map[string]string{Ruleset: g.version(rt)}
		g.versions[Ruleset] = latest[Ruleset]
		for range rapid.IntRange(0, 2).Draw(rt, "table deps") {
			name := rapid.SampledFrom(sortedKeys(Dependencies)).Draw(rt, "dep")
			if contains(deps, name) {
				continue
			}
			deps = append(deps, name)
			for _, n := range closure(name) {
				p := Dependencies[n].Path
				if _, done := latest[p]; !done {
					latest[p] = g.version(rt)
				}
				g.depPaths[p], g.versions[p] = true, latest[p]
			}
		}
		for range rapid.IntRange(0, 2).Draw(rt, "marker deps") {
			owner, mod := g.draw(rt, "own"), g.draw(rt, "own")
			g.forbidden[owner], g.forbidden[mod] = true, true
			name := "buf.build/" + owner + "/" + mod
			if contains(deps, name) {
				continue
			}
			deps = append(deps, name)
			target := g.draw(rt, "tg") + "." + g.draw(rt, "tg") + "/" + g.draw(rt, "tg")
			if rapid.Bool().Draw(rt, "replaced") && !g.depPaths[target] { // one path, one version
				value := name + "=" + target
				if rapid.Bool().Draw(rt, "versioned") {
					v := g.version(rt)
					value += "@" + v
					g.versions[target] = v
				} else {
					latest[target] = g.version(rt)
					g.versions[target] = latest[target]
				}
				g.depPaths[target] = true
				if err := repl.Replace("dep", value); err != nil {
					rt.Fatal(err)
				}
			}
		}
		if len(deps) > 0 {
			b.WriteString("deps:\n")
			for _, d := range deps {
				if rapid.Bool().Draw(rt, "ref") {
					d += ":" + g.draw(rt, "rf")
				}
				fmt.Fprintf(&b, "  - %s\n", d)
			}
		}
		if rapid.Bool().Draw(rt, "file lint") {
			g.section(rt, &b, "", "lint", g.dirs[0])
		}
		if rapid.Bool().Draw(rt, "file breaking") {
			g.section(rt, &b, "", "breaking", g.dirs[0])
		}
		if rapid.Bool().Draw(rt, "unmodeled") {
			fmt.Fprintf(&b, "%s: %s\n", g.draw(rt, "ky"), g.draw(rt, "uv"))
		}
		// The configuration at the root, or below it under --config:
		// every buf file and proto lies under it, pb's files at the
		// root (REQ-migrate-verb).
		if rapid.Bool().Draw(rt, "config below root") {
			g.config = "cfg" + g.draw(rt, "cf")
		}
		cfg := path.Join("repo", g.config)
		files := map[string]string{path.Join(cfg, "buf.yaml"): b.String()}
		if len(deps) > 0 && rapid.Bool().Draw(rt, "lock") {
			var lb strings.Builder
			lb.WriteString("version: v2\ndeps:\n")
			for _, d := range deps {
				fmt.Fprintf(&lb, "  - name: %s\n    commit: %s\n    digest: b5:%s\n", d, g.draw(rt, "c"), g.draw(rt, "dg"))
			}
			g.used["b5"] = true
			files[path.Join(cfg, "buf.lock")] = lb.String()
		}
		hasGen := rapid.Bool().Draw(rt, "gen")
		if hasGen {
			files[path.Join(cfg, "buf.gen.yaml")] = g.gen(rt, &repl, true, false)
		}
		// A template beside the file, or alone: its entries follow
		// the file's, its inputs its own; its managed mode and clean
		// are held to the first file's — itself, where no
		// buf.gen.yaml lies there.
		if rapid.IntRange(0, 2).Draw(rt, "template") == 0 {
			files[path.Join(cfg, "buf.gen."+g.draw(rt, "tp")+".yaml")] = g.gen(rt, &repl, !hasGen, true)
		}
		for p, text := range g.extra {
			files[p] = text
		}
		for i, d := range g.dirs {
			name := g.draw(rt, "pr") + ".proto"
			body := "syntax = \"proto3\";\n// buf:lint:ignore " + g.draw(rt, "id") + "\nmessage " + g.draw(rt, "ms") + " {}\n"
			// A sibling's file imported, sometimes: the sibling declared
			// at the version discovery names for its path; a bundled
			// import, sometimes: its provider declared the same way
			// (REQ-migrate-imports).
			if i > 0 && rapid.Bool().Draw(rt, "sibling import") {
				// The file named for its sibling, so no two siblings
				// provide one path, which pb never picks between.
				sibling := g.dirs[i-1]
				shared := "shared_" + strings.ReplaceAll(sibling, "/", "_") + ".proto"
				files[path.Join(cfg, sibling, shared)] = "syntax = \"proto3\";\n"
				body = "syntax = \"proto3\";\nimport \"" + shared + "\";\n" + body[len("syntax = \"proto3\";\n"):]
				sp := g.modulePath
				if sibling != "." {
					sp += "/" + sibling
				}
				latest[sp] = g.version(rt)
				g.depPaths[sp], g.versions[sp] = true, latest[sp]
				g.requires[d] = append(g.requires[d], sp)
			}
			if rapid.Bool().Draw(rt, "bundled import") {
				body = "syntax = \"proto3\";\nimport \"google/protobuf/go_features.proto\";\n" + body[len("syntax = \"proto3\";\n"):]
				for _, p := range BundledImports {
					if _, done := latest[p]; !done {
						latest[p] = g.version(rt)
					}
					g.depPaths[p], g.versions[p] = true, latest[p]
					g.requires[d] = append(g.requires[d], p)
				}
			}
			files[path.Join(cfg, d, name)] = body
		}

		ws := memfs.New()
		for p, text := range files {
			if err := util.WriteFile(ws, p, []byte(text), 0o644); err != nil {
				rt.Fatal(err)
			}
		}
		var out strings.Builder
		err := Run(context.Background(), Invocation{
			WS: ws, Dir: "repo", Config: g.config, ModulePath: g.modulePath, Replacements: repl,
			Discovery: &anyDiscovery{latest: latest}, PluginTags: anyTags,
			Tidy: func(context.Context) error { return nil },
			Out:  &out,
		})
		if err != nil && !errors.Is(err, ErrUnmapped) {
			rt.Fatalf("Run over\n%s\n%v", files[path.Join(cfg, "buf.yaml")], err)
		}

		read := func(name string) (map[string]any, []byte, bool) {
			data, err := util.ReadFile(ws, path.Join("repo", name))
			if err != nil {
				return nil, nil, false
			}
			var doc map[string]any
			if err := yaml.Unmarshal(data, &doc); err != nil {
				rt.Fatalf("%s: %v\n%s", name, err, data)
			}
			g.whole(rt, name, data)
			return doc, data, true
		}
		fail := func(name string, data []byte, format string, args ...any) {
			rt.Fatalf("%s: %s\n%s\nfrom\n%s", name, fmt.Sprintf(format, args...), data, files[path.Join(cfg, "buf.yaml")])
		}
		checked := 0
		for _, d := range g.dirs {
			name := path.Join(g.rooted(d), module.ModuleFileName)
			doc, data, ok := read(name)
			if !ok {
				continue
			}
			checked++
			want := g.modulePath
			if d != "." {
				want += "/" + d
			}
			if doc["module"] != want {
				fail(name, data, "module %v, given %s", doc["module"], want)
			}
			for p, v := range mapping(doc["deps"]) {
				if !g.depPaths[p] {
					fail(name, data, "dependency %s given nowhere", p)
				}
				if fmt.Sprint(v) != g.versions[p] {
					fail(name, data, "dependency %s at %v, given %s", p, v, g.versions[p])
				}
			}
			// What the module's file imports of a sibling or a bundled
			// import is declared (REQ-migrate-imports).
			for _, p := range g.requires[d] {
				if _, declared := mapping(doc["deps"])[p]; !declared {
					fail(name, data, "the file imports %s's file, undeclared", p)
				}
			}
		}
		if doc, data, ok := read(workspace.FileName); ok {
			for _, u := range list(doc["use"]) {
				if !contains(g.rootedDirs(), u) {
					fail(workspace.FileName, data, "use %s given nowhere", u)
				}
			}
		}
		if doc, data, ok := read(lintfile.FileName); ok {
			checked++
			// One import: the ruleset at the discovered version under
			// its alias (REQ-migrate-rules).
			rs := list2(doc["rulesets"])
			// A second, the first module as the ruleset holding the
			// local rules, where a value-bearing option gave any: the
			// rule file's rules then each carry a value given.
			ruleFile, ruleData, hasRuleFile := read(path.Join(g.rooted(g.dirs[0]), RuleFileName))
			wantImports := 1
			if hasRuleFile {
				wantImports = 2
			}
			if len(rs) != wantImports {
				fail(lintfile.FileName, data, "rulesets %v", rs)
			}
			if imp := mapping(rs[0]); text(imp["path"]) != Ruleset || text(imp["version"]) != g.versions[Ruleset] || text(imp["alias"]) != RulesetAlias {
				fail(lintfile.FileName, data, "the ruleset import %v: given %s at %s", imp, Ruleset, g.versions[Ruleset])
			}
			if hasRuleFile {
				want := g.modulePath
				if g.dirs[0] != "." {
					want += "/" + g.dirs[0]
				}
				if imp := mapping(rs[1]); text(imp["path"]) != want || text(imp["alias"]) != LocalRulesetAlias || text(imp["version"]) != "" {
					fail(lintfile.FileName, data, "the local ruleset import %v: given %s", imp, want)
				}
				if len(list2(ruleFile["rules"])) == 0 {
					fail(RuleFileName, ruleData, "a rule file with no rule")
				}
				for _, r := range list2(ruleFile["rules"]) {
					rule := mapping(r)
					value, given := g.valued[text(rule["id"])]
					if !given || !strings.Contains(text(rule["cel"]), "'"+celString.Replace(value)+"'") || !strings.Contains(text(rule["message"]), value) {
						fail(RuleFileName, ruleData, "rule %v given nowhere", rule)
					}
				}
			}
			g.selection(rt, lintfile.FileName, data, doc, "")
			for dir, sel := range mapping(doc["modules"]) {
				at := indexOf(g.rootedDirs(), dir)
				if at < 0 {
					fail(lintfile.FileName, data, "module %s given nowhere", dir)
				}
				g.selection(rt, lintfile.FileName, data, mapping(sel), g.dirs[at])
			}
		}
		if doc, data, ok := read(genfile.FileName); ok {
			if c, has := doc["clean"]; has && (!g.clean || fmt.Sprint(c) != "true") {
				fail(genfile.FileName, data, "clean %v given nowhere", c)
			}
			for _, p := range list2(doc["plugins"]) {
				pl := mapping(p)
				if ref, has := pl["ref"]; has && !g.refs[fmt.Sprint(ref)] {
					fail(genfile.FileName, data, "ref %v given nowhere", ref)
				}
				// A local is its command, or a list of the command and
				// its arguments, given exactly so: the list's spelling
				// with spaces is the local drawn, in its order.
				switch l := pl["local"].(type) {
				case nil:
				case []any:
					var elems []string
					for _, e := range l {
						elems = append(elems, fmt.Sprint(e))
					}
					if !g.locals[strings.Join(elems, " ")] {
						fail(genfile.FileName, data, "local %v given nowhere", l)
					}
				default:
					if !g.locals[fmt.Sprint(l)] {
						fail(genfile.FileName, data, "local %v given nowhere", l)
					}
				}
				if !g.outs[fmt.Sprint(pl["out"])] {
					fail(genfile.FileName, data, "out %v given nowhere", pl["out"])
				}
				for _, pat := range list2(pl["files"]) {
					if !g.patterns[fmt.Sprint(pat)] {
						fail(genfile.FileName, data, "files pattern %v given nowhere", pat)
					}
				}
				if ii, has := pl["include_imports"]; has && fmt.Sprint(ii) != "true" {
					fail(genfile.FileName, data, "include_imports %v: only a true one is written", ii)
				}
				if o, has := pl["opt"]; has && !g.opts[fmt.Sprint(o)] {
					fail(genfile.FileName, data, "opt %v given nowhere", o)
				}
			}
			for _, o := range list2(doc["overrides"]) {
				ov := mapping(o)
				switch ov["option"] {
				case "java_package":
					if v, has := ov["value"]; has && !g.overrideValues[fmt.Sprint(v)] {
						fail(genfile.FileName, data, "override value %v given nowhere", v)
					}
					if sfx, has := ov["suffix"]; has && !g.suffixes[fmt.Sprint(sfx)] {
						fail(genfile.FileName, data, "override suffix %v given nowhere", sfx)
					}
					// buf's starting prefix, com, where no rule of the
					// files cleared or replaced it.
					if p, has := ov["prefix"]; has && fmt.Sprint(p) != "com" && !g.javaPrefixes[fmt.Sprint(p)] {
						fail(genfile.FileName, data, "java_package prefix %v given nowhere", p)
					}
				case "go_package":
					if _, has := ov["value"]; has || !g.prefixes[fmt.Sprint(ov["prefix"])] {
						fail(genfile.FileName, data, "override go_package %v: a prefix given nowhere, or a value", ov)
					}
				default:
					// buf's defaults under managed mode are the spec's
					// constants (REQ-migrate-no-heuristic): each an
					// override over every file, as the spec spells it,
					// and nothing else.
					isDefault := false
					for _, d := range managedDefaults {
						want := defaultOverride(d)
						if want.Option == ov["option"] && text(ov["files"]) == "**" && text(ov["value"]) == want.Value && text(ov["prefix"]) == want.Prefix && text(ov["suffix"]) == want.Suffix {
							isDefault = true
						}
					}
					if !g.managed || !isDefault {
						fail(genfile.FileName, data, "override option %v given nowhere", ov["option"])
					}
				}
				if f := fmt.Sprint(ov["files"]); f != "**" && !g.overridePaths[strings.TrimSuffix(f, "/**")] {
					fail(genfile.FileName, data, "override files %v given nowhere", f)
				}
			}
			// Differential: buf reads each file's option from the rules
			// matching it in order — a prefix rule keeping the suffix, a
			// suffix rule the prefix, a value clearing both, java_package
			// starting from the prefix com, buf's default, which under
			// managed mode reaches every file as an override of the
			// spec's — and the written file's last matching override
			// must say the same, none where no rule matched and no
			// default stands.
			var samples []string
			for sc := range g.overridePaths {
				samples = append(samples, sc+"/x.proto", sc+"way/x.proto", sc+"/deep/x.proto")
			}
			for _, file := range append(samples, "x.proto") {
				for _, option := range []string{"java_package", "go_package"} {
					var buf optionState
					matched := false
					if option == "java_package" {
						buf.prefix = "com"
						matched = g.managed
					}
					for _, r := range g.rules {
						if r.option != option || !(r.scope == "" || file == r.scope || strings.HasPrefix(file, r.scope+"/")) {
							continue
						}
						matched = true
						switch r.axis {
						case "value":
							buf = optionState{value: r.value}
						case "prefix":
							buf = optionState{prefix: r.value, suffix: buf.suffix}
						case "suffix":
							buf = optionState{prefix: buf.prefix, suffix: r.value}
						}
					}
					var pb optionState
					found := false
					for _, o := range list2(doc["overrides"]) {
						ov := mapping(o)
						if fmt.Sprint(ov["option"]) != option {
							continue
						}
						pat, err := glob.Compile(fmt.Sprint(ov["files"]))
						if err != nil {
							fail(genfile.FileName, data, "override files %v: %v", ov["files"], err)
						}
						if !pat.Match(file) {
							continue
						}
						found = true
						pb = optionState{value: text(ov["value"]), prefix: text(ov["prefix"]), suffix: text(ov["suffix"])}
					}
					if matched != found || buf != pb && matched {
						fail(genfile.FileName, data, "%s of %s: buf reads %+v (a rule matched: %v), pb gives %+v (an override matched: %v)", option, file, buf, matched, pb, found)
					}
				}
			}
		}
		g.whole(rt, "the report", []byte(out.String()))
		// Never vacuous: every module's file and the lint file are written.
		if checked < len(g.dirs)+1 {
			rt.Fatalf("%d files checked of %d modules", checked, len(g.dirs))
		}
	})
}

// anyDiscovery answers the version given for a path, and for a path
// given none a version of its own: a dependency the verb guessed a
// path for is then declared rather than left undiscovered, and the
// witness sees it.
type anyDiscovery struct{ latest map[string]string }

func (a *anyDiscovery) Versions(context.Context, string) ([]version.Version, error) {
	return nil, errors.New("the deps step asks for the latest, never the listing")
}

func (a *anyDiscovery) Latest(_ context.Context, path string) (version.Version, error) {
	if s, ok := a.latest[path]; ok {
		return version.Parse(s)
	}
	return version.Parse("v7.7.7")
}

// anyTags lists every plugin repository's tags as the same three, the
// highest version among them v8.8.8.
func anyTags(context.Context, string) ([]string, error) {
	return []string{"v8.8.8", "v8.8.10-rc1", "sha256-ab.sig", "v8.8.7"}, nil
}

// given is what the input gave, by role.
// givenRule is one managed-mode rule the input gave: an option's
// value, prefix or suffix over every file or a path.
type givenRule struct {
	option, axis, value, scope string
}

// optionState is buf's reading of one option for one file: a value,
// or a prefix and suffix.
type optionState struct {
	value, prefix, suffix string
}

type given struct {
	rules          []givenRule // buf.gen.yaml's managed rules in order
	modulePath     string
	dirs           []string
	depPaths       map[string]bool   // a table's, a closure's, a replacement's, the ruleset's
	versions       map[string]string // path -> the version discovery or the flag gave it
	entries        map[string]bool   // bare categories and ids the sections named, buf's defaults, and the variants the options set name
	stable         map[string]bool   // breaking sections ignoring unstable packages, the file's under "top", a module's own under its dir: their names read as _STABLE variants
	ownBreaking    map[string]bool   // modules with a breaking section of their own
	ignores        map[string]bool   // module-relative ignore paths under their module
	refs           map[string]bool   // plugin references the flag or the table gave
	patterns       map[string]bool   // files patterns the inputs' paths gave
	extra          map[string]string // files the inputs' paths name, written with the tree
	locals         map[string]bool
	outs, opts     map[string]bool
	overrideValues map[string]bool
	managed        bool                // buf.gen.yaml's managed mode enabled, its defaults then the spec's constants
	config         string              // the configuration's directory below the root, "" for the root
	valued         map[string]string   // the local rules' ids the value-bearing options give, <RULE>_<value>, to the value
	requires       map[string][]string // per module directory, the providers its file imports
	prefixes       map[string]bool     // go_package prefixes the managed mode gave
	suffixes       map[string]bool     // java_package suffixes the managed mode gave
	javaPrefixes   map[string]bool     // java_package prefixes the managed mode gave
	overridePaths  map[string]bool
	clean          bool            // the first file's clean
	replaced       map[string]bool // catalog plugins a --plugin replacement names
	used           map[string]bool // every marker drawn
	forbidden      map[string]bool // markers no written file may hold
}

func newGiven() *given {
	return &given{
		requires: map[string][]string{},
		valued:   map[string]string{},
		depPaths: map[string]bool{}, versions: map[string]string{}, entries: map[string]bool{"STANDARD": true, "FILE": true},
		ignores: map[string]bool{}, refs: map[string]bool{}, patterns: map[string]bool{}, extra: map[string]string{}, locals: map[string]bool{}, outs: map[string]bool{}, opts: map[string]bool{},
		overrideValues: map[string]bool{}, prefixes: map[string]bool{}, suffixes: map[string]bool{}, javaPrefixes: map[string]bool{}, overridePaths: map[string]bool{}, used: map[string]bool{}, forbidden: map[string]bool{},
		stable: map[string]bool{}, ownBreaking: map[string]bool{},
	}
}

// markerForm is the alphabet: a role's letters and a number.
var markerForm = regexp.MustCompile(`^[a-z]{1,3}[0-9]+$`)

// draw draws a marker of the role and records it.
func (g *given) draw(rt *rapid.T, role string) string {
	m := fmt.Sprintf("%s%d", role, rapid.IntRange(0, 7).Draw(rt, role))
	g.used[m] = true
	return m
}

// version draws a release version and records it.
func (g *given) version(rt *rapid.T) string {
	v := fmt.Sprintf("v%d.%d.%d", rapid.IntRange(0, 3).Draw(rt, "major"), rapid.IntRange(0, 3).Draw(rt, "minor"), rapid.IntRange(0, 9).Draw(rt, "patch"))
	g.used[v] = true
	return v
}

// whole checks a text as a whole: no marker of the alphabet the input
// never drew, and, in a written file, no forbidden marker, stands in
// it.
func (g *given) whole(rt *rapid.T, name string, data []byte) {
	for _, tok := range tokens(string(data)) {
		if g.forbidden[tok] && name != "the report" {
			rt.Fatalf("%s holds %q, which reaches no written file:\n%s", name, tok, data)
		}
		if markerForm.MatchString(tok) && !g.used[tok] {
			rt.Fatalf("%s holds the marker %q the input never drew:\n%s", name, tok, data)
		}
	}
}

// selection checks one selection of the lint file — the root's, or a
// module's entry under its dir: enable and exclude the ruleset's
// qualified spellings of named entries, ignores over named paths,
// kinds pb's.
func (g *given) selection(rt *rapid.T, name string, data []byte, sel map[string]any, dir string) {
	for _, key := range []string{"enable", "exclude"} {
		for _, e := range list(sel[key]) {
			// A local rule: enabled under its alias, its id the rule
			// and the value given.
			if id, ok := strings.CutPrefix(e, LocalRulesetAlias+":"); ok {
				if _, given := g.valued[id]; key != "enable" || !given {
					rt.Fatalf("%s: %s %q given nowhere\n%s", name, key, e, data)
				}
				continue
			}
			bare, ok := strings.CutPrefix(e, RulesetAlias+":")
			if !ok || !g.named(bare, dir) || !rulesetDeclares(bare) {
				rt.Fatalf("%s: %s %q given nowhere\n%s", name, key, e, data)
			}
		}
	}
	for _, ig := range list2(sel["ignore"]) {
		m := mapping(ig)
		for _, p := range list(m["paths"]) {
			if !g.ignores[strings.TrimSuffix(p, "/**")] {
				rt.Fatalf("%s: ignore path %q given nowhere\n%s", name, p, data)
			}
		}
		for _, r := range list(m["rules"]) {
			// A local rule standing in for the one ignored; else a rule
			// named, or one of a category named: an ignore_only over a
			// category is the ruleset's rules carrying its tag.
			if id, ok := strings.CutPrefix(r, LocalRulesetAlias+":"); ok {
				if _, given := g.valued[id]; !given {
					rt.Fatalf("%s: ignore rule %q given nowhere\n%s", name, r, data)
				}
				continue
			}
			bare, ok := strings.CutPrefix(r, RulesetAlias+":")
			tagged := false
			for _, tag := range strings.Fields(rulesetRules[bare].tags) {
				tagged = tagged || g.named(tag, dir)
			}
			if !ok || !rulesetDeclares(bare) || !g.named(bare, dir) && !tagged {
				rt.Fatalf("%s: ignore rule %q given nowhere\n%s", name, r, data)
			}
		}
		if k, has := m["kind"]; has && k != "lint" && k != "breaking" {
			rt.Fatalf("%s: kind %v\n%s", name, k, data)
		}
	}
}

// named reports whether a section named a bare category or id, or
// an option set names it: where the breaking section governing the
// selection — a module's own, else the file's — ignores unstable
// packages, a breaking name read as its _STABLE variant is named by
// its base.
func (g *given) named(bare, dir string) bool {
	if g.entries[bare] {
		return true
	}
	base, stable := strings.CutSuffix(bare, "_STABLE")
	return stable && g.stableFor(dir) && g.entries[base]
}

// stableFor reports whether the breaking section governing a
// selection ignores unstable packages: the root's is a lone module's
// own where it has one, the file's otherwise.
func (g *given) stableFor(dir string) bool {
	if dir == "" && len(g.dirs) == 1 {
		dir = g.dirs[0]
	}
	if g.ownBreaking[dir] {
		return g.stable[dir]
	}
	return g.stable["top"]
}

// declared reports whether the ruleset declares a rule or a tag by
// the bare name.
func rulesetDeclares(bare string) bool {
	_, rule := rulesetRules[bare]
	return rule || rulesetTags[check.KindLint][bare] || rulesetTags[check.KindBreaking][bare]
}

// section writes a lint or breaking section under the indent, its
// entries drawn from the ruleset's categories and ids and from the
// markers, its paths under dir — and, once in a while, an ignore
// outside every module, which reaches no file.
func (g *given) section(rt *rapid.T, b *strings.Builder, indent, kind, dir string) {
	ids := sortedKeys(rulesetRules)
	categories := []string{"STANDARD", "BASIC", "MINIMAL", "COMMENTS", "DEFAULT"}
	if kind == "breaking" {
		categories = []string{"FILE", "PACKAGE", "WIRE", "WIRE_JSON"}
	}
	// An entry the ruleset declares is named; DEFAULT is named as the
	// STANDARD it reads as; a marker entry the ruleset never declared
	// reaches no written file.
	entry := func(label string) string {
		var e string
		switch rapid.IntRange(0, 2).Draw(rt, label+" form") {
		case 0:
			e = rapid.SampledFrom(categories).Draw(rt, label)
		case 1:
			e = rapid.SampledFrom(ids).Draw(rt, label)
		default:
			e = g.draw(rt, "ru")
			g.forbidden[e] = true
			return e
		}
		if e == "DEFAULT" {
			g.entries["STANDARD"] = true
		} else {
			g.entries[e] = true
		}
		return e
	}
	under := func(label string) string {
		p := g.draw(rt, "p")
		if rapid.Bool().Draw(rt, label+" deeper") {
			p += "/" + g.draw(rt, "p")
		}
		g.ignores[p] = true
		if dir != "." {
			p = dir + "/" + p
		}
		return p
	}
	fmt.Fprintf(b, "%s%s:\n", indent, kind)
	// A v2 module's section saying nothing — no entry, no option off
	// its default — is no section of its own, the file's standing in.
	said := false
	for _, key := range []string{"use", "except", "ignore"} {
		n := rapid.IntRange(0, 2).Draw(rt, key)
		if n == 0 {
			continue
		}
		said = true
		var entries []string
		for i := 0; i < n; i++ {
			e := entry(key)
			if key == "ignore" {
				e = under("ignored")
			}
			if !contains(entries, e) {
				entries = append(entries, e)
			}
		}
		if key == "ignore" && indent == "" && len(g.dirs) > 1 && rapid.Bool().Draw(rt, "stray ignore") {
			stray := g.draw(rt, "st")
			g.forbidden[stray] = true // a top-level ignore in no module maps to nothing
			entries = append(entries, stray)
		}
		fmt.Fprintf(b, "%s  %s:\n", indent, key)
		for _, e := range entries {
			fmt.Fprintf(b, "%s    - %s\n", indent, e)
		}
	}
	if rapid.Bool().Draw(rt, "ignore_only") {
		said = true
		fmt.Fprintf(b, "%s  ignore_only:\n%s    %s:\n", indent, indent, entry("only"))
		for range rapid.IntRange(1, 2).Draw(rt, "only paths") {
			fmt.Fprintf(b, "%s      - %s\n", indent, under("only path"))
		}
	}
	if kind == "lint" {
		// A value-bearing option: its value names the local rule the
		// migration declares, the rule it reshapes excluded beside it
		// (REQ-migrate-rule-options) — a marker, or a value needing an
		// escape in the expression or a quoted YAML spelling, one no
		// id carries (a colon, whitespace), or buf's default, which
		// declares nothing.
		for _, o := range []string{"enum_zero_value_suffix", "service_suffix"} {
			if rapid.Bool().Draw(rt, o) {
				sx := g.draw(rt, "sx")
				if v := rapid.SampledFrom([]string{"", "it's", `a\b`, "a b", "a:b", "a\nb", "\t", ruleOptions[o].def}).Draw(rt, o+" value"); v != "" {
					sx = v
				}
				fmt.Fprintf(b, "%s  %s: %s\n", indent, o, strconv.Quote(sx))
				rule := ruleOptions[o].rules[0]
				g.valued[rule+"_"+sx] = sx
				g.entries[rule] = true
			}
		}
		// The uniqueness allowances and the empties, each spelled true
		// or false: set, they name the variant standing in for the rule
		// they read, the uniqueness variant named for the allowances
		// set together, beside the rule excluded.
		set := map[string]bool{}
		for _, o := range []string{"rpc_allow_same_request_response", "rpc_allow_google_protobuf_empty_requests", "rpc_allow_google_protobuf_empty_responses"} {
			if rapid.Bool().Draw(rt, o) {
				set[o] = rapid.Bool().Draw(rt, o+" true")
				fmt.Fprintf(b, "%s  %s: %v\n", indent, o, set[o])
			}
		}
		same, reqs, resps := set["rpc_allow_same_request_response"], set["rpc_allow_google_protobuf_empty_requests"], set["rpc_allow_google_protobuf_empty_responses"]
		if same || reqs || resps {
			id := "RPC_REQUEST_RESPONSE_UNIQUE_ALLOW"
			if same {
				id += "_SAME"
			}
			if reqs || resps {
				id += "_EMPTY"
			}
			if reqs {
				id += "_REQUESTS"
			}
			if resps {
				id += "_RESPONSES"
			}
			g.entries["RPC_REQUEST_RESPONSE_UNIQUE"], g.entries[id] = true, true
		}
		if reqs {
			g.entries["RPC_REQUEST_STANDARD_NAME"], g.entries["RPC_REQUEST_STANDARD_NAME_ALLOW_EMPTY"] = true, true
		}
		if resps {
			g.entries["RPC_RESPONSE_STANDARD_NAME"], g.entries["RPC_RESPONSE_STANDARD_NAME_ALLOW_EMPTY"] = true, true
		}
		if rapid.Bool().Draw(rt, "comments") {
			fmt.Fprintf(b, "%s  disallow_comment_ignores: %v\n", indent, rapid.Bool().Draw(rt, "disallow"))
		}
	} else {
		at := "top"
		if indent != "" {
			at = dir
		}
		if rapid.Bool().Draw(rt, "unstable") {
			v := rapid.Bool().Draw(rt, "ignore unstable")
			fmt.Fprintf(b, "%s  ignore_unstable_packages: %v\n", indent, v)
			g.stable[at] = g.stable[at] || v
			said = said || v
		}
		if indent != "" && said {
			g.ownBreaking[dir] = true
		}
	}
}

// gen writes a buf.gen.yaml, or a template — the first file where
// no buf.gen.yaml lies there:
// plugins from the catalog at a version or none, a local one, a
// protoc builtin, each with an out and perhaps an opt, sometimes
// include_imports and include_wkt; a --plugin
// replacement for a catalog plugin sometimes; inputs naming a
// directory under a module, created in the tree, each a files
// pattern; clean; managed mode with a declarative override, buf's
// defaults then the spec's constants, and a disable naming a module
// the configuration declares nowhere — on
// a template beside a buf.gen.yaml, an override of its own or none,
// neither written: the first file's overrides are every entry's.
func (g *given) gen(rt *rapid.T, repl *Replacements, first, template bool) string {
	var b strings.Builder
	b.WriteString("version: v2\n")
	if template && rapid.Bool().Draw(rt, "template key") {
		// A top-level key the reader passes over, reported among the
		// keys no step models whatever the file's place.
		fmt.Fprintf(&b, "%s: x\n", g.draw(rt, "tk"))
	}
	if first && rapid.Bool().Draw(rt, "clean") {
		b.WriteString("clean: true\n")
		g.clean = true
	}
	if rapid.Bool().Draw(rt, "inputs") {
		b.WriteString("inputs:\n")
		switch rapid.IntRange(0, 4).Draw(rt, "input form") {
		case 0:
			// An input of another kind: pb reads nothing from it.
			fmt.Fprintf(&b, "  - module: buf.build/%s/%s\n", g.draw(rt, "own"), g.draw(rt, "own"))
		case 1:
			// The root, no paths: every workspace file.
			b.WriteString("  - directory: .\n")
		default:
			b.WriteString("  - directory: .\n    paths:\n")
			for range rapid.IntRange(1, 2).Draw(rt, "paths") {
				dir := rapid.SampledFrom(g.dirs).Draw(rt, "input module")
				sub := g.draw(rt, "ip")
				switch rapid.IntRange(0, 4).Draw(rt, "path form") {
				case 0:
					// A whole module: every file where it is alone,
					// unmapped among several; a pattern never.
					fmt.Fprintf(&b, "      - %s\n", dir)
				case 1:
					// A missing path: unmapped, no pattern.
					fmt.Fprintf(&b, "      - %s\n", path.Join(dir, sub))
				case 2:
					// The same relative path under two modules:
					// unmapped, no pattern; alone, a pattern.
					if len(g.dirs) > 1 {
						other := g.dirs[(indexOf(g.dirs, dir)+1)%len(g.dirs)]
						g.extra[path.Join("repo", g.config, other, sub, sub+".proto")] = "syntax = \"proto3\";\n"
					} else {
						g.patterns[sub+"/**"] = true
					}
					g.extra[path.Join("repo", g.config, dir, sub, sub+".proto")] = "syntax = \"proto3\";\n"
					fmt.Fprintf(&b, "      - %s\n", path.Join(dir, sub))
				default:
					// The path exists in the tree, a directory holding
					// a file, and in no other module: a pattern.
					g.extra[path.Join("repo", g.config, dir, sub, sub+".proto")] = "syntax = \"proto3\";\n"
					g.patterns[sub+"/**"] = true
					fmt.Fprintf(&b, "      - %s\n", path.Join(dir, sub))
				}
			}
		}
	}
	b.WriteString("plugins:\n")
	// A replacement is one per plugin across every generation file.
	if g.replaced == nil {
		g.replaced = map[string]bool{}
	}
	replaced := g.replaced
	for range rapid.IntRange(1, 3).Draw(rt, "plugins") {
		switch rapid.IntRange(0, 2).Draw(rt, "plugin form") {
		case 0:
			name := rapid.SampledFrom(Catalog).Draw(rt, "plugin")
			repo := CatalogRepository(name)
			spelled := name
			switch rapid.IntRange(0, 2).Draw(rt, "version form") {
			case 0:
				v := g.version(rt)
				spelled += ":" + v
				g.refs[repo+":"+v] = true
			case 1:
				// buf admits semver's suffixes; a prerelease is a tag,
				// a build suffix's `+` is none, so its entry is
				// unmapped and no reference of the catalog's.
				v := g.version(rt) + rapid.SampledFrom([]string{"-rc1", "-beta.2", "+meta", "-rc1+meta"}).Draw(rt, "suffix")
				spelled += ":" + v
				if !strings.Contains(v, "+") {
					g.refs[repo+":"+v] = true
				}
			default:
				g.refs[repo+":v8.8.8"] = true // the highest tag anyTags lists
			}
			fmt.Fprintf(&b, "  - remote: %s\n", spelled)
			if !replaced[name] && rapid.Bool().Draw(rt, "replace plugin") {
				replaced[name] = true
				ref := "ghcr.io/" + g.draw(rt, "img") + "/" + g.draw(rt, "img") + ":" + g.draw(rt, "t")
				g.refs[ref] = true
				if err := repl.Replace("plugin", name+"="+ref); err != nil {
					rt.Fatal(err)
				}
			}
		case 1:
			// A command alone, or with arguments in buf's list form;
			// every element is a local marker, so the file's list is
			// held to them element by element.
			l := g.draw(rt, "l")
			refused := rapid.IntRange(0, 5).Draw(rt, "refused command") == 0
			if refused {
				// A command pb's schema refuses, in either form: the
				// entry is unmapped, so the command is given as no
				// local and a file naming it fails the oracle below.
				l = rapid.SampledFrom([]string{l + "//" + l, "./", l + "\\\\" + l}).Draw(rt, "refused")
			} else {
				g.locals[l] = true
			}
			if n := rapid.IntRange(0, 2).Draw(rt, "local args"); n > 0 {
				argv := []string{l}
				for range n {
					arg := g.draw(rt, "l")
					argv = append(argv, arg)
				}
				if !refused {
					g.locals[strings.Join(argv, " ")] = true
				}
				fmt.Fprintf(&b, "  - local: [%s]\n", strings.Join(argv, ", "))
			} else {
				fmt.Fprintf(&b, "  - local: %s\n", l)
			}
		default:
			fmt.Fprintf(&b, "  - protoc_builtin: %s\n", g.draw(rt, "bi"))
		}
		out := g.draw(rt, "o") + "/" + g.draw(rt, "o")
		g.outs[path.Join(g.config, out)] = true // read relative to the root through the configuration's directory
		fmt.Fprintf(&b, "    out: %s\n", out)
		if rapid.Bool().Draw(rt, "opt") {
			opt := g.draw(rt, "k") + "=" + g.draw(rt, "v")
			g.opts[opt] = true
			fmt.Fprintf(&b, "    opt: %s\n", opt)
		}
		if rapid.Bool().Draw(rt, "include_imports") {
			b.WriteString("    include_imports: true\n")
			// Beside include_imports alone, as buf admits it.
			if rapid.Bool().Draw(rt, "include_wkt") {
				b.WriteString("    include_wkt: true\n")
			}
		}
	}
	if !first {
		// A template's managed mode: none, or an override of its own
		// — the file's overrides are the first file's, so a value
		// given here only reaches a written file where the first
		// gave it; a top-level key the reader passes over sometimes.
		if rapid.Bool().Draw(rt, "template managed") {
			// A role of its own, so the value never equals one the
			// first file gave — which would be the first file's.
			jv := g.draw(rt, "tjv")
			g.forbidden[jv] = true
			fmt.Fprintf(&b, "managed:\n  enabled: true\n  override:\n    - file_option: java_package\n      value: %s\n", jv)
		}
		return b.String()
	}
	if rapid.Bool().Draw(rt, "managed") {
		b.WriteString("managed:\n  enabled: true\n")
		g.managed = true
		if rapid.Bool().Draw(rt, "override") {
			// Rules in order: values, prefixes and suffixes of
			// java_package and go_package's prefix, each over every
			// file or a path, nested sometimes, so buf's per-file
			// reading has state to carry across scopes.
			b.WriteString("  override:\n")
			for n := rapid.IntRange(1, 4).Draw(rt, "rules"); n > 0; n-- {
				var r givenRule
				switch rapid.IntRange(0, 3).Draw(rt, "rule kind") {
				case 0:
					r = givenRule{option: "java_package", axis: "value", value: g.draw(rt, "jv")}
					g.overrideValues[r.value] = true
				case 1:
					r = givenRule{option: "java_package", axis: "prefix", value: g.draw(rt, "jp")}
					g.javaPrefixes[r.value] = true
				case 2:
					r = givenRule{option: "java_package", axis: "suffix", value: g.draw(rt, "js")}
					g.suffixes[r.value] = true
				case 3:
					r = givenRule{option: "go_package", axis: "prefix", value: g.draw(rt, "gp")}
					g.prefixes[r.value] = true
				}
				name := r.option
				if r.axis != "value" {
					name += "_" + r.axis
				}
				fmt.Fprintf(&b, "    - file_option: %s\n      value: %s\n", name, r.value)
				if rapid.Bool().Draw(rt, "scoped") {
					// A path, one under it, or a sibling sharing its
					// spelling as a prefix, which contains nothing of it.
					r.scope = g.draw(rt, "sc")
					switch rapid.IntRange(0, 2).Draw(rt, "nested") {
					case 1:
						r.scope += "/" + g.draw(rt, "sd")
					case 2:
						r.scope += "way"
					}
					g.overridePaths[r.scope] = true
					fmt.Fprintf(&b, "      path: %s\n", r.scope)
				}
				g.rules = append(g.rules, r)
			}
		}
		if rapid.Bool().Draw(rt, "disable") {
			owner, mod := g.draw(rt, "own"), g.draw(rt, "own")
			g.forbidden[owner], g.forbidden[mod] = true, true
			fmt.Fprintf(&b, "  disable:\n    - module: buf.build/%s/%s\n", owner, mod)
		}
	}
	return b.String()
}

// rooted is a module directory read relative to the root through
// the configuration's (REQ-migrate-verb).
func (g *given) rooted(d string) string {
	return path.Join(g.config, d)
}

// rootedDirs is every module directory rooted.
func (g *given) rootedDirs() []string {
	var out []string
	for _, d := range g.dirs {
		out = append(out, g.rooted(d))
	}
	return out
}

// text is a document scalar's text, "" where absent.
func text(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// indexOf is the index of s in list, -1 where absent.
func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

// closure is a table name with every name its entry declares,
// transitively.
func closure(name string) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(n string)
	walk = func(n string) {
		if seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
		for _, d := range Dependencies[n].Deps {
			walk(d)
		}
	}
	walk(name)
	return out
}

// tokens splits a text on the separators a value is composed with —
// slashes, colons, at signs, equals signs, commas, quotes, brackets
// and whitespace — keeping dots, which lie within a host name, a
// version and a file name.
func tokens(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return strings.ContainsRune("/:@=, \n\t\"'[]", r)
	})
}

func mapping(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func list(v any) []string {
	var out []string
	for _, e := range list2(v) {
		out = append(out, fmt.Sprint(e))
	}
	return out
}

func list2(v any) []any {
	l, _ := v.([]any)
	return l
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
