package genfile

import (
	"errors"
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
plugins:
  - ref: ghcr.io/org/protoc-gen-go:v1.34.2
    out: gen/go
    opt: paths=source_relative
  - local: protoc-gen-lint
    out: .
  - local: ./tools/bin/protoc-gen-x
    out: gen/x
    opt: 1
overrides:
  - files: "**/*.proto"
    option: go_package
    value: example.com/gen
  - files: "api/{v1,v2}/*.proto"
    option: java_multiple_files
    value: true
`
	f, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []Plugin{
		{Scheme: plugin.SchemeOCI, Ref: "ghcr.io/org/protoc-gen-go:v1.34.2", Out: "gen/go", Opt: "paths=source_relative"},
		{Scheme: plugin.SchemeLocal, Ref: "protoc-gen-lint", Out: "."},
		{Scheme: plugin.SchemeLocal, Ref: "./tools/bin/protoc-gen-x", Out: "gen/x", Opt: "1"},
	}
	if len(f.Plugins) != len(want) {
		t.Fatalf("plugins = %+v", f.Plugins)
	}
	for i := range want {
		if f.Plugins[i] != want[i] {
			t.Errorf("plugins[%d] = %+v, want %+v", i, f.Plugins[i], want[i])
		}
	}
	wantO := []Override{
		{Files: "**/*.proto", Option: "go_package", Value: "example.com/gen"},
		{Files: "api/{v1,v2}/*.proto", Option: "java_multiple_files", Value: "true"},
	}
	if len(f.Overrides) != 2 || f.Overrides[0] != wantO[0] || f.Overrides[1] != wantO[1] {
		t.Fatalf("overrides = %+v", f.Overrides)
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
		{"empty", "", "missing plugins key"},
		{"no plugins", "overrides: []\n", "missing plugins key"},
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
		{"local block", "plugins:\n  - local: |\n      bin/gen\n    out: gen\n", "local must be one line of text"},
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
		{"override missing value", ok + "overrides:\n  - files: a\n    option: o\n", "has no value"},
		{"override missing files", ok + "overrides:\n  - option: o\n    value: v\n", "has no files"},
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
	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := Parse(data)
		if err != nil {
			return
		}
		if len(parsed.Plugins) == 0 {
			t.Fatal("accepted a file with no plugins")
		}
		for _, p := range parsed.Plugins {
			if !plugin.ValidScheme(p.Scheme) || p.Ref == "" || p.Out == "" {
				t.Fatalf("accepted malformed plugin %+v", p)
			}
			if strings.HasPrefix(p.Out, "/") || p.Out == ".." || strings.HasPrefix(p.Out, "../") {
				t.Fatalf("accepted escaping out %q", p.Out)
			}
		}
	})
}
