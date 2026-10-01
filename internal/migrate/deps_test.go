package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/migrate/bufconfig"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/modfile"
	"github.com/greatliontech/pb/internal/module/version"
)

// fakeDiscovery answers Latest from a table and counts the asks.
type fakeDiscovery struct {
	latest map[string]string
	asked  []string
}

func (f *fakeDiscovery) Versions(context.Context, string) ([]version.Version, error) {
	return nil, errors.New("the deps step asks for the latest, never the listing")
}

func (f *fakeDiscovery) Latest(_ context.Context, path string) (version.Version, error) {
	f.asked = append(f.asked, path)
	s, ok := f.latest[path]
	if !ok {
		return version.Version{}, errors.New("no origin answers for " + path)
	}
	return version.Parse(s)
}

// specTable reads a table of backticked cells under one heading of a
// spec, as a map from the first column to the rest, each cell's
// backticked values in order.
func specTable(t *testing.T, file, heading string) map[string][][]string {
	t.Helper()
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	section := regexp.MustCompile("(?s)\\n## " + regexp.QuoteMeta(heading) + "\\n(.*?)(\\n## |$)").FindStringSubmatch(string(text))
	if section == nil {
		t.Fatalf("%s has no section %q", file, heading)
	}
	rows := map[string][][]string{}
	cell := regexp.MustCompile("`([^`]+)`")
	for _, line := range strings.Split(section[1], "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cols := strings.Split(strings.Trim(line, "|"), "|")
		var vals [][]string
		for _, c := range cols[1:] {
			var v []string
			for _, m := range cell.FindAllStringSubmatch(c, -1) {
				v = append(v, m[1])
			}
			vals = append(vals, v)
		}
		rows[cell.FindStringSubmatch(cols[0])[1]] = vals
	}
	return rows
}

// The dependency table is the spec's table, entry for entry: the
// path, the imports, and — the layout test's own data — an anchor
// file for every entry and an import naming an entry.
func TestDependencyTableMatchesSpec(t *testing.T) {
	rows := specTable(t, "../../docs/specs/migrate.md", "Dependencies")
	if len(rows) != len(Dependencies) || len(rows) == 0 {
		t.Fatalf("the spec's table has %d rows, the code's %d", len(rows), len(Dependencies))
	}
	for name, cols := range rows {
		e, ok := Dependencies[name]
		if ok && e.Path == "" {
			// An entry mapping to nothing: the spec's row spells no
			// path, and the layout test has nothing to verify.
			if len(cols) != 2 || len(cols[0]) != 0 || len(cols[1]) != 0 || len(e.Deps) != 0 {
				t.Errorf("%s: spec %v, code %+v, want a row mapping to nothing", name, cols, e)
			}
			if _, has := anchors[name]; has {
				t.Errorf("%s: an anchor for an entry mapping to nothing", name)
			}
			continue
		}
		if !ok || len(cols) != 2 || len(cols[0]) != 1 || e.Path != cols[0][0] {
			t.Errorf("%s: spec %v, code %+v", name, cols, e)
			continue
		}
		if !slices.Equal(cols[1], e.Deps) {
			t.Errorf("%s imports: spec %v, code %v", name, cols[1], e.Deps)
		}
		if err := module.ValidatePath(e.Path); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		for _, d := range e.Deps {
			if _, ok := Dependencies[d]; !ok {
				t.Errorf("%s imports %s, which the table lacks", name, d)
			}
		}
		if _, ok := anchors[name]; !ok {
			t.Errorf("%s: no anchor file for the layout test", name)
		}
	}
	for name := range anchors {
		if _, ok := Dependencies[name]; !ok {
			t.Errorf("%s: an anchor for no entry", name)
		}
	}
}

// The bundled imports table is the spec's, entry for entry
// (REQ-migrate-imports).
func TestBundledImportsMatchSpec(t *testing.T) {
	rows := specTable(t, "../../docs/specs/migrate.md", "Imports")
	if len(rows) != len(BundledImports) || len(rows) == 0 {
		t.Fatalf("the spec's table has %d rows, the code's %d", len(rows), len(BundledImports))
	}
	for imp, cols := range rows {
		if p, ok := BundledImports[imp]; !ok || len(cols) != 1 || len(cols[0]) != 1 || cols[0][0] != p {
			t.Errorf("%s: spec %v, code %q", imp, cols, p)
		}
		if err := module.ValidatePath(BundledImports[imp]); err != nil {
			t.Errorf("%s: %v", imp, err)
		}
	}
}

const otel, otelPath = "buf.build/opentelemetry/opentelemetry", "github.com/open-telemetry/opentelemetry-proto"

// A name the table holds brings the entries its files import, to
// closure, each declared at its discovered version and reported as
// the name's dependency; a replacement's path stands for the name
// and the table's imports for it still (REQ-migrate-deps).
func TestDepsDeclaresTheClosure(t *testing.T) {
	cfg, err := bufconfig.ParseFile([]byte("version: v2\ndeps:\n  - buf.build/grpc/grpc\n  - buf.build/grpc-ecosystem/grpc-gateway\nmodules:\n  - path: a\n"))
	if err != nil {
		t.Fatal(err)
	}
	src := &Source{File: cfg}
	l, err := Modules(src, "github.com/acme/mono")
	if err != nil {
		t.Fatal(err)
	}
	// The gateway replaced; googleapis, a name the closure brings,
	// pinned by the flag a failed discovery's fact would name.
	var repl Replacements
	for _, v := range []string{"buf.build/grpc-ecosystem/grpc-gateway=github.com/acme/gateway@v3.0.0", "buf.build/googleapis/googleapis=github.com/googleapis/googleapis@v0.0.0-20240102030405-123456123456"} {
		if err := repl.Replace("dep", v); err != nil {
			t.Fatal(err)
		}
	}
	d := &fakeDiscovery{latest: map[string]string{
		"github.com/grpc/grpc-proto": "v0.0.0-20240102030405-abcdefabcdef",
		Ruleset:                      "v0.1.0",
	}}
	facts, err := Deps(context.Background(), d, src, nil, repl, l)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"github.com/grpc/grpc-proto":       "v0.0.0-20240102030405-abcdefabcdef",
		"github.com/acme/gateway":          "v3.0.0",
		"github.com/googleapis/googleapis": "v0.0.0-20240102030405-123456123456",
	}
	if got := l.Modules["a"].Deps; len(got) != len(want) {
		t.Fatalf("deps %v, want %v", got, want)
	} else {
		for p, v := range want {
			if got[p] != v {
				t.Fatalf("%s: %q, want %q", p, got[p], v)
			}
		}
	}
	found := false
	for _, f := range facts {
		if f.Source == "buf.build/grpc/grpc's dependency buf.build/googleapis/googleapis" && f.Mapped && f.Text == "github.com/googleapis/googleapis@v0.0.0-20240102030405-123456123456 (--dep)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no fact for the closure among %+v", facts)
	}
}

// Each buf dependency, its ref aside, is declared in every module at
// the replacement's version or the latest discovered once per path; a
// name neither the replacements nor the table holds, or whose
// discovery fails, is an unmapped fact naming the flag's form; the
// lockfile's entries are unmapped (REQ-migrate-deps).
func TestDeps(t *testing.T) {
	cfg, err := bufconfig.ParseFile([]byte("version: v2\ndeps:\n  - " + otel + ":abc123\n  - buf.build/acme/private\n  - buf.build/acme/pinned\n  - buf.build/acme/alias\n  - buf.build/nobody/knows\n  - buf.build/acme/unreachable\nmodules:\n  - path: a\n  - path: b\n"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := bufconfig.ParseLock([]byte("version: v2\ndeps:\n  - name: " + otel + "\n    commit: abc123\n    digest: b5:deadbeef\n  - name: buf.build/googleapis/googleapis\n    commit: def456\n    digest: b5:cafe\n  - name: " + WellKnownTypes + "\n    commit: 789\n    digest: b5:feed\n  - name: buf.build/nobody/knows\n    commit: 000\n    digest: b5:0\n"))
	if err != nil {
		t.Fatal(err)
	}
	src := &Source{File: cfg}
	l, err := Modules(src, "github.com/acme/mono")
	if err != nil {
		t.Fatal(err)
	}
	var repl Replacements
	for _, v := range []string{"buf.build/acme/private=github.com/acme/private", "buf.build/acme/pinned=github.com/acme/pinned@v2.1.0", "buf.build/acme/alias=" + otelPath, "buf.build/acme/unreachable=github.com/acme/unreachable"} {
		if err := repl.Replace("dep", v); err != nil {
			t.Fatal(err)
		}
	}
	d := &fakeDiscovery{latest: map[string]string{
		otelPath:                           "v0.0.0-20240102030405-abcdefabcdef",
		"github.com/acme/private":          "v1.4.0",
		"github.com/googleapis/googleapis": "v0.0.0-20240202020202-0123456789ab",
		Ruleset:                            "v0.1.0",
	}}
	facts, err := Deps(context.Background(), d, src, lock, repl, l)
	if err != nil {
		t.Fatal(err)
	}
	wantDeps := map[string]string{
		otelPath:                           "v0.0.0-20240102030405-abcdefabcdef",
		"github.com/acme/private":          "v1.4.0",
		"github.com/acme/pinned":           "v2.1.0",
		"github.com/googleapis/googleapis": "v0.0.0-20240202020202-0123456789ab",
	}
	for dir, f := range l.Modules {
		if len(f.Deps) != len(wantDeps) {
			t.Fatalf("%s: deps %v", dir, f.Deps)
		}
		for p, v := range wantDeps {
			if f.Deps[p] != v {
				t.Errorf("%s: %s = %q, want %q", dir, p, f.Deps[p], v)
			}
		}
		if _, err := modfile.Encode(f); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
	}
	// Discovered once per path — the alias shares otel's path — and
	// never for a pinned replacement; the ruleset last.
	if strings.Join(d.asked, ",") != otelPath+",github.com/acme/private,github.com/acme/unreachable,github.com/googleapis/googleapis,"+Ruleset {
		t.Fatalf("asked %v", d.asked)
	}
	want := "buf.yaml deps[0] " + otel + ":abc123 -> " + otelPath + "@v0.0.0-20240102030405-abcdefabcdef (the dependency table)\n" +
		"buf.yaml deps[1] buf.build/acme/private -> github.com/acme/private@v1.4.0 (--dep)\n" +
		"buf.yaml deps[2] buf.build/acme/pinned -> github.com/acme/pinned@v2.1.0 (--dep)\n" +
		"buf.yaml deps[3] buf.build/acme/alias -> " + otelPath + "@v0.0.0-20240102030405-abcdefabcdef (--dep)\n" +
		"buf.yaml deps[4] buf.build/nobody/knows !! no entry in the dependency table: pass --dep buf.build/nobody/knows=<module path>\n" +
		"buf.yaml deps[5] buf.build/acme/unreachable !! no version discovered for github.com/acme/unreachable (no origin answers for github.com/acme/unreachable): pass --dep buf.build/acme/unreachable=github.com/acme/unreachable@<version>\n" +
		// The lock's entries the configuration does not declare, each
		// a declaration: a table name declared, the well-known types
		// nothing, a name nowhere unmapped.
		"buf.lock deps[1] buf.build/googleapis/googleapis def456 -> github.com/googleapis/googleapis@v0.0.0-20240202020202-0123456789ab (the dependency table)\n" +
		"buf.lock deps[2] " + WellKnownTypes + " 789 -> nothing: the well-known types are the toolchain's own, never a dependency\n" +
		"the lint file's rulesets " + Ruleset + " -> rulesets: path " + Ruleset + " version v0.1.0 alias " + RulesetAlias + " (discovered)\n" +
		// The lock's entries the configuration declared: pinned over
		// the path declared, or nowhere where the name went unmapped.
		"buf.lock deps[0] " + otel + " abc123 -> pinned by the tidy in pb.lock over " + otelPath + " (a BSR commit names no git commit; pb's pin is the lockfile's own)\n" +
		"buf.lock deps[3] buf.build/nobody/knows 000 !! pinned nowhere: no entry in the dependency table: pass --dep buf.build/nobody/knows=<module path>"
	if got := factsOf(facts); got != want {
		t.Fatalf("facts:\n%s", got)
	}
	if l.DepPaths[otel] != otelPath || l.DepPaths["buf.build/acme/pinned"] != "github.com/acme/pinned" || l.DepPaths["buf.build/nobody/knows"] != "" {
		t.Fatalf("DepPaths = %v", l.DepPaths)
	}

	// A pinned replacement's version applies to every name reaching its
	// path, discovery never asked; two pins at odds are refused.
	cfg2, _ := bufconfig.ParseFile([]byte("version: v2\ndeps:\n  - " + otel + "\n  - buf.build/acme/pin\nmodules:\n  - path: .\n"))
	src2 := &Source{File: cfg2}
	l2, _ := Modules(src2, "github.com/acme/one")
	var pins Replacements
	if err := pins.Replace("dep", "buf.build/acme/pin="+otelPath+"@v9.0.0"); err != nil {
		t.Fatal(err)
	}
	// The ruleset's version pinned by its own path, discovery never
	// asked; a replacement keyed by it naming another path is refused.
	if err := pins.Replace("dep", Ruleset+"="+Ruleset+"@v0.2.0"); err != nil {
		t.Fatal(err)
	}
	d2 := &fakeDiscovery{latest: map[string]string{otelPath: "v1.0.0", Ruleset: "v0.1.0"}}
	facts2, err := Deps(context.Background(), d2, src2, nil, pins, l2)
	if err != nil || len(d2.asked) != 0 || l2.Modules["."].Deps[otelPath] != "v9.0.0" || l2.RulesetVersion != "v0.2.0" || l2.Modules["."].Deps[Ruleset] != "" {
		t.Fatalf("a pin over a discovered name: %v asked %v deps %v", err, d2.asked, l2.Modules["."].Deps)
	}
	if got := factsOf(facts2); got != "buf.yaml deps[0] "+otel+" -> "+otelPath+"@v9.0.0 (the dependency table, at --dep buf.build/acme/pin's version)\nbuf.yaml deps[1] buf.build/acme/pin -> "+otelPath+"@v9.0.0 (--dep)\nthe lint file's rulesets "+Ruleset+" -> rulesets: path "+Ruleset+" version v0.2.0 alias "+RulesetAlias+" (--dep)" {
		t.Fatalf("pinned facts:\n%s", got)
	}
	// A replacement keyed by a bundled import's provider, or by a
	// workspace module's own path, pins a version Imports reads: Deps
	// admits it as declared.
	for _, key := range []string{"github.com/protocolbuffers/protobuf-go/src", "github.com/acme/one"} {
		var byPath Replacements
		if err := byPath.Replace("dep", key+"="+key+"@v1.2.3"); err != nil {
			t.Fatal(err)
		}
		if _, err := Deps(context.Background(), d2, src2, nil, byPath, l2); err != nil {
			t.Fatalf("a replacement keyed by %s: %v", key, err)
		}
		// One naming another path, or no version, is refused.
		for _, target := range []string{"github.com/other@v1.0.0", key} {
			var bad Replacements
			if err := bad.Replace("dep", key+"="+target); err != nil {
				t.Fatal(err)
			}
			if _, err := Deps(context.Background(), d2, src2, nil, bad, l2); err == nil || !strings.Contains(err.Error(), "a replacement keyed by a module path names a version alone") {
				t.Fatalf("--dep %s=%s: %v", key, target, err)
			}
		}
	}
	// A BSR name a workspace module bears is the sibling, declared in
	// every module but itself at the version discovered for its path.
	sib, _ := bufconfig.ParseFile([]byte("version: v2\nmodules:\n  - path: a\n    name: buf.build/acme/a\n  - path: b\ndeps:\n  - buf.build/acme/a\n"))
	srcSib := &Source{File: sib, Rel: "."}
	lSib, err := Modules(srcSib, "github.com/acme/mono")
	if err != nil {
		t.Fatal(err)
	}
	dSib := &fakeDiscovery{latest: map[string]string{"github.com/acme/mono/a": "v0.4.0", Ruleset: "v0.1.0"}}
	factsSib, err := Deps(context.Background(), dSib, srcSib, nil, Replacements{}, lSib)
	if err != nil || !strings.Contains(factsOf(factsSib), "buf.yaml deps[0] buf.build/acme/a -> github.com/acme/mono/a@v0.4.0 (a workspace module, by its name)") || lSib.Modules["b"].Deps["github.com/acme/mono/a"] != "v0.4.0" || len(lSib.Modules["a"].Deps) != 0 || lSib.DepPaths["buf.build/acme/a"] != "github.com/acme/mono/a" {
		t.Fatalf("a sibling by its name: %v\n%s\n%v %v", err, factsOf(factsSib), lSib.Modules["a"].Deps, lSib.Modules["b"].Deps)
	}
	// A replacement naming the sibling's path declares it the same
	// way: in every module but itself.
	lSib2, _ := Modules(srcSib, "github.com/acme/mono")
	var sibRepl Replacements
	if err := sibRepl.Replace("dep", "buf.build/acme/a=github.com/acme/mono/a@v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := Deps(context.Background(), dSib, srcSib, nil, sibRepl, lSib2); err != nil || lSib2.Modules["b"].Deps["github.com/acme/mono/a"] != "v1.0.0" || len(lSib2.Modules["a"].Deps) != 0 {
		t.Fatalf("a sibling by replacement: %v %v %v", err, lSib2.Modules["a"].Deps, lSib2.Modules["b"].Deps)
	}
	pins.Deps[Ruleset] = Dep{Path: "github.com/acme/fork", Version: "v1.0.0"}
	if _, err := Deps(context.Background(), d2, src2, nil, pins, l2); err == nil || !strings.Contains(err.Error(), "a replacement keyed by it names a version alone") {
		t.Fatalf("the ruleset replaced: %v", err)
	}
	// Many pins on one path agreeing are fine; one at odds is refused
	// naming the first in name order and itself, whatever the map's
	// order; an unused flag is refused before any conflict it is in.
	agree, _ := bufconfig.ParseFile([]byte("version: v2\ndeps:\n  - buf.build/acme/p0\n  - buf.build/acme/p1\n  - buf.build/acme/p2\n  - buf.build/acme/p3\n  - buf.build/acme/p4\n  - buf.build/acme/p5\n  - buf.build/acme/p6\n  - buf.build/acme/p7\n  - buf.build/acme/p8\n  - buf.build/acme/p9\nmodules:\n  - path: .\n"))
	srcA := &Source{File: agree}
	lA, _ := Modules(srcA, "github.com/acme/one")
	var many Replacements
	for i := 0; i < 10; i++ {
		if err := many.Replace("dep", fmt.Sprintf("buf.build/acme/p%d=%s@v9.0.0", i, otelPath)); err != nil {
			t.Fatal(err)
		}
	}
	// The ruleset's discovery failing is an unmapped fact naming the
	// flag that pins it, the buf dependencies unaffected.
	dA := &fakeDiscovery{}
	if facts, err := Deps(context.Background(), dA, srcA, nil, many, lA); err != nil || strings.Join(dA.asked, ",") != Ruleset || len(facts) != 11 || lA.Modules["."].Deps[otelPath] != "v9.0.0" || facts[10].Mapped || !strings.Contains(facts[10].Text, "pass --dep "+Ruleset+"="+Ruleset+"@<version>") {
		t.Fatalf("ten pins agreeing: %v asked %v facts %d", err, dA.asked, len(facts))
	}
	dA.asked = nil
	many.Deps["buf.build/acme/p9"] = Dep{Path: otelPath, Version: "v8.0.0"}
	if _, err := Deps(context.Background(), dA, srcA, nil, many, lA); err == nil || err.Error() != "--dep buf.build/acme/p0 and --dep buf.build/acme/p9 pin "+otelPath+" at v9.0.0 and v8.0.0: a module path is declared at one version" {
		t.Fatalf("a pin at odds: %v", err)
	}
	for _, n := range []string{"u4", "u1", "u3", "u0", "u2"} {
		many.Deps["buf.build/stale/"+n] = Dep{Path: "github.com/stale/" + n, Version: "v1.0.0"}
	}
	if _, err := Deps(context.Background(), dA, srcA, nil, many, lA); err == nil || err.Error() != "--dep buf.build/stale/u0, --dep buf.build/stale/u1, --dep buf.build/stale/u2, --dep buf.build/stale/u3, --dep buf.build/stale/u4: the configuration declares no such dependency" {
		t.Fatalf("unused before a conflict: %v", err)
	}
	// A discovery's error reaches the fact as its first line alone.
	cfg3, _ := bufconfig.ParseFile([]byte("version: v1\ndeps: [" + otel + "]\n"))
	src3 := &Source{File: cfg3}
	l3, _ := Modules(src3, "github.com/acme/one")
	facts3, err := Deps(context.Background(), &multiLineDiscovery{}, src3, nil, Replacements{}, l3)
	if err != nil || len(facts3) != 2 || strings.Contains(facts3[0].Text, "\n") || !strings.Contains(facts3[0].Text, "(ERROR: Repository not found.): pass --dep") || strings.Contains(facts3[1].Text, "\n") {
		t.Fatalf("a multi-line discovery error: %v %+v", err, facts3)
	}

	// A replacement the configuration never names fails the step.
	if err := repl.Replace("dep", "buf.build/acme/unused=github.com/acme/unused"); err != nil {
		t.Fatal(err)
	}
	if _, err := Deps(context.Background(), d, src, nil, repl, l); err == nil || !strings.Contains(err.Error(), "--dep buf.build/acme/unused: the configuration declares no such dependency") {
		t.Fatalf("an unused replacement: %v", err)
	}

	// A v1 workspace: each member's deps, every module declaring them,
	// the members in name order, the root buf.yaml's deps unread; two
	// members naming one BSR module at two refs: one discovery.
	dirs := []string{"h", "c", "f", "a", "e", "b", "g", "d"}
	work, _ := bufconfig.ParseWork([]byte("version: v1\ndirectories: [" + strings.Join(dirs, ", ") + "]\n"))
	root, _ := bufconfig.ParseFile([]byte("version: v1\ndeps: [buf.build/acme/private]\n"))
	src = &Source{File: root, Work: work, Members: map[string]*bufconfig.File{}}
	for i, dir := range dirs {
		f, _ := bufconfig.ParseFile([]byte(fmt.Sprintf("version: v1\ndeps: [%s:ref%d]\n", otel, i)))
		src.Members[dir] = f
	}
	l, _ = Modules(src, "github.com/acme/mono")
	d = &fakeDiscovery{latest: map[string]string{otelPath: "v1.3.2", Ruleset: "v0.1.0"}}
	facts, err = Deps(context.Background(), d, src, nil, Replacements{}, l)
	if err != nil || len(d.asked) != 2 || len(facts) != len(dirs)+1 {
		t.Fatalf("workspace deps: %v asked %v facts %d", err, d.asked, len(facts))
	}
	for _, dir := range dirs {
		if l.Modules[dir].Deps[otelPath] != "v1.3.2" || l.RulesetVersion != "v0.1.0" || len(l.Modules[dir].Deps) != 1 {
			t.Fatalf("%s: %v", dir, l.Modules[dir].Deps)
		}
	}
	for i, f := range facts[:len(dirs)] {
		if want := "abcdefgh"[i : i+1]; !strings.HasPrefix(f.Source, want+"/buf.yaml deps[0] ") {
			t.Fatalf("fact %d out of member order: %s", i, f.Source)
		}
	}
	if _, err := Deps(context.Background(), d, nil, nil, Replacements{}, l); err == nil {
		t.Fatal("no source: no error")
	}
}

// multiLineDiscovery fails as a remote's banner fails, over lines.
type multiLineDiscovery struct{ fakeDiscovery }

func (multiLineDiscovery) Latest(context.Context, string) (version.Version, error) {
	return version.Version{}, errors.New("\x1b[2K\r\n\x1b(B\x1b[m\x07\x1b[31mERROR:\x1b[0m\x08  Repository not found.\nfatal: Could not read from remote repository.")
}

// A replacement is <name>=<target>, given once, a dependency's target
// a module path with a version after @ or none (migrate.md, the
// replacement term).
func TestReplace(t *testing.T) {
	var r Replacements
	for _, v := range []string{"buf.build/x/y=github.com/x/y", "buf.build/p/q=github.com/p/q@v1.2.3"} {
		if err := r.Replace("dep", v); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Replace("plugin", "buf.build/protocolbuffers/go=ghcr.io/acme/protoc-gen-go:v1.36.0"); err != nil {
		t.Fatal(err)
	}
	if r.Deps["buf.build/x/y"] != (Dep{Path: "github.com/x/y"}) || r.Deps["buf.build/p/q"] != (Dep{Path: "github.com/p/q", Version: "v1.2.3"}) || r.Plugins["buf.build/protocolbuffers/go"] != "ghcr.io/acme/protoc-gen-go:v1.36.0" {
		t.Fatalf("%+v", r)
	}
	for name, c := range map[string]struct{ flag, value, want string }{
		"no equals":    {"dep", "buf.build/x/y", "expected <name>=<target>"},
		"empty name":   {"dep", "=github.com/x/y", "expected <name>=<target>"},
		"empty path":   {"dep", "buf.build/x/y=", "expected <name>=<target>"},
		"bad path":     {"dep", "buf.build/x/y=x", "invalid module path"},
		"bad version":  {"dep", "buf.build/a/b=github.com/a/b@1.2", "invalid version"},
		"twice":        {"dep", "buf.build/x/y=github.com/x/z", "is replaced twice"},
		"plugin twice": {"plugin", "buf.build/protocolbuffers/go=other", "is replaced twice"},
		"no flag":      {"module", "a=b", "no replacement flag"},
	} {
		if err := r.Replace(c.flag, c.value); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// oneLine keeps multi-byte runes whole beside any escape, drops an
// escape sequence entire and nothing past its grammar, folds a bare
// control byte, and takes the first line that holds anything; the
// fold is idempotent.
func TestOneLine(t *testing.T) {
	for in, want := range map[string]string{
		"aucun dépôt":                     "aucun dépôt",
		"\x1bépôt introuvable":            "épôt introuvable",
		"\x1b(B\x1b[mdépôt introuvable":   "dépôt introuvable",
		"\x1bM\x1b[1;31mred\x1b[0m":       "red",
		"dépôt \x07\x08\x7f introuvable":  "dépôt introuvable",
		"\x1b[2K\nfatal: real cause here": "fatal: real cause here",
		"  a \t b  \n second":             "a b",
		"\x1b":                            "",
		"":                                "",
		// A truncated sequence swallows nothing after it; a doubled
		// escape leaks no body; an intermediate is not a parameter.
		"Error:\x1b[2\nfatal: the real cause": "Error:",
		"\x1b\x1b[31mERROR: not found":        "ERROR: not found",
		"\x1b0abc":                            "abc",
		"\x1b[error: repository not found":    "rror: repository not found",
		// A carriage return is a space: git's progress line and the
		// cause after it both reach the line.
		"remote: Counting 1%\rremote: ERROR: not found": "remote: Counting 1% remote: ERROR: not found",
		// A string sequence's body is no text: to its terminator, or
		// to the end.
		"\x1b]0;window title\x07ERROR: not found": "ERROR: not found",
		"\x1bP1;2|payload\x1b\\ERROR: not found":  "ERROR: not found",
		"\x1b_apc body without an end":            "",
		"\x1bX sos body \x1b\\ERROR: not found":   "ERROR: not found",
		// An unterminated string sequence ends at a line break, the
		// cause on the next line reaching the line.
		"\x1b]0;title\nremote: ERROR: not found":      "remote: ERROR: not found",
		"remote: \x1b]\nERROR: Repository not found.": "remote:",
		"\x1b]0;t\rremote: ERROR: not found":          "remote: ERROR: not found",
		"\x1b[2K\r\nfatal: after a cleared line":      "fatal: after a cleared line",
	} {
		got := oneLine(in)
		if got != want {
			t.Errorf("oneLine(%q) = %q, want %q", in, got, want)
		}
		if again := oneLine(got); again != got {
			t.Errorf("oneLine(%q) folded twice = %q, once %q", in, again, got)
		}
	}
}

// A fact is one line however it is made: the constructors fold both
// its texts.
func TestFactsAreOneLine(t *testing.T) {
	if f := mapped("a\nb", "c\x1b[1md"); f.Source != "a" || f.Text != "cd" || !f.Mapped {
		t.Fatalf("mapped: %+v", f)
	}
	if f := unmapped(" x \r y", "why\x07"); f.Source != "x y" || f.Text != "why" || f.Mapped {
		t.Fatalf("unmapped: %+v", f)
	}
}
