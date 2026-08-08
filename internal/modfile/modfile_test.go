package modfile

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"pgregory.net/rapid"

	"github.com/greatliontech/pb/internal/modpath"
)

func TestParseGolden(t *testing.T) {
	data := []byte("module: example.com/m\ndeps:\n  example.com/a: v1.2.3\n  example.com/b: v0.1.0-rc.1\n")
	f, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if f.Module != "example.com/m" || len(f.Deps) != 2 || f.Deps["example.com/a"] != "v1.2.3" || f.Deps["example.com/b"] != "v0.1.0-rc.1" {
		t.Fatalf("parsed %+v", f)
	}
	// No deps.
	f, err = Parse([]byte("module: example.com/solo\n"))
	if err != nil || f.Module != "example.com/solo" || f.Deps != nil {
		t.Fatalf("solo: %+v, %v", f, err)
	}
}

func TestParseRejections(t *testing.T) {
	cases := []struct{ name, in string }{
		{"empty", ""},
		{"missing module", "deps:\n  example.com/a: v1.0.0\n"},
		{"unknown key", "module: example.com/m\nextra: 1\n"},
		{"duplicate key", "module: example.com/m\nmodule: example.com/n\n"},
		{"wrong module type", "module: [1]\n"},
		{"wrong deps type", "module: example.com/m\ndeps: [a]\n"},
		{"empty deps", "module: example.com/m\ndeps: {}\n"},
		{"invalid module path", "module: nodot/x\n"},
		{"invalid dep path", "module: example.com/m\ndeps:\n  nodot: v1.0.0\n"},
		{"invalid version", "module: example.com/m\ndeps:\n  example.com/a: 1.0.0\n"},
		{"short version", "module: example.com/m\ndeps:\n  example.com/a: v1.2\n"},
		{"build metadata", "module: example.com/m\ndeps:\n  example.com/a: v1.0.0+meta\n"},
		{"multi-document stream", "module: a.b/x\n---\nmodule: evil.com/y\n"},
		{"null deps", "module: example.com/m\ndeps:\n"},
		{"explicit null deps", "module: example.com/m\ndeps: null\n"},
		{"merge key top level", "module: example.com/m\n<<: {x: y}\n"},
		{"merge key in deps", "module: example.com/m\ndeps:\n  <<: {example.com/a: v1.0.0}\n"},
		{"top level sequence", "- module: example.com/m\n"},
	}
	for _, tc := range cases {
		if _, err := Parse([]byte(tc.in)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", tc.name, err)
		}
	}
	// Guard-specific messages are load-bearing: the missing-module and
	// empty-deps rejections come from their own checks, not from a
	// downstream validator with a different message.
	_, err := Parse([]byte("deps:\n  example.com/a: v1.0.0\n"))
	if err == nil || !strings.Contains(err.Error(), "missing module key") {
		t.Errorf("missing module: err %v does not name the guard", err)
	}
	_, err = Parse([]byte("module: example.com/m\ndeps: {}\n"))
	if err == nil || !strings.Contains(err.Error(), "deps must be a non-empty mapping") {
		t.Errorf("empty deps: err %v does not name the guard", err)
	}
	_, err = Parse([]byte("module: a.b/x\n---\nmodule: evil.com/y\n"))
	if err == nil || !strings.Contains(err.Error(), "exactly one YAML document") {
		t.Errorf("multi-doc: err %v does not name the guard", err)
	}
	_, err = Parse([]byte("module: example.com/m\ndeps:\n  <<: {example.com/a: v1.0.0}\n"))
	if err == nil || !strings.Contains(err.Error(), "merge keys") {
		t.Errorf("merge key: err %v does not name the guard", err)
	}
	_, err = Parse([]byte("module: example.com/m\n<<: {x: y}\n"))
	if err == nil || !strings.Contains(err.Error(), "merge keys") {
		t.Errorf("top-level merge key: err %v does not name the guard", err)
	}
	_, err = Parse([]byte("---\n"))
	if err == nil || !strings.Contains(err.Error(), "missing module key") {
		t.Errorf("empty document: err %v does not name the guard", err)
	}
	_, err = Parse([]byte("module: example.com/m\nextra: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("unknown key: err %v does not name the guard", err)
	}
	_, err = Parse([]byte("module: [1]\n"))
	if err == nil || !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("wrong module type: err %v does not name the decode failure", err)
	}
	// An empty version string is rejected by the validity operand, not the
	// canonical-equality one (Canonical("") equals "").
	_, err = Parse([]byte("module: example.com/m\ndeps:\n  example.com/a: \"\"\n"))
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("empty version: err = %v, want ErrInvalid", err)
	}
}

func TestEncodeGolden(t *testing.T) {
	f := &File{
		Module: "example.com/m",
		Deps: map[string]string{
			"example.com/b": "v0.1.0",
			"example.com/a": "v1.2.3",
		},
	}
	out, err := Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	want := "module: example.com/m\ndeps:\n  example.com/a: v1.2.3\n  example.com/b: v0.1.0\n"
	if string(out) != want {
		t.Fatalf("encoded:\n%s\nwant:\n%s", out, want)
	}
	solo, err := Encode(&File{Module: "example.com/solo"})
	if err != nil || string(solo) != "module: example.com/solo\n" {
		t.Fatalf("solo: %q, %v", solo, err)
	}
	if _, err := Encode(&File{Module: "nodot"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid module encode: %v", err)
	}
	if _, err := Encode(&File{Module: "example.com/m", Deps: map[string]string{"example.com/a": "bad"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid version encode: %v", err)
	}
	if _, err := Encode(&File{Module: "example.com/m", Deps: map[string]string{"nodot": "v1.0.0"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid dep path encode: %v", err)
	}
	if _, err := Encode(&File{Module: "example.com/m", Deps: map[string]string{}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty deps map encode: %v", err)
	}
}

func TestCheckIdentity(t *testing.T) {
	f := &File{Module: "example.com/m"}
	if err := CheckIdentity(f, "example.com/m"); err != nil {
		t.Fatal(err)
	}
	err := CheckIdentity(f, "example.com/other")
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("err = %v, want ErrIdentityMismatch", err)
	}
	for _, s := range []string{"example.com/m", "example.com/other"} {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("error %q does not name %q", err, s)
		}
	}
}

// Identity property: any two distinct valid paths mismatch, and every path
// matches itself.
func TestCheckIdentityProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := "example.com/" + string(rapid.SliceOfN(rapid.SampledFrom([]rune("abcdefgh")), 1, 8).Draw(t, "a"))
		b := "example.com/" + string(rapid.SliceOfN(rapid.SampledFrom([]rune("abcdefgh")), 1, 8).Draw(t, "b"))
		if err := CheckIdentity(&File{Module: a}, a); err != nil {
			t.Fatalf("self mismatch: %v", err)
		}
		err := CheckIdentity(&File{Module: a}, b)
		if (a == b) != (err == nil) {
			t.Fatalf("identity check disagrees with equality: %q vs %q -> %v", a, b, err)
		}
	})
}

// Round-trip property: for any generated valid file, Encode is
// deterministic and parseable, and Parse(Encode(f)) reproduces f —
// canonical emission is a fixed point.
func TestRoundTripProperty(t *testing.T) {
	segAlpha := []rune("abcdefghijklmnopqrstuvwxyz0123456789-_~")
	rapid.Check(t, func(t *rapid.T) {
		path := func(label string) string {
			return "example.com/" + string(rapid.SliceOfN(rapid.SampledFrom(segAlpha), 1, 10).Draw(t, label))
		}
		f := &File{Module: path("module")}
		n := rapid.IntRange(0, 6).Draw(t, "n")
		if n > 0 {
			f.Deps = make(map[string]string, n)
			for i := range n {
				v := fmt.Sprintf("v%d.%d.%d",
					rapid.IntRange(0, 9).Draw(t, "maj"),
					rapid.IntRange(0, 9).Draw(t, "min"),
					rapid.IntRange(0, 9).Draw(t, "pat"))
				if rapid.Bool().Draw(t, "pre") {
					v += "-rc." + fmt.Sprint(rapid.IntRange(0, 9).Draw(t, "rcn"))
				}
				f.Deps[fmt.Sprintf("example.com/d%d/%s", i, string(rapid.SliceOfN(rapid.SampledFrom(segAlpha), 1, 6).Draw(t, "dseg")))] = v
			}
		}
		out1, err := Encode(f)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		parsed, err := Parse(out1)
		if err != nil {
			t.Fatalf("Parse(Encode): %v\n%s", err, out1)
		}
		if parsed.Module != f.Module || len(parsed.Deps) != len(f.Deps) {
			t.Fatalf("round trip lost data: %+v vs %+v", parsed, f)
		}
		for p, v := range f.Deps {
			if parsed.Deps[p] != v {
				t.Fatalf("dep %q: %q vs %q", p, parsed.Deps[p], v)
			}
		}
		out2, err := Encode(parsed)
		if err != nil || string(out1) != string(out2) {
			t.Fatalf("emission is not a fixed point:\n%s\nvs\n%s (%v)", out1, out2, err)
		}
	})
}

// FuzzParse: never panics; success implies validated invariants and a
// re-encodable file whose emission reparses to the same content.
func FuzzParse(f *testing.F) {
	f.Add([]byte("module: example.com/m\n"))
	f.Add([]byte("module: example.com/m\ndeps:\n  example.com/a: v1.2.3\n"))
	f.Add([]byte("{}"))
	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := Parse(data)
		if err != nil {
			return
		}
		if modpath.Validate(parsed.Module) != nil {
			t.Fatalf("accepted invalid module path %q", parsed.Module)
		}
		out, err := Encode(parsed)
		if err != nil {
			t.Fatalf("accepted file fails to encode: %v", err)
		}
		again, err := Parse(out)
		if err != nil {
			t.Fatalf("canonical emission fails to parse: %v\n%s", err, out)
		}
		if again.Module != parsed.Module || len(again.Deps) != len(parsed.Deps) {
			t.Fatalf("emission changed content")
		}
	})
}

// An anchored entry can smuggle a merge key past checks that inspect only
// mapping keys: anchors, aliases, and tags are rejected anywhere in the
// module file.
func TestForbiddenYAMLConstructs(t *testing.T) {
	cases := []struct{ name, in, msg string }{
		{"anchored deps hiding merge", "module: a.b/x\ndeps: &d\n  <<: {a.b/c: v1.0.0}\n", "anchors"},
		{"anchored module value", "module: &m a.b/x\n", "anchors"},
		{"tagged module", "module: !!str a.b/x\n", "tags"},
		{"null key", "module: a.b/x\nnull: x\n", "mapping keys are strings"},
		{"integer key in deps", "module: a.b/x\ndeps:\n  7: v1.0.0\n", "mapping keys are strings"},
	}
	for _, tc := range cases {
		_, err := Parse([]byte(tc.in))
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: err = %v, want %q rejection", tc.name, err, tc.msg)
		}
	}
}

// The shape check's non-string-key fallback fails closed even though
// yamlshape rejects such keys first in the Parse pipeline: called
// directly with a non-string key, it must reject, not fall through.
func TestMappingShapeNonStringKeyFallback(t *testing.T) {
	f, err := parser.ParseBytes([]byte("7: x\n"), 0)
	if err != nil {
		t.Fatal(err)
	}
	mapping, ok := f.Docs[0].Body.(*ast.MappingNode)
	if !ok {
		t.Fatal("fixture is not a mapping")
	}
	if err := checkMappingShape(mapping); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("non-string key: err = %v, want unknown-key rejection", err)
	}
}

// Acceptance variants the wire contract admits: key order, CRLF, and
// quoting yield the same declared facts, normalized by re-emission.
func TestAcceptanceVariants(t *testing.T) {
	canonical := "module: a.b/x\ndeps:\n  a.b/c: v1.0.0\n"
	for name, in := range map[string]string{
		"key order": "deps:\n  a.b/c: v1.0.0\nmodule: a.b/x\n",
		"crlf":      strings.ReplaceAll(canonical, "\n", "\r\n"),
		"quoting":   "\"module\": \"a.b/x\"\ndeps:\n  'a.b/c': v1.0.0\n",
	} {
		f, err := Parse([]byte(in))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		out, err := Encode(f)
		if err != nil || string(out) != canonical {
			t.Errorf("%s: re-emission not canonical (%v):\n%s", name, err, out)
		}
	}
}

// A root module file declares the module; its absence synthesizes one —
// identity is the required path, no dependencies. Deeper module files
// never decide.
func TestFromFileSet(t *testing.T) {
	declared := []byte("module: example.com/protos\ndeps:\n  example.com/dep: v1.2.0\n")
	cases := map[string]struct {
		required string
		files    map[string][]byte
		want     *File
		wantErr  error
	}{
		"declared": {
			required: "example.com/protos",
			files:    map[string][]byte{ModuleFileName: declared, "a/b.proto": nil},
			want:     &File{Module: "example.com/protos", Deps: map[string]string{"example.com/dep": "v1.2.0"}},
		},
		"synthesized": {
			required: "example.com/protos",
			files:    map[string][]byte{"a/b.proto": nil, "c.proto": nil},
			want:     &File{Module: "example.com/protos"},
		},
		"synthesized empty set": {
			required: "example.com/protos",
			files:    nil,
			want:     &File{Module: "example.com/protos"},
		},
		"nested module file does not decide": {
			required: "example.com/protos",
			files:    map[string][]byte{"sub/" + ModuleFileName: declared},
			want:     &File{Module: "example.com/protos"},
		},
		"identity mismatch": {
			required: "example.com/other",
			files:    map[string][]byte{ModuleFileName: declared},
			wantErr:  ErrIdentityMismatch,
		},
		"invalid module file": {
			required: "example.com/protos",
			files:    map[string][]byte{ModuleFileName: []byte("module: [broken\n")},
			wantErr:  ErrInvalid,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := FromFileSet(tc.required, tc.files)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Module != tc.want.Module || fmt.Sprint(got.Deps) != fmt.Sprint(tc.want.Deps) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The required path is validated even on the synthesized branch: a
// synthesized module cannot exist under an invalid identity.
func TestFromFileSetRejectsInvalidRequired(t *testing.T) {
	if _, err := FromFileSet("nodot/x", nil); err == nil {
		t.Fatal("invalid required path accepted")
	}
}
