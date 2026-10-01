package genfile

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/plugin"
	"pgregory.net/rapid"
)

// A well-formed file parses into exactly its declarations, scheme
// discriminated by the key written, scalars kept as spelled
// (REQ-gen-schema).
func TestParse(t *testing.T) {
	in := `
clean: true
plugins:
  - ref: ghcr.io/org/protoc-gen-go:v1.34.2
    out: gen/go
    opt: paths=source_relative
    files: ["acme/v1/*.proto", "**/x.proto"]
    include_imports: true
  - ref: ghcr.io/org/protoc-gen-go:v1.34.2
    out: gen/all
    include_imports: false
    include_wkt: false
    clean: false
  - local: protoc-gen-lint
    out: .
    include_imports: true
    include_wkt: true
    clean: true
  - local: ./tools/bin/protoc-gen-x
    out: gen/x
    opt: 1
  - local: [go, tool, protoc-gen-go-grpc]
    out: gen/grpc
  - local:
      - bun
      - web/node_modules/.bin/protoc-gen-es
      - "--flag with space"
      - ""
    out: gen/es
  - local: [protoc-gen-one]
    out: gen/one
overrides:
  - files: "**/*.proto"
    option: go_package
    value: example.com/gen
  - files: "api/{v1,v2}/*.proto"
    option: java_multiple_files
    value: true
  - files: "**"
    option: go_package
    prefix: example.com/gen
  - files: "**"
    option: java_package
    prefix: com.acme
    suffix: proto
  - files: "**"
    option: ruby_package
    suffix: Proto
`
	f, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Clean {
		t.Error("clean not read")
	}
	want := []Plugin{
		{Scheme: plugin.SchemeOCI, Ref: "ghcr.io/org/protoc-gen-go:v1.34.2", Out: "gen/go", Opt: "paths=source_relative", Files: []string{"acme/v1/*.proto", "**/x.proto"}, IncludeImports: true},
		{Scheme: plugin.SchemeOCI, Ref: "ghcr.io/org/protoc-gen-go:v1.34.2", Out: "gen/all"},
		{Scheme: plugin.SchemeLocal, Ref: "protoc-gen-lint", Out: ".", IncludeImports: true, IncludeWKT: true, Clean: true},
		{Scheme: plugin.SchemeLocal, Ref: "./tools/bin/protoc-gen-x", Out: "gen/x", Opt: "1"},
		{Scheme: plugin.SchemeLocal, Ref: "go", Args: []string{"tool", "protoc-gen-go-grpc"}, Out: "gen/grpc"},
		{Scheme: plugin.SchemeLocal, Ref: "bun", Args: []string{"web/node_modules/.bin/protoc-gen-es", "--flag with space", ""}, Out: "gen/es"},
		{Scheme: plugin.SchemeLocal, Ref: "protoc-gen-one", Out: "gen/one"},
	}
	if len(f.Plugins) != len(want) {
		t.Fatalf("plugins = %+v", f.Plugins)
	}
	for i := range want {
		if !f.Plugins[i].Equal(want[i]) {
			t.Errorf("plugins[%d] = %+v, want %+v", i, f.Plugins[i], want[i])
		}
	}
	wantO := []Override{
		{Files: "**/*.proto", Option: "go_package", Value: "example.com/gen"},
		{Files: "api/{v1,v2}/*.proto", Option: "java_multiple_files", Value: "true"},
	}
	wantO = append(wantO,
		Override{Files: "**", Option: "go_package", Prefix: "example.com/gen"},
		Override{Files: "**", Option: "java_package", Prefix: "com.acme", Suffix: "proto"},
		Override{Files: "**", Option: "ruby_package", Suffix: "Proto"},
	)
	if len(f.Overrides) != len(wantO) {
		t.Fatalf("overrides = %+v", f.Overrides)
	}
	for i := range wantO {
		if f.Overrides[i] != wantO[i] {
			t.Errorf("overrides[%d] = %+v, want %+v", i, f.Overrides[i], wantO[i])
		}
	}
}

// Equal tells two entries apart by every field — each one the type
// has, walked by reflection, so a field added without it is caught —
// as a rendering that dropped any would otherwise pass Encode's
// reading check.
func TestPluginEqual(t *testing.T) {
	base := Plugin{Scheme: plugin.SchemeLocal, Ref: "gen", Args: []string{"a"}, Out: "gen", Opt: "o", Files: []string{"**"}, IncludeImports: true, IncludeWKT: true, Clean: true}
	if !base.Equal(base) {
		t.Fatal("an entry differs from itself")
	}
	rv := reflect.ValueOf(base)
	for i := 0; i < rv.NumField(); i++ {
		changed := reflect.New(rv.Type()).Elem()
		changed.Set(rv)
		f := changed.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("other")
		case reflect.Bool:
			f.SetBool(false)
		case reflect.Slice:
			f.Set(reflect.Zero(f.Type()))
		default:
			t.Fatalf("field %s of a kind this test does not vary", rv.Type().Field(i).Name)
		}
		p := changed.Interface().(Plugin)
		if base.Equal(p) || p.Equal(base) {
			t.Errorf("%s: entries differing in it are equal", rv.Type().Field(i).Name)
		}
	}
}

// Numeric-, boolean-, and float-looking scalars are recorded as the
// text the author wrote, never the parser's typed reading; a block
// scalar is a scalar too, its value the block's text with the
// newline the block ends in.
func TestScalarSpellingsPreserved(t *testing.T) {
	in := "plugins:\n  - ref: localhost:5000/p:0x1f\n    out: gen\n    opt: 007\n  - ref: localhost:5000/q:v1\n    out: gen2\n    opt: |\n      paths=source_relative,\n      module=x\noverrides:\n  - files: a.proto\n    option: opt\n    value: 1.0\n  - files: b.proto\n    option: opt\n    value: True\n  - files: c.proto\n    option: opt\n    value: >-\n      folded\n      value\n  - files: d.proto\n    option: opt\n    value: |\n      two\n      lines\n"
	f, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if f.Plugins[0].Ref != "localhost:5000/p:0x1f" || f.Plugins[0].Opt != "007" {
		t.Errorf("plugin spellings altered: %+v", f.Plugins[0])
	}
	if f.Plugins[1].Opt != "paths=source_relative,\nmodule=x\n" {
		t.Errorf("block scalar opt = %q", f.Plugins[1].Opt)
	}
	if f.Overrides[0].Value != "1.0" || f.Overrides[1].Value != "True" || f.Overrides[2].Value != "folded value" || f.Overrides[3].Value != "two\nlines\n" {
		t.Errorf("override spellings altered: %+v", f.Overrides)
	}
}

func TestParseRejections(t *testing.T) {
	ok := "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n"
	cases := []struct{ name, in, msg string }{
		{"empty", "", "missing plugins"},
		{"no plugins", "overrides: []\n", "missing plugins"},
		{"unknown top key", ok + "runner: docker\n", `unknown key "runner"`},
		{"plugins not list", "plugins: {}\n", "must be a list"},
		{"plugins empty", "plugins: []\n", "must not be empty"},
		{"entry not mapping", "plugins:\n  - ghcr.io/o/p:v1\n", "must be a mapping"},
		{"bare plugin key", "plugins:\n  - plugin: go\n    out: gen\n", `unknown key "plugin"`},
		{"no scheme", "plugins:\n  - out: gen\n", "exactly one of ref or local, found 0"},
		{"two schemes", "plugins:\n  - ref: ghcr.io/o/p:v1\n    local: protoc-gen-p\n    out: gen\n", "exactly one of ref or local, found 2"},
		{"no out", "plugins:\n  - ref: ghcr.io/o/p:v1\n", "has no out"},
		{"ref not scalar", "plugins:\n  - ref: [a]\n    out: gen\n", "ref must be one line of text"},
		{"ref block", "plugins:\n  - ref: |\n      ghcr.io/o/p:v1\n    out: gen\n", "ref must be one line of text"},
		{"local block", "plugins:\n  - local: |\n      bin/gen\n    out: gen\n", "local must be one line of text or a list of them"},
		{"local mapping", "plugins:\n  - local: {cmd: gen}\n    out: gen\n", "local must be one line of text or a list of them"},
		{"local empty list", "plugins:\n  - local: []\n    out: gen\n", "local names no command"},
		{"local list block element", "plugins:\n  - local:\n      - gen\n      - |\n        a\n        b\n    out: gen\n", "local[1] must be one line of text"},
		{"local list nested", "plugins:\n  - local: [gen, [a]]\n    out: gen\n", "local[1] must be one line of text"},
		{"local list bad command", "plugins:\n  - local: [./, a]\n    out: gen\n", "not a clean path"},
		{"local list empty command", "plugins:\n  - local: [\"\", a]\n    out: gen\n", "empty local value"},
		{"files not list", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    files: a/*.proto\n", "files must be a list"},
		{"files empty", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    files: []\n", "files names no pattern"},
		{"files empty pattern", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    files: [\"\"]\n", "files[0] is empty"},
		{"files block element", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    files:\n      - |\n        a\n        b\n", "files[0] must be one line"},
		{"files bad glob", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    files: [\"a/[x\"]\n", "files[0]:"},
		{"include_imports word", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    include_imports: yes\n", "include_imports must be true or false"},
		{"include_imports list", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    include_imports: [true]\n", "include_imports must be true or false"},
		{"include_wkt word", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    include_imports: true\n    include_wkt: yes\n", "include_wkt must be true or false"},
		{"include_wkt alone", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    include_wkt: true\n", "include_wkt without include_imports"},
		{"include_wkt beside false", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    include_imports: false\n    include_wkt: true\n", "include_wkt without include_imports"},
		{"entry clean word", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    clean: True\n", "plugins[0].clean must be true or false"},
		{"clean number", "clean: 1\n" + ok, "clean must be true or false"},
		{"clean capital", "clean: True\n" + ok, "clean must be true or false"},
		{"out not scalar", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: {a: b}\n", "out must be one line of text"},
		{"out block", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: |\n      gen\n", "out must be one line of text"},
		{"out broken", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: \"gen\\nx\"\n", "out must be one line of text"},
		{"opt not scalar", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    opt: [x]\n", "opt must be a scalar"},
		{"ref digest", "plugins:\n  - ref: ghcr.io/o/p@sha256:abc\n    out: gen\n", "carries a digest"},
		{"ref no registry", "plugins:\n  - ref: protoc-gen-go:v1\n    out: gen\n", "has no registry"},
		{"ref short registry", "plugins:\n  - ref: library/p:v1\n    out: gen\n", "does not name itself unambiguously"},
		{"ref no tag", "plugins:\n  - ref: ghcr.io/o/p\n    out: gen\n", "has no tag"},
		{"ref port no tag", "plugins:\n  - ref: localhost:5000/p\n    out: gen\n", "has no tag"},
		{"ref uppercase repo", "plugins:\n  - ref: ghcr.io/oRg/p:v1\n    out: gen\n", `contains 'R'`},
		{"ref uppercase repo edge", "plugins:\n  - ref: ghcr.io/Org/p:v1\n    out: gen\n", "start and end alphanumeric"},
		{"local dot-slash only", "plugins:\n  - local: ./\n    out: gen\n", "not a clean path"},
		{"ref empty component", "plugins:\n  - ref: ghcr.io/o//p:v1\n    out: gen\n", "empty repository component"},
		{"ref bad tag", "plugins:\n  - ref: ghcr.io/o/p:v1+build\n    out: gen\n", `contains '+'`},
		{"ref tag leading dot", "plugins:\n  - ref: ghcr.io/o/p:.v1\n    out: gen\n", "must start with"},
		{"ref registry bad char", "plugins:\n  - ref: ghcr_io/o/p:v1\n    out: gen\n", "label"},
		{"ref registry empty port", "plugins:\n  - ref: ghcr.io:/o/p:v1\n    out: gen\n", "empty port"},
		{"ref registry alpha port", "plugins:\n  - ref: ghcr.io:x/o/p:v1\n    out: gen\n", "non-numeric port"},
		{"ref component separators", "plugins:\n  - ref: ghcr.io/o/p..q:v1\n    out: gen\n", "invalid separator run"},
		{"ref triple underscore", "plugins:\n  - ref: ghcr.io/o/p___q:v1\n    out: gen\n", "separator run"},
		{"ref mixed separator run", "plugins:\n  - ref: ghcr.io/o/p-_q:v1\n    out: gen\n", "adjacent separators"},
		{"ref empty label", "plugins:\n  - ref: a..b/p:v1\n    out: gen\n", "label"},
		{"ref label leading hyphen", "plugins:\n  - ref: a.-b/p:v1\n    out: gen\n", "label"},
		{"ref label double hyphen", "plugins:\n  - ref: a--b.io/p:v1\n    out: gen\n", "single hyphens"},
		{"ref port too large", "plugins:\n  - ref: host:99999/p:v1\n    out: gen\n", "beyond 65535"},
		{"ref port zero", "plugins:\n  - ref: host:0/p:v1\n    out: gen\n", "port 0"},
		{"ref uppercase host", "plugins:\n  - ref: GHCR.io/p:v1\n    out: gen\n", "label"},
		{"ref ipv6 host", "plugins:\n  - ref: \"[::1]:5000/p:v1\"\n    out: gen\n", "label"},
		{"local dot", "plugins:\n  - local: .\n    out: gen\n", "names no program"},
		{"local dotdot", "plugins:\n  - local: ..\n    out: gen\n", "names no program"},
		{"override unclosed extension", ok + "overrides:\n  - files: a\n    option: (a.b\n    value: v\n", "unclosed extension"},
		{"override bad extension", ok + "overrides:\n  - files: a\n    option: (a..b).c\n    value: v\n", "invalid extension"},
		{"override trailing dot", ok + "overrides:\n  - files: a\n    option: a.\n    value: v\n", "not a protobuf option name"},
		{"override extension no dot", ok + "overrides:\n  - files: a\n    option: (a.b)c\n    value: v\n", "not a protobuf option name"},
		{"ref trailing separator", "plugins:\n  - ref: ghcr.io/o/p-:v1\n    out: gen\n", "start and end alphanumeric"},
		{"local empty", "plugins:\n  - local: \"\"\n    out: gen\n", "empty local value"},
		{"local backslash", "plugins:\n  - local: tools\\\\p\n    out: gen\n", "forward slashes"},
		{"local unclean", "plugins:\n  - local: ./tools//p\n    out: gen\n", "not a clean path"},
		{"out absolute", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: /gen\n", "is absolute"},
		{"out escapes", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: ../gen\n", "escapes the resolution root"},
		{"out dotdot exact", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: ..\n", "escapes the resolution root"},
		{"out unclean", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen/\n", "not a clean path"},
		{"out backslash", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\\\\go\n", "forward slashes"},
		{"out empty", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: \"\"\n", "out: empty"},
		{"overrides not list", ok + "overrides: {}\n", "overrides must be a list"},
		{"override not mapping", ok + "overrides:\n  - x\n", "must be a mapping"},
		{"override unknown key", ok + "overrides:\n  - files: a\n    option: o\n    value: v\n    extra: 1\n", `unknown key "extra"`},
		{"override missing value", ok + "overrides:\n  - files: a\n    option: o\n", "missing value, or a prefix or suffix"},
		{"override value and prefix", ok + "overrides:\n  - files: a\n    option: go_package\n    value: v\n    prefix: p\n", "carries a value and a derivation"},
		{"override value and empty suffix", ok + "overrides:\n  - files: a\n    option: java_package\n    value: v\n    suffix: \"\"\n", "suffix is empty"},
		{"override empty prefix", ok + "overrides:\n  - files: a\n    option: go_package\n    prefix: \"\"\n", "prefix is empty"},
		{"override prefix and empty suffix", ok + "overrides:\n  - files: a\n    option: java_package\n    prefix: p\n    suffix: \"\"\n", "suffix is empty"},
		{"override derivation unknown option", ok + "overrides:\n  - files: a\n    option: optimize_for\n    prefix: p\n", "has no derivation rule"},
		{"override go suffix", ok + "overrides:\n  - files: a\n    option: go_package\n    suffix: s\n", "derives from no suffix"},
		{"override ruby prefix", ok + "overrides:\n  - files: a\n    option: ruby_package\n    prefix: p\n", "derives from no prefix"},
		{"override csharp suffix", ok + "overrides:\n  - files: a\n    option: csharp_namespace\n    suffix: s\n", "derives from no suffix"},
		{"override php prefix", ok + "overrides:\n  - files: a\n    option: php_metadata_namespace\n    prefix: p\n", "derives from no prefix"},
		{"override prefix block", ok + "overrides:\n  - files: a\n    option: go_package\n    prefix: |\n      p\n", "prefix must be one line of text"},
		{"override extension derivation", ok + "overrides:\n  - files: a\n    option: (a.b)\n    prefix: p\n", "has no derivation rule"},
		{"override missing files", ok + "overrides:\n  - option: o\n    value: v\n", "overrides[0]: missing files"},
		{"override non-scalar", ok + "overrides:\n  - files: [a]\n    option: o\n    value: v\n", "files must be one line of text"},
		{"override files block", ok + "overrides:\n  - files: |\n      a\n    option: o\n    value: v\n", "files must be one line of text"},
		{"override option block", ok + "overrides:\n  - files: a\n    option: |\n      o\n    value: v\n", "option must be one line of text"},
		{"override value list", ok + "overrides:\n  - files: a\n    option: o\n    value: [v]\n", "value must be a scalar"},
		{"override bad glob", ok + "overrides:\n  - files: \"a[\"\n    option: o\n    value: v\n", "files:"},
		{"override bad option", ok + "overrides:\n  - files: a\n    option: 1go\n    value: v\n", "not a protobuf option name"},
		{"override empty option component", ok + "overrides:\n  - files: a\n    option: a..b\n    value: v\n", "not a protobuf option name"},
		{"merge key", "plugins:\n  - <<: {ref: ghcr.io/o/p:v1}\n    out: gen\n", "merge keys"},
		{"multi-doc", ok + "---\n" + ok, "exactly one YAML document"},
	}
	for _, tc := range cases {
		_, err := Parse([]byte(tc.in))
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: err = %v, want ErrInvalid with %q", tc.name, err, tc.msg)
		}
	}
}

// Reference grammar edges accepted: registries with ports, localhost,
// nested repositories, separator runs, digits in tags.
func TestReferenceAccepts(t *testing.T) {
	for _, ref := range []string{
		"ghcr.io/o/p:v1",
		"localhost/p:v1",
		"localhost:5000/a/b/c:latest",
		"r.example.com:443/o/p__q.r-s:1.2.3_rc-1",
		"ghcr.io/o/foo--bar:v1",
		"my-reg.example.com:65535/p:v1",
		"1.2.3.4/p:_v",
		"host.tld/p:" + strings.Repeat("a", 128),
	} {
		if err := CheckReference(ref); err != nil {
			t.Errorf("%s: %v", ref, err)
		}
	}
	if err := CheckReference("host.tld/p:" + strings.Repeat("a", 129)); err == nil {
		t.Error("129-character tag accepted")
	}
	for _, opt := range []string{"go_package", "java.multiple_files", "(pkg.ext)", "(pkg.ext).field", "(a.b.c).d.e", "_x1"} {
		if err := checkOptionName(opt); err != nil {
			t.Errorf("option %s: %v", opt, err)
		}
	}
}

// Property: any file Parse accepts has plugins whose out never
// escapes the root and whose scheme is exactly the key written; and
// generated references built from the grammar round-trip.
func TestReferenceGrammarProperty(t *testing.T) {
	alnum := "abcdefghijklmnopqrstuvwxyz0123456789"
	rapid.Check(t, func(rt *rapid.T) {
		comp := func(label string) string {
			// alnum with optional single separators between alnum runs
			n := rapid.IntRange(1, 4).Draw(rt, label+"-runs")
			var b strings.Builder
			for i := 0; i < n; i++ {
				b.WriteString(rapid.StringOfN(rapid.RuneFrom([]rune(alnum)), 1, 4, -1).Draw(rt, label+"-run"))
				if i < n-1 {
					b.WriteString(rapid.SampledFrom([]string{".", "-", "--", "_", "__"}).Draw(rt, label+"-sep"))
				}
			}
			return b.String()
		}
		host := rapid.SampledFrom([]string{"localhost", "ghcr.io", "r.example.com:5000", "10.0.0.1"}).Draw(rt, "host")
		depth := rapid.IntRange(1, 3).Draw(rt, "depth")
		parts := make([]string, depth)
		for i := range parts {
			parts[i] = comp("comp")
		}
		tag := rapid.StringOfN(rapid.RuneFrom([]rune(alnum+"_")), 1, 1, -1).Draw(rt, "tag0") +
			rapid.StringOfN(rapid.RuneFrom([]rune(alnum+"_.-ABC")), 0, 20, -1).Draw(rt, "tag")
		ref := host + "/" + strings.Join(parts, "/") + ":" + tag
		if err := CheckReference(ref); err != nil {
			rt.Fatalf("grammar-built ref rejected: %s: %v", ref, err)
		}
		in := "plugins:\n  - ref: " + ref + "\n    out: gen\n"
		f, err := Parse([]byte(in))
		if err != nil || f.Plugins[0].Ref != ref || f.Plugins[0].Scheme != plugin.SchemeOCI {
			rt.Fatalf("round trip: %v %+v", err, f)
		}
	})
}

// FuzzParse: never panics; accepted files satisfy the structural
// guarantees (scheme written, out contained).
func FuzzParse(f *testing.F) {
	f.Add([]byte("plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n"))
	f.Add([]byte("plugins:\n  - local: p\n    out: .\noverrides:\n  - files: '**'\n    option: a.b\n    value: 1\n"))
	f.Add([]byte("plugins:\n  - local: [go, tool, protoc-gen-x, \"\", \"a b\"]\n    out: gen\n"))
	f.Add([]byte("plugins:\n  - local:\n      - ./tools/gen\n      - --flag\n    out: gen\n  - local: [p]\n    out: .\n"))
	f.Add([]byte("clean: true\nplugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    files: [\"a/**\", \"*.proto\"]\n    include_imports: true\n"))
	f.Add([]byte("plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    include_imports: true\n    include_wkt: true\n    clean: true\n"))
	f.Add([]byte("plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n    include_wkt: true\n"))
	f.Add([]byte("plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\noverrides:\n  - files: '**'\n    option: go_package\n    prefix: example.com/x\n  - files: '**'\n    option: java_package\n    suffix: pb\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := Parse(data)
		if err != nil {
			return
		}
		if len(parsed.Plugins) == 0 {
			t.Fatal("accepted a file with no plugins")
		}
		// What is accepted is rendered, and read back as itself
		// (REQ-gen-emission).
		out, err := Encode(parsed)
		if err != nil {
			t.Fatalf("accepted a file Encode refuses: %v", err)
		}
		again, err := Parse(out)
		if err != nil || !slices.EqualFunc(parsed.Plugins, again.Plugins, Plugin.Equal) || !slices.Equal(parsed.Overrides, again.Overrides) || parsed.Clean != again.Clean {
			t.Fatalf("the rendering reads as a different file: %v\n%s", err, out)
		}
		for _, p := range parsed.Plugins {
			if !plugin.ValidScheme(p.Scheme) || p.Ref == "" || p.Out == "" {
				t.Fatalf("accepted malformed plugin %+v", p)
			}
			if p.Scheme == plugin.SchemeOCI && p.Args != nil {
				t.Fatalf("an oci entry with arguments %+v", p)
			}
			if p.IncludeWKT && !p.IncludeImports {
				t.Fatalf("accepted include_wkt without include_imports %+v", p)
			}
			for _, a := range p.Args {
				if strings.ContainsAny(a, "\n\r") {
					t.Fatalf("accepted a multi-line argument %+v", p)
				}
			}
			for _, pat := range p.Files {
				if pat == "" || strings.ContainsAny(pat, "\n\r") {
					t.Fatalf("accepted a malformed pattern %+v", p)
				}
			}
			if p.Files != nil && len(p.Files) == 0 {
				t.Fatalf("accepted an empty files list %+v", p)
			}
			if strings.HasPrefix(p.Out, "/") || p.Out == ".." || strings.HasPrefix(p.Out, "../") {
				t.Fatalf("accepted escaping out %q", p.Out)
			}
		}
		for _, o := range parsed.Overrides {
			if o.Derived() && (o.Value != "" || CheckDerivation(o) != nil) {
				t.Fatalf("accepted a malformed derived override %+v", o)
			}
			if o.Option == "" || o.Files == "" {
				t.Fatalf("accepted an override naming no option or files %+v", o)
			}
		}
	})
}

// The generation file renders canonically (REQ-gen-emission): plugins
// then overrides, entries in the order given, keys in their order,
// opt absent where empty, scalars spelled by the shared rule, a
// multi-line opt or value double-quoted; the rendering reads back as
// the file; what Parse rejects Encode refuses.
func TestEncode(t *testing.T) {
	f := &File{
		Clean: true,
		Plugins: []Plugin{
			{Scheme: plugin.SchemeOCI, Ref: "ghcr.io/acme/protoc-gen-go:v1.36.0", Out: "gen/go", Opt: "paths=source_relative,module=example.com/x", Files: []string{"acme/v1/*.proto", "**"}, IncludeImports: true, IncludeWKT: true, Clean: true},
			{Scheme: plugin.SchemeLocal, Ref: "protoc-gen-connect-go", Out: "gen/connect", Clean: true},
			{Scheme: plugin.SchemeLocal, Ref: "tools/gen", Out: "gen/x", Opt: "a=1\nb=2\n"},
			{Scheme: plugin.SchemeLocal, Ref: "go", Args: []string{"tool", "protoc-gen-go-grpc", "--x=1 2", "true", ""}, Out: "gen/grpc"},
		},
		Overrides: []Override{
			{Files: "**", Option: "java_package", Value: "com.acme"},
			{Files: "acme/*.proto", Option: "(pkg.ext).field", Value: "true"},
			{Files: "**", Option: "go_package", Prefix: "example.com/gen"},
			{Files: "**", Option: "java_package", Prefix: "com.acme", Suffix: "proto"},
			{Files: "**", Option: "php_metadata_namespace", Suffix: "PB\\Meta"},
		},
	}
	out, err := Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	want := `clean: true
plugins:
  - ref: ghcr.io/acme/protoc-gen-go:v1.36.0
    out: gen/go
    opt: paths=source_relative,module=example.com/x
    files:
      - acme/v1/*.proto
      - "**"
    include_imports: true
    include_wkt: true
    clean: true
  - local: protoc-gen-connect-go
    out: gen/connect
    clean: true
  - local: tools/gen
    out: gen/x
    opt: "a=1\nb=2\n"
  - local:
      - go
      - tool
      - protoc-gen-go-grpc
      - --x=1 2
      - "true"
      - ""
    out: gen/grpc
overrides:
  - files: "**"
    option: java_package
    value: com.acme
  - files: acme/*.proto
    option: (pkg.ext).field
    value: "true"
  - files: "**"
    option: go_package
    prefix: example.com/gen
  - files: "**"
    option: java_package
    prefix: com.acme
    suffix: proto
  - files: "**"
    option: php_metadata_namespace
    suffix: PB\Meta
`
	if string(out) != want {
		t.Fatalf("Encode:\n%s", out)
	}
	again, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if twice, err := Encode(again); err != nil || string(twice) != want {
		t.Fatalf("round trip: %v\n%s", err, twice)
	}
	if out, err := Encode(&File{Plugins: []Plugin{{Scheme: plugin.SchemeOCI, Ref: "ghcr.io/a/b:v1", Out: "gen"}}}); err != nil || string(out) != "plugins:\n  - ref: ghcr.io/a/b:v1\n    out: gen\n" {
		t.Fatalf("one plugin, no overrides: %q %v", out, err)
	}
	// Entries stay in the order given: generation runs them in
	// declaration order, and later overrides win on overlap.
	ordered := &File{
		Plugins:   []Plugin{{Scheme: plugin.SchemeLocal, Ref: "z-gen", Out: "gen/z"}, {Scheme: plugin.SchemeOCI, Ref: "ghcr.io/a/b:v1", Out: "gen/a"}},
		Overrides: []Override{{Files: "z/**", Option: "java_package", Value: "z"}, {Files: "**", Option: "java_package", Value: "a"}},
	}
	if out, err := Encode(ordered); err != nil || string(out) != "plugins:\n  - local: z-gen\n    out: gen/z\n  - ref: ghcr.io/a/b:v1\n    out: gen/a\noverrides:\n  - files: z/**\n    option: java_package\n    value: z\n  - files: \"**\"\n    option: java_package\n    value: a\n" {
		t.Fatalf("the order given: %q %v", out, err)
	}
	for name, f := range map[string]*File{
		"nil":            nil,
		"no plugins":     {},
		"no out":         {Plugins: []Plugin{{Scheme: plugin.SchemeOCI, Ref: "ghcr.io/a/b:v1"}}},
		"no tag":         {Plugins: []Plugin{{Scheme: plugin.SchemeOCI, Ref: "ghcr.io/a/b", Out: "gen"}}},
		"escaping out":   {Plugins: []Plugin{{Scheme: plugin.SchemeOCI, Ref: "ghcr.io/a/b:v1", Out: "../gen"}}},
		"bad option":     {Plugins: []Plugin{{Scheme: plugin.SchemeOCI, Ref: "ghcr.io/a/b:v1", Out: "gen"}}, Overrides: []Override{{Files: "**", Option: "not an option", Value: "v"}}},
		"unknown scheme": {Plugins: []Plugin{{Scheme: "remote", Ref: "x", Out: "gen"}}},
		"wkt alone":      {Plugins: []Plugin{{Scheme: plugin.SchemeOCI, Ref: "ghcr.io/a/b:v1", Out: "gen", IncludeWKT: true}}},
	} {
		if _, err := Encode(f); err == nil {
			t.Errorf("%s: encoded", name)
		}
	}
}

// A derived override spells each file's value from its declared
// prefix or suffix and the file's own path and package, exactly by
// the rules stated, and assigns nothing to a file declaring no
// package where the rule reads it (REQ-gen-overrides-derived).
func TestDerive(t *testing.T) {
	cases := []struct {
		o         Override
		path, pkg string
		want      string
		ok        bool
	}{
		{Override{Option: "go_package", Prefix: "example.com/gen"}, "acme/foo/v1/foo.proto", "acme.foo.v1", "example.com/gen/acme/foo/v1;foov1", true},
		{Override{Option: "go_package", Prefix: "example.com/gen"}, "acme/foo/v1beta1/foo.proto", "acme.foo.v1beta1", "example.com/gen/acme/foo/v1beta1;foov1beta1", true},
		{Override{Option: "go_package", Prefix: "example.com/gen"}, "acme/foo/foo.proto", "acme.foo", "example.com/gen/acme/foo", true},
		{Override{Option: "go_package", Prefix: "example.com/gen"}, "x.proto", "v1", "example.com/gen", true},
		{Override{Option: "go_package", Prefix: "example.com/gen"}, "x.proto", "", "example.com/gen", true},
		{Override{Option: "go_package", Prefix: "example.com/gen"}, "a/x.proto", "a.version1", "example.com/gen/a", true},
		{Override{Option: "go_package", Prefix: "example.com/gen/"}, "a/x.proto", "a", "example.com/gen/a", true},
		{Override{Option: "go_package", Prefix: "example.com/gen/"}, "x.proto", "", "example.com/gen", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v1alpha", "g/a;av1alpha", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v1beta", "g/a;av1beta", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v1test", "g/a;av1test", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v1testfoo", "g/a;av1testfoo", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v1p1alpha1", "g/a;av1p1alpha1", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v2beta3", "g/a;av2beta3", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v01", "g/a;av01", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v0", "g/a", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v1p1", "g/a", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v1beta0", "g/a", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v1alphabeta", "g/a", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v", "g/a", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v2147483647", "g/a;av2147483647", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v2147483648", "g/a", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v1beta2147483648", "g/a", true},
		{Override{Option: "go_package", Prefix: "g"}, "a/x.proto", "a.v1x", "g/a", true},
		{Override{Option: "java_package", Prefix: "com.acme"}, "x.proto", "acme.foo.v1", "com.acme.acme.foo.v1", true},
		{Override{Option: "java_package", Suffix: "proto"}, "x.proto", "acme.foo", "acme.foo.proto", true},
		{Override{Option: "java_package", Prefix: "com", Suffix: "pb"}, "x.proto", "acme", "com.acme.pb", true},
		{Override{Option: "java_package", Prefix: "com"}, "x.proto", "", "", false},
		{Override{Option: "csharp_namespace", Prefix: "Acme.Gen"}, "x.proto", "acme.foo.v1", "Acme.Gen.Acme.Foo.V1", true},
		{Override{Option: "csharp_namespace", Prefix: "Acme"}, "x.proto", "", "", false},
		{Override{Option: "csharp_namespace", Prefix: "Acme"}, "x.proto", "acme.foo_bar.v1", "Acme.Acme.FooBar.V1", true},
		{Override{Option: "csharp_namespace", Prefix: "Acme"}, "x.proto", "acme.fooBAR._x_", "Acme.Acme.FooBAR.X", true},
		{Override{Option: "php_metadata_namespace", Suffix: "Meta"}, "x.proto", "acme.list.v1", `Acme\List_\V1\Meta`, true},
		{Override{Option: "php_metadata_namespace", Suffix: "Meta"}, "x.proto", "acme.Class.v1", `Acme\Class_\V1\Meta`, true},
		{Override{Option: "ruby_package", Suffix: "Proto"}, "x.proto", "acme.foo_bar", "Acme::FooBar::Proto", true},
		{Override{Option: "java_package"}, "x.proto", "acme", "", false},
		{Override{Option: "php_metadata_namespace", Suffix: "Meta"}, "x.proto", "acme.foo.v1", `Acme\Foo\V1\Meta`, true},
		{Override{Option: "ruby_package", Suffix: "Proto"}, "x.proto", "acme.foo.v1", "Acme::Foo::V1::Proto", true},
		{Override{Option: "ruby_package", Suffix: "Proto"}, "x.proto", "", "", false},
		{Override{Option: "optimize_for", Prefix: "x"}, "x.proto", "a", "", false},
	}
	for _, c := range cases {
		got, ok := c.o.Derive(c.path, c.pkg)
		if got != c.want || ok != c.ok {
			t.Errorf("Derive(%+v, %q, %q) = %q %v, want %q %v", c.o, c.path, c.pkg, got, ok, c.want, c.ok)
		}
	}
}
