package migrate

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/goccy/go-yaml"
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
// of a category or id the input named or buf's default; an ignore
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
		g.depPaths[Ruleset] = true
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
		files := map[string]string{"repo/buf.yaml": b.String()}
		if len(deps) > 0 && rapid.Bool().Draw(rt, "lock") {
			var lb strings.Builder
			lb.WriteString("version: v2\ndeps:\n")
			for _, d := range deps {
				fmt.Fprintf(&lb, "  - name: %s\n    commit: %s\n    digest: b5:%s\n", d, g.draw(rt, "c"), g.draw(rt, "dg"))
			}
			g.used["b5"] = true
			files["repo/buf.lock"] = lb.String()
		}
		if rapid.Bool().Draw(rt, "gen") {
			files["repo/buf.gen.yaml"] = g.gen(rt, &repl)
		}
		for _, d := range g.dirs {
			files[path.Join("repo", d, g.draw(rt, "pr")+".proto")] = "syntax = \"proto3\";\n// buf:lint:ignore " + g.draw(rt, "id") + "\nmessage " + g.draw(rt, "ms") + " {}\n"
		}

		ws := memfs.New()
		for p, text := range files {
			if err := util.WriteFile(ws, p, []byte(text), 0o644); err != nil {
				rt.Fatal(err)
			}
		}
		var out strings.Builder
		err := Run(context.Background(), Invocation{
			WS: ws, Dir: "repo", ModulePath: g.modulePath, Replacements: repl,
			Discovery: &anyDiscovery{latest: latest}, PluginTags: anyTags,
			Tidy: func(context.Context) error { return nil },
			Out:  &out,
		})
		if err != nil && !errors.Is(err, ErrUnmapped) {
			rt.Fatalf("Run over\n%s\n%v", files["repo/buf.yaml"], err)
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
			rt.Fatalf("%s: %s\n%s\nfrom\n%s", name, fmt.Sprintf(format, args...), data, files["repo/buf.yaml"])
		}
		checked := 0
		for _, d := range g.dirs {
			name := path.Join(d, module.ModuleFileName)
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
		}
		if doc, data, ok := read(workspace.FileName); ok {
			for _, u := range list(doc["use"]) {
				if !contains(g.dirs, u) {
					fail(workspace.FileName, data, "use %s given nowhere", u)
				}
			}
		}
		if doc, data, ok := read(lintfile.FileName); ok {
			checked++
			if rs := list(doc["rulesets"]); len(rs) != 1 || rs[0] != Ruleset {
				fail(lintfile.FileName, data, "rulesets %v", rs)
			}
			g.selection(rt, lintfile.FileName, data, doc)
			for dir, sel := range mapping(doc["modules"]) {
				if !contains(g.dirs, dir) {
					fail(lintfile.FileName, data, "module %s given nowhere", dir)
				}
				g.selection(rt, lintfile.FileName, data, mapping(sel))
			}
		}
		if doc, data, ok := read(genfile.FileName); ok {
			for _, p := range list2(doc["plugins"]) {
				pl := mapping(p)
				if ref, has := pl["ref"]; has && !g.refs[fmt.Sprint(ref)] {
					fail(genfile.FileName, data, "ref %v given nowhere", ref)
				}
				if l, has := pl["local"]; has && !g.locals[fmt.Sprint(l)] {
					fail(genfile.FileName, data, "local %v given nowhere", l)
				}
				if !g.outs[fmt.Sprint(pl["out"])] {
					fail(genfile.FileName, data, "out %v given nowhere", pl["out"])
				}
				if o, has := pl["opt"]; has && !g.opts[fmt.Sprint(o)] {
					fail(genfile.FileName, data, "opt %v given nowhere", o)
				}
			}
			for _, o := range list2(doc["overrides"]) {
				ov := mapping(o)
				if ov["option"] != "java_package" {
					fail(genfile.FileName, data, "override option %v: buf's computed options declare no value", ov["option"])
				}
				if !g.overrideValues[fmt.Sprint(ov["value"])] {
					fail(genfile.FileName, data, "override value %v given nowhere", ov["value"])
				}
				if f := fmt.Sprint(ov["files"]); f != "**" && !g.overridePaths[strings.TrimSuffix(f, "/**")] {
					fail(genfile.FileName, data, "override files %v given nowhere", f)
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
type given struct {
	modulePath     string
	dirs           []string
	depPaths       map[string]bool   // a table's, a closure's, a replacement's, the ruleset's
	versions       map[string]string // path -> the version discovery or the flag gave it
	entries        map[string]bool   // bare categories and ids the sections named, and buf's defaults
	ignores        map[string]bool   // module-relative ignore paths under their module
	refs           map[string]bool   // plugin references the flag or the table gave
	locals         map[string]bool
	outs, opts     map[string]bool
	overrideValues map[string]bool
	overridePaths  map[string]bool
	used           map[string]bool // every marker drawn
	forbidden      map[string]bool // markers no written file may hold
}

func newGiven() *given {
	return &given{
		depPaths: map[string]bool{}, versions: map[string]string{}, entries: map[string]bool{"STANDARD": true, "FILE": true},
		ignores: map[string]bool{}, refs: map[string]bool{}, locals: map[string]bool{}, outs: map[string]bool{}, opts: map[string]bool{},
		overrideValues: map[string]bool{}, overridePaths: map[string]bool{}, used: map[string]bool{}, forbidden: map[string]bool{},
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

// selection checks one selection of the lint file: enable and exclude
// the ruleset's qualified spellings of named entries, ignores over
// named paths, kinds pb's.
func (g *given) selection(rt *rapid.T, name string, data []byte, sel map[string]any) {
	for _, key := range []string{"enable", "exclude"} {
		for _, e := range list(sel[key]) {
			bare, ok := strings.CutPrefix(e, Ruleset+":")
			if !ok || !g.entries[bare] || !rulesetDeclares(bare) {
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
			// A rule named, or one of a category named: an ignore_only
			// over a category is the ruleset's rules carrying its tag.
			bare, ok := strings.CutPrefix(r, Ruleset+":")
			tagged := false
			for _, tag := range strings.Fields(rulesetRules[bare].tags) {
				tagged = tagged || g.entries[tag]
			}
			if !ok || !rulesetDeclares(bare) || !g.entries[bare] && !tagged {
				rt.Fatalf("%s: ignore rule %q given nowhere\n%s", name, r, data)
			}
		}
		if k, has := m["kind"]; has && k != "lint" && k != "breaking" {
			rt.Fatalf("%s: kind %v\n%s", name, k, data)
		}
	}
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
	for _, key := range []string{"use", "except", "ignore"} {
		n := rapid.IntRange(0, 2).Draw(rt, key)
		if n == 0 {
			continue
		}
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
		fmt.Fprintf(b, "%s  ignore_only:\n%s    %s:\n", indent, indent, entry("only"))
		for range rapid.IntRange(1, 2).Draw(rt, "only paths") {
			fmt.Fprintf(b, "%s      - %s\n", indent, under("only path"))
		}
	}
	if kind == "lint" {
		if rapid.Bool().Draw(rt, "suffix") {
			sx := g.draw(rt, "sx")
			g.forbidden[sx] = true // a rule-shaping option's value is unmapped, never a value of pb's
			fmt.Fprintf(b, "%s  enum_zero_value_suffix: %s\n", indent, sx)
		}
		if rapid.Bool().Draw(rt, "comments") {
			fmt.Fprintf(b, "%s  disallow_comment_ignores: %v\n", indent, rapid.Bool().Draw(rt, "disallow"))
		}
	}
}

// gen writes a buf.gen.yaml: plugins from the catalog at a version or
// none, a local one, a protoc builtin, each with an out and perhaps
// an opt; a --plugin replacement for a catalog plugin sometimes;
// managed mode with a declarative override, a computed option buf's
// own heuristic would fill, and a disable.
func (g *given) gen(rt *rapid.T, repl *Replacements) string {
	var b strings.Builder
	b.WriteString("version: v2\nplugins:\n")
	replaced := map[string]bool{}
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
			l := g.draw(rt, "l")
			g.locals[l] = true
			fmt.Fprintf(&b, "  - local: %s\n", l)
		default:
			fmt.Fprintf(&b, "  - protoc_builtin: %s\n", g.draw(rt, "bi"))
		}
		out := g.draw(rt, "o") + "/" + g.draw(rt, "o")
		g.outs[out] = true
		fmt.Fprintf(&b, "    out: %s\n", out)
		if rapid.Bool().Draw(rt, "opt") {
			opt := g.draw(rt, "k") + "=" + g.draw(rt, "v")
			g.opts[opt] = true
			fmt.Fprintf(&b, "    opt: %s\n", opt)
		}
	}
	if rapid.Bool().Draw(rt, "managed") {
		b.WriteString("managed:\n  enabled: true\n")
		if rapid.Bool().Draw(rt, "override") {
			b.WriteString("  override:\n")
			jv := g.draw(rt, "jv")
			g.overrideValues[jv] = true
			fmt.Fprintf(&b, "    - file_option: java_package\n      value: %s\n", jv)
			if rapid.Bool().Draw(rt, "scoped") {
				sc := g.draw(rt, "sc")
				g.overridePaths[sc] = true
				fmt.Fprintf(&b, "      path: %s\n", sc)
			}
			if rapid.Bool().Draw(rt, "computed") {
				gp := g.draw(rt, "gp")
				g.forbidden[gp] = true // buf's own heuristic: pb declares values alone
				fmt.Fprintf(&b, "    - file_option: go_package_prefix\n      value: %s\n", gp)
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
