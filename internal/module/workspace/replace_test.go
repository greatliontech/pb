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
	if src := root.Source("example.com/x", v2); src.Module != nil || src.String() != "example.com/y@v1.0.0" || !root.Replaced("example.com/x") {
		t.Fatalf("Source(x@v2.0.0) = %s, want the replacement for every version of x", src)
	}
	if src := root.Source("example.com/y", v2); src.Module != nil || src.String() != "example.com/y@v2.0.0" || root.Replaced("example.com/y") {
		t.Fatalf("Source(y@v2.0.0) = %s, want the pair itself: the replacement is not replaced", src)
	}
	single, err := Load(fstest.MapFS{"pb.yaml": mod("example.com/solo")}, ".")
	if err != nil {
		t.Fatal(err)
	}
	if src := single.Source("example.com/x", v2); src.Module != nil || src.String() != "example.com/x@v2.0.0" || single.Replaced("example.com/x") {
		t.Fatalf("Source(x@v2.0.0) = %s on a single-module root, want the pair itself", src)
	}
}

// The directory form: `./dir` reads and writes as a root-contained
// directory, distinct from a pair by its leading dot, and refuses
// every spelling the schema does not admit (REQ-work-schema,
// REQ-work-emission, REQ-work-replace-names).
func TestReplaceDirectoryGrammar(t *testing.T) {
	f, err := Parse([]byte("use:\n  - m\nreplace:\n  example.com/x: ./forks/./x/\n  example.com/w: ./.\n  example.com/v: .\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Replace["example.com/x"]; got.Dir != "forks/x" || got.Path != "" || got.String() != "./forks/x" {
		t.Fatalf("replace x = %+v", got)
	}
	for _, root := range []string{"example.com/w", "example.com/v"} {
		if got := f.Replace[root]; got.Dir != "." || got.String() != "." {
			t.Fatalf("replace %s = %+v, %q: want the root, spelled .", root, got, got.String())
		}
	}
	out, err := Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	if want := "use:\n  - m\nreplace:\n  example.com/v: .\n  example.com/w: .\n  example.com/x: ./forks/x\n"; string(out) != want {
		t.Fatalf("encoded = %q, want %q: every directory cleaned, the root spelled .", out, want)
	}
	again, err := Parse(out)
	if err != nil || again.Replace["example.com/x"].Dir != "forks/x" || again.Replace["example.com/w"].Dir != "." {
		t.Fatalf("round trip = %+v, %v", again, err)
	}
	if twice, err := Encode(again); err != nil || string(twice) != string(out) {
		t.Fatalf("emission is not idempotent: %q / %q, %v", out, twice, err)
	}
	for name, body := range map[string]string{
		"escapes the root":        "use:\n  - m\nreplace:\n  example.com/x: ./../x\n",
		"parent directory":        "use:\n  - m\nreplace:\n  example.com/x: ../x\n",
		"absolute":                "use:\n  - m\nreplace:\n  example.com/x: /x\n",
		"bare relative directory": "use:\n  - m\nreplace:\n  example.com/x: forks/x\n",
	} {
		if _, err := Parse([]byte(body)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Parse = %v, want ErrInvalid", name, err)
		}
	}
	// A directory replacement is no chain: the replaced path may be
	// another replacement's pair path only through the pair form.
	if _, err := Parse([]byte("use:\n  - m\nreplace:\n  example.com/x: ./forks/x\n  example.com/y: ./forks/y\n")); err != nil {
		t.Fatalf("two directory replacements: %v", err)
	}
}

// A directory replacement is read at Load as a workspace module is:
// it holds a module file, it is no workspace module's directory, and
// Source hands every version of the replaced path the directory's
// module file (REQ-work-replace-dir, REQ-work-replace-names).
func TestReplaceDirectoryLoads(t *testing.T) {
	mod := func(path, deps string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte("module: " + path + "\n" + deps)}
	}
	for name, tc := range map[string]struct {
		files fstest.MapFS
		want  string
	}{
		"no module file": {fstest.MapFS{
			"pb.work":         {Data: []byte("use:\n  - m\nreplace:\n  example.com/x: ./forks/x\n")},
			"m/pb.yaml":       mod("example.com/m", ""),
			"forks/x/x.proto": {Data: []byte("syntax = \"proto3\";\n")},
		}, "not a declared module root"},
		"a workspace module's directory": {fstest.MapFS{
			"pb.work":   {Data: []byte("use:\n  - m\nreplace:\n  example.com/x: ./m\n")},
			"m/pb.yaml": mod("example.com/m", ""),
		}, "workspace module's directory"},
		"an invalid module file": {fstest.MapFS{
			"pb.work":         {Data: []byte("use:\n  - m\nreplace:\n  example.com/x: ./forks/x\n")},
			"m/pb.yaml":       mod("example.com/m", ""),
			"forks/x/pb.yaml": {Data: []byte("module: Not A Path\n")},
		}, "module at ./forks/x"},
	} {
		if _, err := Load(tc.files, "."); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Load = %v, want %q", name, err, tc.want)
		}
	}
	fsys := fstest.MapFS{
		"pb.work":         {Data: []byte("use:\n  - m\nreplace:\n  example.com/x: ./forks/x\n")},
		"m/pb.yaml":       mod("example.com/m", "deps:\n  example.com/x: v1.0.0\n"),
		"forks/x/pb.yaml": mod("example.com/x", "deps:\n  example.com/z: v1.0.0\n"),
	}
	root, err := Load(fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := version.Parse("v2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	src := root.Source("example.com/x", v2)
	if src.Module == nil || src.Module.Dir != "forks/x" || src.Module.File.Deps["example.com/z"] != "v1.0.0" || src.String() != "./forks/x" || !root.Replaced("example.com/x") {
		t.Fatalf("Source(x@v2.0.0) = %+v, want the directory read as a workspace module is, for every version of x", src)
	}
	if len(root.Modules) != 1 {
		t.Fatalf("workspace modules = %v: the replacement directory joined the workspace", root.Modules)
	}
}

// The root labels a build-list pair as every report and error spells
// it: a replaced pair with its replacement, pinned or directory, and
// an unreplaced pair by itself (REQ-work-replace, REQ-work-replace-dir).
func TestRootLabelsAPairOnce(t *testing.T) {
	mod := func(path string) *fstest.MapFile { return &fstest.MapFile{Data: []byte("module: " + path + "\n")} }
	root, err := LoadFor(fstest.MapFS{
		"pb.work":         {Data: []byte("use:\n  - m\nreplace:\n  example.com/x: example.com/y@v2.0.0\n  example.com/z: ./forks/z\n")},
		"m/pb.yaml":       mod("example.com/m"),
		"forks/z/pb.yaml": mod("example.com/z"),
	}, ".")
	if err != nil {
		t.Fatal(err)
	}
	v1, err := version.Parse("v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"example.com/x": "example.com/x@v1.0.0 => example.com/y@v2.0.0",
		"example.com/z": "example.com/z@v1.0.0 => ./forks/z",
		"example.com/w": "example.com/w@v1.0.0",
	} {
		if got := root.Label(path, v1); got != want {
			t.Errorf("Label(%s) = %q, want %q", path, got, want)
		}
	}
}
