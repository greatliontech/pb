package internal_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/greatliontech/pb/"

// domain is one row of README.md's table: the name and its packages,
// module-relative.
type domain struct {
	name     string
	packages []string
}

// level names the domains README.md says stand level, which never
// import one another; each must be a row of the table.
var level = map[string]bool{"source": true, "proto": true, "plugin": true}

// testSupport is imported from test files only; it is no domain, and
// a fixture imports what it fixtures, so its own imports are free.
const testSupport = "internal/testing/"

// readme parses README.md's table, the one statement of the order:
// each row's first cell is the domain, its last cell the packages in
// backticks, a name under internal/ unless it names a command.
func readme(t *testing.T) []domain {
	t.Helper()
	text, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	var domains []domain
	for _, line := range strings.Split(string(text), "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 3 || strings.HasPrefix(strings.TrimSpace(cells[0]), "-") || strings.TrimSpace(cells[0]) == "Domain" {
			continue
		}
		d := domain{name: strings.TrimSpace(cells[0])}
		for _, cell := range strings.Split(cells[2], ",") {
			name := strings.Trim(strings.TrimSpace(cell), "`")
			if name == "" {
				t.Fatalf("README.md row %q names an empty package", d.name)
			}
			if !strings.HasPrefix(name, "cmd/") {
				name = "internal/" + name
			}
			d.packages = append(d.packages, name)
		}
		domains = append(domains, d)
	}
	if len(domains) == 0 {
		t.Fatal("README.md holds no table")
	}
	return domains
}

// listing is every package of the module with its non-test imports,
// module-relative; a package with no non-test files has no row.
func listing(t *testing.T) map[string][]string {
	t.Helper()
	cmd := exec.Command("go", "list", "-f", "{{if .GoFiles}}{{.ImportPath}} {{join .Imports \" \"}}{{end}}", module+"...")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	packages := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		var imports []string
		for _, imp := range fields[1:] {
			if rel, ok := strings.CutPrefix(imp, module); ok {
				imports = append(imports, rel)
			}
		}
		packages[strings.TrimPrefix(fields[0], module)] = imports
	}
	if len(packages) == 0 {
		t.Fatalf("the listing names no package:\n%s", out)
	}
	return packages
}

// violations judges a listing against the domains: every row names a
// domain once, every package has exactly one domain, every import
// edge points up the order or stays within a domain, level domains
// never import one another, test support is imported from no
// non-test file, the level domains are rows, and the table names only
// packages that exist.
func violations(domains []domain, packages map[string][]string) []string {
	var out []string
	rank, owner, names := map[string]int{}, map[string]string{}, map[string]bool{}
	for i, d := range domains {
		if d.name == "" {
			out = append(out, "a row of README.md names no domain")
		} else if names[d.name] {
			out = append(out, "the domain "+d.name+" is two rows of README.md")
		}
		names[d.name] = true
		for _, p := range d.packages {
			if prior, ok := owner[p]; ok {
				out = append(out, p+" is placed twice, in "+prior+" and in "+d.name)
				continue
			}
			rank[p], owner[p] = i, d.name
		}
	}
	for name := range level {
		if !names[name] {
			out = append(out, "the level domain "+name+" is no row of README.md")
		}
	}
	for pkg, imports := range packages {
		if strings.HasPrefix(pkg, testSupport) {
			continue
		}
		from, ok := owner[pkg]
		if !ok {
			out = append(out, pkg+" belongs to no domain in README.md")
			continue
		}
		for _, imp := range imports {
			to, ok := owner[imp]
			switch {
			case strings.HasPrefix(imp, testSupport):
				out = append(out, pkg+" ships "+imp)
			case !ok:
				out = append(out, pkg+" imports "+imp+", which belongs to no domain")
			case to == from:
			case rank[imp] < rank[pkg] && !(level[to] && level[from]):
			default:
				out = append(out, pkg+" ("+from+") imports "+imp+" ("+to+"), against the order")
			}
		}
	}
	for p := range owner {
		if _, ok := packages[p]; !ok {
			out = append(out, "README.md names "+p+", which does not exist")
		}
	}
	return out
}

// The tree keeps the layering README.md states.
func TestPackagesKeepTheLayering(t *testing.T) {
	for _, v := range violations(readme(t), listing(t)) {
		t.Error(v)
	}
}

// The judgement refuses each shape the layering forbids, over a
// listing built from the table itself so the only edge under test is
// the one added, and admits the permitted shapes.
func TestLayeringRefuses(t *testing.T) {
	table := readme(t)
	conforming := func() map[string][]string {
		m := map[string][]string{}
		for _, d := range table {
			for _, p := range d.packages {
				m[p] = nil
			}
		}
		return m
	}
	edit := func(f func(map[string][]string)) func([]domain, map[string][]string) ([]domain, map[string][]string) {
		return func(d []domain, m map[string][]string) ([]domain, map[string][]string) { f(m); return d, m }
	}
	for name, c := range map[string]struct {
		shape func([]domain, map[string][]string) ([]domain, map[string][]string)
		want  string
	}{
		"an edge against the order":                   {edit(func(m map[string][]string) { m["internal/modfile"] = []string{"internal/dep"} }), "against the order"},
		"an edge up the list between level domains":   {edit(func(m map[string][]string) { m["internal/proxy"] = []string{"internal/genfile"} }), "against the order"},
		"an edge down the list between level domains": {edit(func(m map[string][]string) { m["internal/genfile"] = []string{"internal/proxy"} }), "against the order"},
		"a package outside every domain":              {edit(func(m map[string][]string) { m["internal/stray"] = nil }), "belongs to no domain"},
		"an import outside every domain":              {edit(func(m map[string][]string) { m["internal/dep"] = []string{"internal/stray"} }), "belongs to no domain"},
		"test support shipped":                        {edit(func(m map[string][]string) { m["internal/dep"] = []string{"internal/testing/gittest"} }), "ships"},
		"a table entry that does not exist":           {edit(func(m map[string][]string) { delete(m, "internal/mvs") }), "does not exist"},
		"a package placed twice": {func(d []domain, m map[string][]string) ([]domain, map[string][]string) {
			d = append([]domain(nil), d...)
			d[0] = domain{d[0].name, append(append([]string(nil), d[0].packages...), "internal/dep")}
			return d, m
		}, "placed twice"},
		"a domain that is two rows": {func(d []domain, m map[string][]string) ([]domain, map[string][]string) {
			d = append([]domain(nil), d...)
			d[0] = domain{d[len(d)-1].name, d[0].packages}
			return d, m
		}, "is two rows"},
		"a row naming no domain": {func(d []domain, m map[string][]string) ([]domain, map[string][]string) {
			d = append([]domain(nil), d...)
			d[0] = domain{"", d[0].packages}
			return d, m
		}, "names no domain"},
		"a level domain that is no row": {func(d []domain, m map[string][]string) ([]domain, map[string][]string) {
			var out []domain
			for _, x := range d {
				if x.name == "proto" {
					x = domain{"compile", x.packages}
				}
				out = append(out, x)
			}
			return out, m
		}, "is no row"},
	} {
		d, m := c.shape(table, conforming())
		got := violations(d, m)
		if len(got) != 1 || !strings.Contains(got[0], c.want) {
			t.Errorf("%s: %q, want one naming %q", name, got, c.want)
		}
	}
	// The permitted shapes: an edge up the order, one within a domain,
	// a level domain reaching below the level ones, a fixture importing
	// what it fixtures.
	m := conforming()
	m["internal/dep"] = []string{"internal/modfile"}
	m["internal/modfile"] = []string{"internal/archive"}
	m["internal/modfiles"] = []string{"internal/archive"}
	m["internal/testing/gittest"] = []string{"internal/dep"}
	if got := violations(table, m); len(got) != 0 {
		t.Errorf("the permitted shapes: %q", got)
	}
}
