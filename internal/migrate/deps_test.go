package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
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

// specTable reads a two-column table of backticked cells under one
// heading of a spec, as a map from the first column to the second.
func specTable(t *testing.T, file, heading string) map[string]string {
	t.Helper()
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	section := regexp.MustCompile("(?s)\\n## " + regexp.QuoteMeta(heading) + "\\n(.*?)(\\n## |$)").FindStringSubmatch(string(text))
	if section == nil {
		t.Fatalf("%s has no section %q", file, heading)
	}
	rows := map[string]string{}
	for _, r := range regexp.MustCompile("(?m)^\\| `([^`]+)` \\| `([^`]+)` \\|$").FindAllStringSubmatch(section[1], -1) {
		rows[r[1]] = r[2]
	}
	return rows
}

// The dependency table is the spec's table, entry for entry.
func TestDependencyTableMatchesSpec(t *testing.T) {
	rows := specTable(t, "../../docs/specs/migrate.md", "Dependencies")
	if len(rows) != len(Dependencies) || len(rows) == 0 {
		t.Fatalf("the spec's table has %d rows, the code's %d", len(rows), len(Dependencies))
	}
	for name, path := range rows {
		if Dependencies[name] != path {
			t.Errorf("%s: spec %q, code %q", name, path, Dependencies[name])
		}
		if err := module.ValidatePath(path); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

const otel, otelPath = "buf.build/opentelemetry/opentelemetry", "github.com/open-telemetry/opentelemetry-proto"

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
	lock, err := bufconfig.ParseLock([]byte("version: v2\ndeps:\n  - name: " + otel + "\n    commit: abc123\n    digest: b5:deadbeef\n"))
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
		otelPath:                  "v0.0.0-20240102030405-abcdefabcdef",
		"github.com/acme/private": "v1.4.0",
	}}
	facts, err := Deps(context.Background(), d, src, lock, repl, l)
	if err != nil {
		t.Fatal(err)
	}
	wantDeps := map[string]string{
		otelPath:                  "v0.0.0-20240102030405-abcdefabcdef",
		"github.com/acme/private": "v1.4.0",
		"github.com/acme/pinned":  "v2.1.0",
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
	// never for a pinned replacement.
	if strings.Join(d.asked, ",") != otelPath+",github.com/acme/private,github.com/acme/unreachable" {
		t.Fatalf("asked %v", d.asked)
	}
	want := "buf.yaml deps[0] " + otel + ":abc123 -> " + otelPath + "@v0.0.0-20240102030405-abcdefabcdef (the dependency table)\n" +
		"buf.yaml deps[1] buf.build/acme/private -> github.com/acme/private@v1.4.0 (--dep)\n" +
		"buf.yaml deps[2] buf.build/acme/pinned -> github.com/acme/pinned@v2.1.0 (--dep)\n" +
		"buf.yaml deps[3] buf.build/acme/alias -> " + otelPath + "@v0.0.0-20240102030405-abcdefabcdef (--dep)\n" +
		"buf.yaml deps[4] buf.build/nobody/knows !! no entry in the dependency table: pass --dep buf.build/nobody/knows=<module path>\n" +
		"buf.yaml deps[5] buf.build/acme/unreachable !! no version discovered for github.com/acme/unreachable (no origin answers for github.com/acme/unreachable): pass --dep buf.build/acme/unreachable=github.com/acme/unreachable@<version>\n" +
		"buf.lock deps[0] " + otel + " abc123 !! a BSR commit names no git commit; pb's pin is the lockfile's own, made by the tidy"
	if got := factsOf(facts); got != want {
		t.Fatalf("facts:\n%s", got)
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
	d2 := &fakeDiscovery{latest: map[string]string{otelPath: "v1.0.0"}}
	facts2, err := Deps(context.Background(), d2, src2, nil, pins, l2)
	if err != nil || len(d2.asked) != 0 || l2.Modules["."].Deps[otelPath] != "v9.0.0" {
		t.Fatalf("a pin over a discovered name: %v asked %v deps %v", err, d2.asked, l2.Modules["."].Deps)
	}
	if got := factsOf(facts2); got != "buf.yaml deps[0] "+otel+" -> "+otelPath+"@v9.0.0 (the dependency table, at --dep buf.build/acme/pin's version)\nbuf.yaml deps[1] buf.build/acme/pin -> "+otelPath+"@v9.0.0 (--dep)" {
		t.Fatalf("pinned facts:\n%s", got)
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
	dA := &fakeDiscovery{}
	if facts, err := Deps(context.Background(), dA, srcA, nil, many, lA); err != nil || len(dA.asked) != 0 || len(facts) != 10 || lA.Modules["."].Deps[otelPath] != "v9.0.0" {
		t.Fatalf("ten pins agreeing: %v asked %v facts %d", err, dA.asked, len(facts))
	}
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
	if err != nil || len(facts3) != 1 || strings.Contains(facts3[0].Text, "\n") || !strings.Contains(facts3[0].Text, "(ERROR: Repository not found.): pass --dep") {
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
	d = &fakeDiscovery{latest: map[string]string{otelPath: "v1.3.2"}}
	facts, err = Deps(context.Background(), d, src, nil, Replacements{}, l)
	if err != nil || len(d.asked) != 1 || len(facts) != len(dirs) {
		t.Fatalf("workspace deps: %v asked %v facts %d", err, d.asked, len(facts))
	}
	for _, dir := range dirs {
		if l.Modules[dir].Deps[otelPath] != "v1.3.2" || len(l.Modules[dir].Deps) != 1 {
			t.Fatalf("%s: %v", dir, l.Modules[dir].Deps)
		}
	}
	for i, f := range facts {
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
