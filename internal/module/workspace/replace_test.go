package workspace

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/greatliontech/pb/internal/module/version"
)

// The workspace file's replace mapping reads back what it writes, keys
// in raw-byte order after use, and refuses every spelling the schema
// does not admit (workspace.md REQ-work-schema, REQ-work-emission,
// REQ-work-replace-names).
func TestReplaceGrammar(t *testing.T) {
	f, err := Parse([]byte("use:\n  - m\nreplace:\n  example.com/z: example.com/w@v2.0.0\n  example.com/x: example.com/y@v1.0.0-20260101000000-abcdefabcdef\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Replace["example.com/x"]; got.Path != "example.com/y" || got.Version.String() != "v1.0.0-20260101000000-abcdefabcdef" {
		t.Fatalf("replace x = %+v", got)
	}
	out, err := Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	if want := "use:\n  - m\nreplace:\n  example.com/x: example.com/y@v1.0.0-20260101000000-abcdefabcdef\n  example.com/z: example.com/w@v2.0.0\n"; string(out) != want {
		t.Fatalf("encoded = %q, want %q: use first, then replace keyed in raw-byte order, every scalar plain", out, want)
	}
	again, err := Parse(out)
	if err != nil || len(again.Replace) != 2 || again.Replace["example.com/z"].String() != "example.com/w@v2.0.0" {
		t.Fatalf("round trip = %+v, %v", again, err)
	}
	if twice, err := Encode(again); err != nil || string(twice) != string(out) {
		t.Fatalf("emission is not idempotent: %q / %q, %v", out, twice, err)
	}
	for name, body := range map[string]string{
		"empty mapping":         "use:\n  - m\nreplace: {}\n",
		"not a mapping":         "use:\n  - m\nreplace:\n  - example.com/x\n",
		"value not a scalar":    "use:\n  - m\nreplace:\n  example.com/x: [example.com/y@v1.0.0]\n",
		"no version":            "use:\n  - m\nreplace:\n  example.com/x: example.com/y\n",
		"version not canonical": "use:\n  - m\nreplace:\n  example.com/x: example.com/y@latest\n",
		"replacement bad path":  "use:\n  - m\nreplace:\n  example.com/x: Example.COM/y@v1.0.0\n",
		"replaced bad path":     "use:\n  - m\nreplace:\n  notapath: example.com/y@v1.0.0\n",
		"names itself":          "use:\n  - m\nreplace:\n  example.com/x: example.com/x@v1.0.0\n",
		"a chain":               "use:\n  - m\nreplace:\n  example.com/x: example.com/y@v1.0.0\n  example.com/y: example.com/z@v1.0.0\n",
	} {
		if _, err := Parse([]byte(body)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Parse = %v, want ErrInvalid", name, err)
		}
	}
	// Encode refuses through the same reading: a chain never renders,
	// and an empty mapping renders as no key.
	v1, err := version.Parse("v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	chain := map[string]Replacement{
		"example.com/x": {Path: "example.com/y", Version: v1},
		"example.com/y": {Path: "example.com/z", Version: v1},
	}
	if _, err := Encode(&File{Use: []string{"m"}, Replace: chain}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Encode of a chain = %v, want ErrInvalid", err)
	}
	if out, err := Encode(&File{Use: []string{"m"}, Replace: map[string]Replacement{}}); err != nil || string(out) != "use:\n  - m\n" {
		t.Fatalf("Encode of an empty mapping = %q, %v, want the file without a replace key", out, err)
	}
}

// A replacement answers for an external path with another external
// pair: neither side may be a workspace module, and only a workspace
// file carries one (REQ-work-replace-names).
func TestReplaceNamesExternalsOnly(t *testing.T) {
	mod := func(path string) *fstest.MapFile { return &fstest.MapFile{Data: []byte("module: " + path + "\n")} }
	for name, replace := range map[string]string{
		"a workspace module replaced":    "example.com/m: example.com/y@v1.0.0",
		"a workspace module as the fork": "example.com/x: example.com/m@v1.0.0",
	} {
		fsys := fstest.MapFS{
			"pb.work":   {Data: []byte("use:\n  - m\nreplace:\n  " + replace + "\n")},
			"m/pb.yaml": mod("example.com/m"),
		}
		if _, err := Load(fsys, "."); err == nil || !strings.Contains(err.Error(), "workspace module") {
			t.Errorf("%s: Load = %v, want the workspace module named", name, err)
		}
	}
	fsys := fstest.MapFS{
		"pb.work":   {Data: []byte("use:\n  - m\nreplace:\n  example.com/x: example.com/y@v1.0.0\n")},
		"m/pb.yaml": mod("example.com/m"),
	}
	root, err := Load(fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := version.Parse("v2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if src, srcV := root.Source("example.com/x", v2); src != "example.com/y" || srcV.String() != "v1.0.0" {
		t.Fatalf("Source(x@v2.0.0) = %s@%s, want the replacement for every version of x", src, srcV)
	}
	if src, srcV := root.Source("example.com/y", v2); src != "example.com/y" || srcV.String() != "v2.0.0" {
		t.Fatalf("Source(y@v2.0.0) = %s@%s, want the pair itself: the replacement is not replaced", src, srcV)
	}
	single, err := Load(fstest.MapFS{"pb.yaml": mod("example.com/solo")}, ".")
	if err != nil {
		t.Fatal(err)
	}
	if src, srcV := single.Source("example.com/x", v2); src != "example.com/x" || srcV.String() != "v2.0.0" {
		t.Fatalf("Source(x@v2.0.0) = %s@%s on a single-module root, want the pair itself", src, srcV)
	}
}
