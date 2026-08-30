package protocomp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/modfiles"
	"github.com/greatliontech/pb/internal/protoimport"
	"pgregory.net/rapid"
)

var ctx = context.Background()

func mod(path, ver string, local bool, files map[string]string) modfiles.Module {
	m := modfiles.Module{Path: path, Version: ver, Local: local, Files: map[string][]byte{}}
	for p, b := range files {
		m.Files[p] = []byte(b)
	}
	return m
}

// A workspace file importing a well-known file, an external module's
// file, and a synthesized module's file compiles; imports resolve at
// each module's include root, and output follows workspace order
// (REQ-gen-compile, REQ-resolve-synthesis include-root clause).
func TestCompileResolvesAcrossModules(t *testing.T) {
	mods := []modfiles.Module{
		mod("example.com/b", "", true, map[string]string{
			"b.proto": "syntax = \"proto3\";\npackage b;\nmessage B {}\n",
		}),
		mod("example.com/a", "", true, map[string]string{
			"a/a.proto": "syntax = \"proto3\";\npackage a;\nimport \"google/protobuf/timestamp.proto\";\nimport \"m1/types.proto\";\nimport \"synth/s.proto\";\nimport \"b.proto\";\nmessage A { google.protobuf.Timestamp t = 1; m1.T m = 2; synth.S s = 3; b.B b = 4; }\n",
			"a/z.proto": "syntax = \"proto3\";\npackage a;\n",
		}),
		mod("example.com/m1", "v1.0.0", false, map[string]string{
			"m1/types.proto": "syntax = \"proto3\";\npackage m1;\nmessage T {}\n",
		}),
		// Synthesized: no module file; the archive root is the include root.
		mod("example.com/repo/synth", "v0.0.0-20240101000000-abcdefabcdef", false, map[string]string{
			"synth/s.proto": "syntax = \"proto3\";\npackage synth;\nmessage S {}\n",
		}),
	}
	res, err := Compile(ctx, mods)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	got := make([]string, len(res.Files))
	for i, f := range res.Files {
		got[i] = f.Path()
	}
	want := []string{"b.proto", "a/a.proto", "a/z.proto"}
	if !slices.Equal(got, want) {
		t.Fatalf("compiled = %v, want %v", got, want)
	}
	a := res.Files.FindFileByPath("a/a.proto")
	if a == nil || a.FindImportByPath("synth/s.proto") == nil || a.FindImportByPath("google/protobuf/timestamp.proto") == nil {
		t.Fatal("imports not linked")
	}
	if a.SourceLocations().Len() == 0 {
		t.Fatal("source info missing")
	}
}

// Two modules providing one path fail naming the path and both
// providers (REQ-gen-compile).
func TestCompileAmbiguousProvider(t *testing.T) {
	mods := []modfiles.Module{
		mod("example.com/a", "", true, map[string]string{"x.proto": "syntax = \"proto3\";\n", "shared/s.proto": "syntax = \"proto3\";\n"}),
		mod("example.com/m1", "v1.0.0", false, map[string]string{"shared/s.proto": "syntax = \"proto3\";\n"}),
		mod("example.com/m2", "v2.0.0", false, map[string]string{"shared/s.proto": "syntax = \"proto3\";\n", "other.proto": "syntax = \"proto3\";\n"}),
	}
	_, err := Compile(ctx, mods)
	var amb *AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("err = %v, want AmbiguousError", err)
	}
	if len(amb.Paths) != 1 || amb.Paths[0].Path != "shared/s.proto" ||
		!slices.Equal(amb.Paths[0].Providers, []string{"example.com/a", "example.com/m1@v1.0.0", "example.com/m2@v2.0.0"}) {
		t.Fatalf("ambiguity = %+v", amb.Paths)
	}
	if !strings.Contains(err.Error(), "shared/s.proto is provided by example.com/a and example.com/m1@v1.0.0 and example.com/m2@v2.0.0") {
		t.Fatalf("message = %q", err)
	}
}

// Two ambiguous paths across exactly two providers: the report is
// path-sorted and "; "-joined, and a two-provider duplicate is already
// an error.
func TestCompileAmbiguousTwoPaths(t *testing.T) {
	mods := []modfiles.Module{
		mod("example.com/a", "", true, map[string]string{"b/b.proto": "syntax = \"proto3\";\n", "a/a.proto": "syntax = \"proto3\";\n"}),
		mod("example.com/m1", "v1.0.0", false, map[string]string{"b/b.proto": "syntax = \"proto3\";\n", "a/a.proto": "syntax = \"proto3\";\n"}),
	}
	_, err := Compile(ctx, mods)
	var amb *AmbiguousError
	if !errors.As(err, &amb) || len(amb.Paths) != 2 {
		t.Fatalf("err = %v", err)
	}
	if amb.Paths[0].Path != "a/a.proto" || amb.Paths[1].Path != "b/b.proto" {
		t.Fatalf("not path-sorted: %+v", amb.Paths)
	}
	want := "ambiguous protobuf import paths: a/a.proto is provided by example.com/a and example.com/m1@v1.0.0; b/b.proto is provided by example.com/a and example.com/m1@v1.0.0"
	if err.Error() != want {
		t.Fatalf("message = %q", err.Error())
	}
}

// A module file sorting after a well-known-named file still enters the
// provider index: the well-known skip skips one entry, never the rest.
func TestWellKnownSkipIsPerFile(t *testing.T) {
	mods := []modfiles.Module{
		mod("example.com/a", "", true, map[string]string{
			"a.proto":                     "syntax = \"proto3\";\nimport \"z.proto\";\nmessage A { Z z = 1; }\n",
			"google/protobuf/empty.proto": "syntax = \"proto3\";\n",
			"z.proto":                     "syntax = \"proto3\";\nmessage Z {}\n",
		}),
	}
	res, err := Compile(ctx, mods)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files.FindFileByPath("a.proto").FindImportByPath("z.proto") == nil {
		t.Fatal("z.proto missing from the provider index")
	}
}

// An unsatisfied import is reported through protoimport's exhaustive
// report, before any compilation.
func TestCompileUnsatisfiedImport(t *testing.T) {
	mods := []modfiles.Module{
		mod("example.com/a", "", true, map[string]string{"a.proto": "syntax = \"proto3\";\nimport \"missing.proto\";\nimport \"also/missing.proto\";\n"}),
	}
	_, err := Compile(ctx, mods)
	var u *protoimport.UnsatisfiedError
	if !errors.As(err, &u) || len(u.Unsatisfied) != 2 {
		t.Fatalf("err = %v", err)
	}
}

// A parse error in a module's file names the module (and version for
// externals).
func TestCompileParseErrorLabels(t *testing.T) {
	_, err := Compile(ctx, []modfiles.Module{mod("example.com/m1", "v1.0.0", false, map[string]string{"bad.proto": "syntax = \"proto3\";\nmessage {"})})
	if err == nil || !strings.Contains(err.Error(), "example.com/m1@v1.0.0") {
		t.Fatalf("err = %v", err)
	}
	_, err = Compile(ctx, []modfiles.Module{mod("example.com/a", "", true, map[string]string{"bad.proto": "syntax = \"proto3\";\nmessage {"})})
	if err == nil || !strings.Contains(err.Error(), "example.com/a:") {
		t.Fatalf("err = %v", err)
	}
}

// A well-known path provided by a module never enters the provider
// index: the toolchain's copy wins, and no ambiguity is raised.
func TestWellKnownNeverShadowed(t *testing.T) {
	mods := []modfiles.Module{
		mod("example.com/a", "", true, map[string]string{
			"a.proto":                     "syntax = \"proto3\";\nimport \"google/protobuf/empty.proto\";\nmessage A { google.protobuf.Empty e = 1; }\n",
			"google/protobuf/empty.proto": "syntax = \"proto3\";\npackage google.protobuf;\nmessage Empty { int32 not_the_real_one = 1; }\n",
		}),
	}
	res, err := Compile(ctx, mods)
	if err != nil {
		t.Fatal(err)
	}
	empty := res.Files.FindFileByPath("a.proto").FindImportByPath("google/protobuf/empty.proto")
	if empty == nil || empty.Messages().ByName("Empty").Fields().Len() != 0 {
		t.Fatal("module copy shadowed the toolchain's well-known file")
	}
}

// Property: output order is module order then sorted path, for any
// file names — never map-iteration order.
func TestCompileOrderProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 6).Draw(rt, "n")
		names := rapid.SliceOfNDistinct(rapid.StringMatching(`[a-z]{1,6}(/[a-z]{1,4})?\.proto`), n, n, rapid.ID[string]).Draw(rt, "names")
		files := map[string]string{}
		for _, nm := range names {
			files[nm] = "syntax = \"proto3\";\n"
		}
		res, err := Compile(ctx, []modfiles.Module{mod("example.com/a", "", true, files)})
		if err != nil {
			rt.Fatal(err)
		}
		got := make([]string, len(res.Files))
		for i, f := range res.Files {
			got[i] = f.Path()
		}
		if !slices.IsSorted(got) || len(got) != n {
			rt.Fatalf("order %v", got)
		}
	})
}
