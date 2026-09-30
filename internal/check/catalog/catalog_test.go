// Package catalog is the engine's fixture corpus: buf's lint and
// breaking rules rewritten in CEL under environment 1, each rule
// compiled — proving the environment closed over the catalog — and
// evaluated over a schema built to trip every one of them, the
// findings held against a golden file. pb ships no rules; the corpus
// is a test.
package catalog

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/check/env1"
	"github.com/greatliontech/pb/internal/check/eval"
	"github.com/greatliontech/pb/internal/check/lintfile"
	"github.com/greatliontech/pb/internal/check/rules"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/testing/prototest"
)

var update = flag.Bool("update", false, "rewrite the golden findings from the current run")

// catalog reads the rule files under testdata/buf/<kind>.
func catalog(t *testing.T, kind string) []rules.Rule {
	t.Helper()
	files := map[string][]byte{}
	for p, b := range tree(t, filepath.Join("testdata", "buf", kind)) {
		if module.IsRuleFile(p) {
			files[p] = b
		}
	}
	located, err := rules.Discover(files)
	if err != nil {
		t.Fatal(err)
	}
	sel, err := lintfile.Select(&lintfile.File{}, []lintfile.Ruleset{{Path: "buf/" + kind, Alias: "buf", Files: located}})
	if err != nil {
		t.Fatal(err)
	}
	rs := sel.Rules
	if len(rs) == 0 {
		t.Fatalf("no rules under testdata/buf/%s", kind)
	}
	return rs
}

// sources reads a fixture directory's protobuf files by path.
func sources(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for p, b := range tree(t, dir) {
		if strings.HasSuffix(p, ".proto") {
			out[p] = string(b)
		}
	}
	return out
}

// tree reads the regular files under dir by slash path relative to it.
func tree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(strings.TrimPrefix(p, dir+string(filepath.Separator)))] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func paths(srcs map[string]string) []string {
	out := make([]string, 0, len(srcs))
	for p := range srcs {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// golden holds the run's findings against testdata/<name>.txt: every
// rule of the catalog fires at least once, and the lines are the
// golden's.
func golden(t *testing.T, name string, rs []rules.Rule, findings []check.Finding) {
	t.Helper()
	check.Sort(findings)
	var buf bytes.Buffer
	fired := map[string]bool{}
	for _, f := range findings {
		buf.WriteString(f.String() + "\n")
		fired[f.Rule] = true
	}
	for _, r := range rs {
		if !fired[r.Name()] {
			t.Errorf("%s never fires over the fixture", r.Name())
		}
	}
	path := filepath.Join("testdata", name+".txt")
	if *update {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Errorf("findings differ from %s (run with -update after reviewing):\n%s", path, buf.String())
	}
}

// Every lint rule of buf's catalog compiles under environment 1 and
// fires over the lint fixture as the golden says.
func TestLintCatalog(t *testing.T) {
	rs := catalog(t, "lint")
	srcs := sources(t, filepath.Join("testdata", "lint"))
	set := env1.NewSet(prototest.Compile(t, srcs))
	env, err := env1.New(set, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if _, err := env.Compile(r); err != nil {
			t.Errorf("%v", err)
		}
	}
	report, err := eval.Lint(env, paths(srcs), prototest.Source(srcs), rs)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "lint", rs, report.Findings)
}

// Every breaking rule of buf's catalog compiles under environment 1
// and fires over the old and new fixtures as the golden says.
func TestBreakingCatalog(t *testing.T) {
	rs := catalog(t, "breaking")
	oldSrc := sources(t, filepath.Join("testdata", "breaking", "old"))
	newSrc := sources(t, filepath.Join("testdata", "breaking", "new"))
	oldSet := env1.NewSet(prototest.Compile(t, oldSrc))
	newSet := env1.NewSet(prototest.Compile(t, newSrc))
	env, err := env1.New(newSet, oldSet)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if _, err := env.Compile(r); err != nil {
			t.Errorf("%v", err)
		}
	}
	report, err := eval.Breaking(env, paths(oldSrc), paths(newSrc), prototest.Source(newSrc), prototest.Source(oldSrc), rs)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "breaking", rs, report.Findings)
}

// The variants read buf's shaping options as distinct rules: each
// fires exactly where buf's rule fires under the option, judged over
// small schemas built for the cases; the functions the variants are
// built from take the option's value, so a workspace rule of one's
// own reads a value-bearing option (migrate.md REQ-migrate-rule-options).
func TestVariants(t *testing.T) {
	lint := catalog(t, "lint")
	byName := map[string]rules.Rule{}
	for _, r := range lint {
		byName[r.ID] = r
	}
	verdicts := func(srcs map[string]string, ids ...string) map[string]bool {
		t.Helper()
		files := prototest.Compile(t, srcs)
		set := env1.NewSet(files)
		env, err := env1.New(set, nil)
		if err != nil {
			t.Fatal(err)
		}
		var rs []rules.Rule
		for _, id := range ids {
			r, ok := byName[id]
			if !ok {
				t.Fatalf("no rule %s", id)
			}
			rs = append(rs, r)
		}
		report, err := eval.Lint(env, paths(srcs), func(p string) ([]byte, error) { return []byte(srcs[p]), nil }, rs)
		if err != nil {
			t.Fatal(err)
		}
		fired := map[string]bool{}
		for _, f := range report.Findings {
			fired[strings.TrimPrefix(f.Rule, "buf:")] = true
		}
		return fired
	}
	empty := "syntax = \"proto3\";\npackage google.protobuf;\nmessage Empty {}\n"
	schema := func(body string) map[string]string {
		return map[string]string{"google/protobuf/empty.proto": empty, "a.proto": "syntax = \"proto3\";\npackage a;\nimport \"google/protobuf/empty.proto\";\n" + body}
	}
	// The uniqueness rule and its seven variants, each named for the
	// allowances it reads, over every case buf's rule distinguishes:
	// one type for an rpc's request and response, free under
	// allow_same alone — google.protobuf.Empty for both free under
	// both empties as well; Empty repeated as a request free under
	// empty requests, as a response under empty responses, as one
	// rpc's request and another's response under either; a type
	// shared by two rpcs never free; a package's own Empty no
	// google.protobuf.Empty.
	unique := "RPC_REQUEST_RESPONSE_UNIQUE"
	variants := []string{"", "_ALLOW_SAME", "_ALLOW_EMPTY_REQUESTS", "_ALLOW_EMPTY_RESPONSES", "_ALLOW_SAME_EMPTY_REQUESTS", "_ALLOW_SAME_EMPTY_RESPONSES", "_ALLOW_EMPTY_REQUESTS_RESPONSES", "_ALLOW_SAME_EMPTY_REQUESTS_RESPONSES"}
	var ids []string
	for _, v := range variants {
		ids = append(ids, unique+v)
	}
	for _, c := range []struct {
		name, body string
		passes     func(same, reqs, resps bool) bool
	}{
		{"one type for both", "message Ping {}\nservice S { rpc Do(Ping) returns (Ping); }\n", func(same, reqs, resps bool) bool { return same }},
		{"Empty for both of one rpc", "service S { rpc Ping(google.protobuf.Empty) returns (google.protobuf.Empty); }\n", func(same, reqs, resps bool) bool { return same || (reqs && resps) }},
		{"Empty as two requests", "message AResponse {}\nmessage BResponse {}\nservice S {\n  rpc A(google.protobuf.Empty) returns (AResponse);\n  rpc B(google.protobuf.Empty) returns (BResponse);\n}\n", func(same, reqs, resps bool) bool { return reqs }},
		{"Empty as two responses", "message ARequest {}\nmessage BRequest {}\nservice S {\n  rpc A(ARequest) returns (google.protobuf.Empty);\n  rpc B(BRequest) returns (google.protobuf.Empty);\n}\n", func(same, reqs, resps bool) bool { return resps }},
		{"Empty as a request and another's response", "message AResponse {}\nmessage BRequest {}\nservice S {\n  rpc A(google.protobuf.Empty) returns (AResponse);\n  rpc B(BRequest) returns (google.protobuf.Empty);\n}\n", func(same, reqs, resps bool) bool { return reqs || resps }},
		{"a type shared by two rpcs", "message X {}\nmessage Y {}\nmessage Z {}\nservice S {\n  rpc A(X) returns (Y);\n  rpc B(Y) returns (Z);\n}\n", func(same, reqs, resps bool) bool { return false }},
		{"a package's own Empty", "message Empty {}\nmessage AResponse {}\nmessage BResponse {}\nservice S {\n  rpc A(Empty) returns (AResponse);\n  rpc B(Empty) returns (BResponse);\n}\n", func(same, reqs, resps bool) bool { return false }},
		{"distinct types", "message ARequest {}\nmessage AResponse {}\nservice S { rpc A(ARequest) returns (AResponse); }\n", func(same, reqs, resps bool) bool { return true }},
	} {
		fired := verdicts(schema(c.body), ids...)
		for _, v := range variants {
			same, reqs, resps := strings.Contains(v, "_SAME"), strings.Contains(v, "_REQUESTS"), strings.Contains(v, "_RESPONSES")
			if fired[unique+v] == c.passes(same, reqs, resps) {
				t.Errorf("%s under %s: fired %v", c.name, unique+v, fired[unique+v])
			}
		}
	}
	// The standard-name rules and their _ALLOW_EMPTY variants: the
	// rpc's or the service's and rpc's name, or google.protobuf.Empty
	// under the variant alone; a package's own Empty neither.
	for _, c := range []struct {
		name, body                                         string
		request, response, allowedRequest, allowedResponse bool
	}{
		{"the rpc's name", "message GetRequest {}\nmessage GetResponse {}\nservice S { rpc Get(GetRequest) returns (GetResponse); }\n", false, false, false, false},
		{"the service's and rpc's name", "message SGetRequest {}\nmessage SGetResponse {}\nservice S { rpc Get(SGetRequest) returns (SGetResponse); }\n", false, false, false, false},
		{"Empty for both", "service S { rpc Get(google.protobuf.Empty) returns (google.protobuf.Empty); }\n", true, true, false, false},
		{"a package's own Empty", "message Empty {}\nservice S { rpc Get(Empty) returns (Empty); }\n", true, true, true, true},
		{"another name", "message Req {}\nmessage Resp {}\nservice S { rpc Get(Req) returns (Resp); }\n", true, true, true, true},
	} {
		fired := verdicts(schema(c.body), "RPC_REQUEST_STANDARD_NAME", "RPC_RESPONSE_STANDARD_NAME", "RPC_REQUEST_STANDARD_NAME_ALLOW_EMPTY", "RPC_RESPONSE_STANDARD_NAME_ALLOW_EMPTY")
		if fired["RPC_REQUEST_STANDARD_NAME"] != c.request || fired["RPC_RESPONSE_STANDARD_NAME"] != c.response || fired["RPC_REQUEST_STANDARD_NAME_ALLOW_EMPTY"] != c.allowedRequest || fired["RPC_RESPONSE_STANDARD_NAME_ALLOW_EMPTY"] != c.allowedResponse {
			t.Errorf("%s: %v", c.name, fired)
		}
	}
	// The suffix functions take the suffix: a workspace rule of one's
	// own reads the option's value, lent the ruleset's one scope as an
	// import lends it.
	house := map[string][]byte{"h.rules.yaml": []byte("celEnv: 1\nimports:\n  - path: example.com/buf\n    version: v1.0.0\n    alias: buf\nrules:\n  - id: SVC\n    kind: lint\n    target: service\n    severity: error\n    cel: buf.serviceSuffix(service, 'Svc')\n    message: m\n  - id: ZERO\n    kind: lint\n    target: enum-value\n    severity: error\n    cel: buf.enumZeroValueSuffix(enumValue, '_NONE')\n    message: m\n")}
	sel := houseRules(t, house, catalogFiles(t, "lint"))
	srcs := map[string]string{"a.proto": "syntax = \"proto3\";\npackage a;\nenum E { E_NONE = 0; }\nenum F { F_UNSPECIFIED = 0; }\nservice ShopSvc {}\nservice ShopService {}\n"}
	env, err := env1.New(env1.NewSet(prototest.Compile(t, srcs)), nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := eval.Lint(env, paths(srcs), func(p string) ([]byte, error) { return []byte(srcs[p]), nil }, sel.Rules)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, f := range report.Findings {
		lines = append(lines, f.String())
	}
	sort.Strings(lines)
	if want := []string{"a.proto:4:10: error house:ZERO: m", "a.proto:6:1: error house:SVC: m"}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("a workspace rule over the suffix functions: %v", lines)
	}
}

// The _STABLE variants skip a file whose package carries buf's
// unstable version suffix — its last component `v<major>` followed by
// `test<anything>`, or by an optional `p<patch>` and `alpha` or
// `beta` with an optional number, every number without a leading
// sign, zero no major — on either side of the pair; a one-component
// package is no version, as buf reads it (migrate.md
// REQ-migrate-rule-options).
func TestUnstablePackage(t *testing.T) {
	house := map[string][]byte{"h.rules.yaml": []byte("celEnv: 1\nimports:\n  - path: example.com/buf\n    version: v1.0.0\n    alias: buf\nrules:\n  - id: UNSTABLE\n    kind: lint\n    target: file\n    severity: error\n    cel: \"!buf.unstablePackage(file.package)\"\n    message: m\n")}
	sel := houseRules(t, house, catalogFiles(t, "breaking"))
	packages := map[string]bool{
		"acme.v1": false, "acme.v1beta1": true, "acme.v1beta": true, "acme.v1alpha": true, "acme.v1alpha2": true, "acme.v01beta1": true, "acme.v1beta01": true,
		"acme.v1test": true, "acme.v1testfoo": true, "acme.v1p1beta1": true, "acme.v1p1": false, "acme.v10alpha": true, "acme.v0beta1": false, "acme.v1beta0": false,
		"acme.v1alphabeta1": false, "acme.v1beta1x": false, "acme.v1beta1.sub": false, "v1beta1": false, "acme.beta1": false, "acme.v1.beta1": false,
	}
	srcs := map[string]string{}
	for pkg := range packages {
		srcs[strings.ReplaceAll(pkg, ".", "_")+".proto"] = "syntax = \"proto3\";\npackage " + pkg + ";\n"
	}
	env, err := env1.New(env1.NewSet(prototest.Compile(t, srcs)), nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := eval.Lint(env, paths(srcs), func(p string) ([]byte, error) { return []byte(srcs[p]), nil }, sel.Rules)
	if err != nil {
		t.Fatal(err)
	}
	fired := map[string]bool{}
	for _, f := range report.Findings {
		fired[f.Path] = true
	}
	for pkg, unstable := range packages {
		if got := fired[strings.ReplaceAll(pkg, ".", "_")+".proto"]; got != unstable {
			t.Errorf("%s: unstable %v", pkg, got)
		}
	}
	// Either side: a file stable on one side of the pair and unstable
	// on the other is skipped, as buf skips a finding whose file or
	// against-file package is unstable — the pair by path for a file
	// rule, the message's file on each side for a deletion.
	breaking := catalog(t, "breaking")
	var stable []rules.Rule
	for _, r := range breaking {
		if r.ID == "FILE_SAME_PACKAGE_STABLE" || r.ID == "FILE_SAME_PACKAGE" || r.ID == "MESSAGE_NO_DELETE_STABLE" || r.ID == "MESSAGE_NO_DELETE" {
			stable = append(stable, r)
		}
	}
	for _, c := range []struct {
		name, old, new string
		fired          []string
	}{
		{"to stable", "acme.v1beta1", "acme.v1", []string{"buf:FILE_SAME_PACKAGE", "buf:MESSAGE_NO_DELETE"}},
		{"to unstable", "acme.v1", "acme.v1beta1", []string{"buf:FILE_SAME_PACKAGE", "buf:MESSAGE_NO_DELETE"}},
		{"stable both", "acme.v1", "acme.v2", []string{"buf:FILE_SAME_PACKAGE", "buf:FILE_SAME_PACKAGE_STABLE", "buf:MESSAGE_NO_DELETE", "buf:MESSAGE_NO_DELETE_STABLE"}},
	} {
		oldSrc := map[string]string{"a.proto": "syntax = \"proto3\";\npackage " + c.old + ";\nmessage M { int32 x = 1; }\n"}
		newSrc := map[string]string{"a.proto": "syntax = \"proto3\";\npackage " + c.new + ";\n"}
		env, err := env1.New(env1.NewSet(prototest.Compile(t, newSrc)), env1.NewSet(prototest.Compile(t, oldSrc)))
		if err != nil {
			t.Fatal(err)
		}
		report, err := eval.Breaking(env, paths(oldSrc), paths(newSrc), prototest.Source(newSrc), prototest.Source(oldSrc), stable)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range report.Findings {
			got = append(got, f.Rule)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, c.fired) {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

// houseRules is a workspace ruleset's rules, its scope lent the
// catalog files' one scope under the alias buf, as the loader lends
// an import.
func houseRules(t *testing.T, house map[string][]byte, lentFrom []rules.Located) lintfile.Selection {
	t.Helper()
	located, err := rules.Discover(house)
	if err != nil {
		t.Fatal(err)
	}
	scope := located[0].File.Scope
	scope.Lent = map[string][]rules.Lent{}
	for _, fn := range lentFrom[0].File.Scope.Functions {
		scope.Lent["buf"] = append(scope.Lent["buf"], rules.Lent{Function: fn, Scope: lentFrom[0].File.Scope})
	}
	sel, err := lintfile.Select(&lintfile.File{}, []lintfile.Ruleset{{Path: "example.com/house", Alias: "house", Files: located}})
	if err != nil {
		t.Fatal(err)
	}
	return sel
}

// catalogFiles reads the rule files under testdata/buf/<kind> with
// their scopes.
func catalogFiles(t *testing.T, kind string) []rules.Located {
	t.Helper()
	files := map[string][]byte{}
	for p, b := range tree(t, filepath.Join("testdata", "buf", kind)) {
		if module.IsRuleFile(p) {
			files[p] = b
		}
	}
	located, err := rules.Discover(files)
	if err != nil {
		t.Fatal(err)
	}
	return located
}

// The ruleset is discovered whole, its lint and breaking files one
// namespace of functions and one of import aliases, and every rule
// of it compiles under its kind's environment through that one
// scope, the ruleset being the compile unit (check-rules.md
// REQ-rules-functions, REQ-rules-imports).
func TestRulesetDiscoversWhole(t *testing.T) {
	files := map[string][]byte{}
	for p, b := range tree(t, filepath.Join("testdata", "buf")) {
		if module.IsRuleFile(p) {
			files[p] = b
		}
	}
	located, err := rules.Discover(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(located) < 2 || located[1].File.Scope != located[0].File.Scope {
		t.Fatalf("the ruleset's files share one scope: %d files", len(located))
	}
	src := map[string]string{"a.proto": "syntax = \"proto3\";\npackage a;\n"}
	lint, err := env1.New(env1.NewSet(prototest.Compile(t, src)), nil)
	if err != nil {
		t.Fatal(err)
	}
	breaking, err := env1.New(env1.NewSet(prototest.Compile(t, src)), env1.NewSet(prototest.Compile(t, src)))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, lf := range located {
		for _, r := range lf.File.Rules {
			env := lint
			if r.Kind == check.KindBreaking {
				env = breaking
			}
			if _, err := env.Compile(r); err != nil {
				t.Errorf("%s: %v", r.ID, err)
			}
			n++
		}
	}
	if n != len(rulesetIDs(t)) {
		t.Fatalf("compiled %d rules, the catalog holds %d", n, len(rulesetIDs(t)))
	}
}

// rulesetIDs is every rule id of the catalog's two kinds.
func rulesetIDs(t *testing.T) []string {
	t.Helper()
	var ids []string
	for _, kind := range []string{"lint", "breaking"} {
		for _, r := range catalog(t, kind) {
			ids = append(ids, r.ID)
		}
	}
	return ids
}
