package compile

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/proto/importcheck"
	"github.com/greatliontech/pb/internal/proto/modfiles"
	"google.golang.org/protobuf/proto"
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

// Membership names each file's module by its path — a workspace
// module's or a dependency's — "" for a well-known import and for a
// path no module provides, holds every module of the build whether
// or not a file reaches it, and refuses an ambiguous build as
// Providers does (generation.md REQ-gen-overrides-declarative).
func TestMembership(t *testing.T) {
	mods := []modfiles.Module{
		mod("example.com/a", "", true, map[string]string{"a/a.proto": "syntax = \"proto3\";\nimport \"m1/m.proto\";\n", "google/protobuf/empty.proto": "syntax = \"proto3\";\n"}),
		mod("example.com/m1", "v1.0.0", false, map[string]string{"m1/m.proto": "syntax = \"proto3\";\n"}),
		mod("example.com/unused", "v2.0.0", false, map[string]string{"u/u.proto": "syntax = \"proto3\";\n"}),
	}
	of, paths, err := Membership(mods)
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"a/a.proto": "example.com/a", "m1/m.proto": "example.com/m1", "u/u.proto": "example.com/unused", "google/protobuf/empty.proto": "", "nowhere.proto": ""} {
		if got := of(p); got != want {
			t.Errorf("of(%s) = %q, want %q", p, got, want)
		}
	}
	if !maps.Equal(paths, map[string]bool{"example.com/a": true, "example.com/m1": true, "example.com/unused": true}) {
		t.Errorf("paths = %v", paths)
	}
	ambiguous := append(mods, mod("example.com/m2", "v1.0.0", false, map[string]string{"m1/m.proto": "syntax = \"proto3\";\n"}))
	var amb *AmbiguousError
	if _, _, err := Membership(ambiguous); !errors.As(err, &amb) {
		t.Fatalf("ambiguous: %v", err)
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

// An unsatisfied import is reported through importcheck's exhaustive
// report, before any compilation.
func TestCompileUnsatisfiedImport(t *testing.T) {
	mods := []modfiles.Module{
		mod("example.com/a", "", true, map[string]string{"a.proto": "syntax = \"proto3\";\nimport \"missing.proto\";\nimport \"also/missing.proto\";\n"}),
	}
	_, err := Compile(ctx, mods)
	var u *importcheck.UnsatisfiedError
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
	// The module's copy is no file of the build: not compiled as a
	// target, so neither generated for nor exported.
	if len(res.Files) != 1 || res.Files[0].Path() != "a.proto" {
		t.Fatalf("compiled %v, want a.proto alone", res.Files)
	}
	if got := res.Closure(); !slices.Equal(got, []string{"a.proto"}) {
		t.Fatalf("Closure = %v, want a.proto alone", got)
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

// CompileFiles compiles one module's files alone: every module
// provides imports, and no other module's imports are checked or
// compiled — a build where a's files no longer satisfy b's import
// still compiles a (check-rules.md REQ-break-base-materialized). The
// module's every file has its imports checked, the files given alone
// are compiled: a module whose files declare one symbol twice
// compiles a file of them, and a file outside the targets with an
// unsatisfied import fails them.
func TestCompileFiles(t *testing.T) {
	mods := []modfiles.Module{
		{Path: "example.com/a", Local: true, Dir: "a", Files: map[string][]byte{"a.proto": []byte("syntax = \"proto3\";\npackage a;\nmessage A {}\n")}},
		{Path: "example.com/b", Local: true, Dir: "b", Files: map[string][]byte{"b.proto": []byte("syntax = \"proto3\";\npackage b;\nimport \"a.proto\";\nimport \"gone.proto\";\nmessage B { a.A a = 1; }\n")}},
	}
	if _, err := Compile(context.Background(), mods); err == nil {
		t.Fatal("the build with an unsatisfied import compiled whole")
	}
	r, err := CompileFiles(context.Background(), mods, 0, mods[0].Protos())
	if err != nil || len(r.Files) != 1 || r.Files[0].Path() != "a.proto" {
		t.Fatalf("a alone: %v %v", err, r)
	}
	if _, err := CompileFiles(context.Background(), mods, 1, mods[1].Protos()); err == nil {
		t.Fatal("b alone compiled with its import unsatisfied")
	}

	// A subset: the whole set fails on the twice-declared symbol, a
	// file of it compiles; a file outside the targets still has its
	// imports checked.
	twice := modfiles.Module{Path: "example.com/t", Local: true, Dir: "t", Files: map[string][]byte{
		"x/m.proto":         []byte("syntax = \"proto3\";\npackage x;\nmessage M {}\n"),
		"preview/x/m.proto": []byte("syntax = \"proto3\";\npackage x;\nmessage M {}\n"),
		"y.proto":           []byte("syntax = \"proto3\";\nimport \"x/m.proto\";\nimport \"preview/x/m.proto\";\n"),
	}}
	if _, err := CompileFiles(context.Background(), []modfiles.Module{twice}, 0, twice.Protos()); err == nil {
		t.Fatal("a symbol declared twice compiled as a set")
	}
	r, err = CompileFiles(context.Background(), []modfiles.Module{twice}, 0, []string{"x/m.proto"})
	if err != nil || len(r.Files) != 1 || r.Files[0].Path() != "x/m.proto" {
		t.Fatalf("one file of the set: %v %v", err, r)
	}
	twice.Files["z.proto"] = []byte("syntax = \"proto3\";\nimport \"gone.proto\";\n")
	if _, err := CompileFiles(context.Background(), []modfiles.Module{twice}, 0, []string{"x/m.proto"}); err == nil {
		t.Fatal("a file outside the targets with an unsatisfied import passed")
	}
}

// The provider index names each path's one module by its position and
// skips a module's copy of a well-known path (REQ-gen-compile); two
// providers of one path are the ambiguity Compile refuses.
func TestProviders(t *testing.T) {
	mods := []modfiles.Module{
		mod("example.com/a", "", true, map[string]string{
			"a/a.proto":                   "syntax = \"proto3\";\n",
			"google/protobuf/empty.proto": "syntax = \"proto3\";\n",
		}),
		mod("example.com/m1", "v1.0.0", false, map[string]string{
			"m1/types.proto": "syntax = \"proto3\";\n",
		}),
	}
	index, err := Providers(mods)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"a/a.proto": 0, "m1/types.proto": 1}
	if !maps.Equal(index, want) {
		t.Fatalf("Providers = %v, want %v", index, want)
	}
	mods = append(mods, mod("example.com/m2", "v1.0.0", false, map[string]string{"m1/types.proto": "syntax = \"proto3\";\n"}))
	var amb *AmbiguousError
	if _, err := Providers(mods); !errors.As(err, &amb) || len(amb.Paths) != 1 || amb.Paths[0].Path != "m1/types.proto" {
		t.Fatalf("Providers over two providers = %v", err)
	}
}

// The closure of a compiled build is every workspace file and every
// file one reaches through imports, across modules, transitively — a
// file no workspace file reaches is absent, a well-known import is
// absent and its own imports are not walked — sorted, each once
// (export.md, the import closure term).
func TestClosure(t *testing.T) {
	mods := []modfiles.Module{
		mod("example.com/a", "", true, map[string]string{
			"a/a.proto": "syntax = \"proto3\";\npackage a;\nimport \"google/protobuf/timestamp.proto\";\nimport \"m1/types.proto\";\nmessage A { google.protobuf.Timestamp t = 1; m1.T m = 2; }\n",
			"a/z.proto": "syntax = \"proto3\";\npackage a;\nimport \"m1/types.proto\";\nimport \"google/protobuf/go_features.proto\";\n",
			// A workspace copy of a well-known path: the toolchain's
			// answers for it, and it is no file of the closure.
			"google/protobuf/empty.proto": "syntax = \"proto3\";\npackage google.protobuf;\n",
		}),
		mod("example.com/m1", "v1.0.0", false, map[string]string{
			"m1/types.proto":  "syntax = \"proto3\";\npackage m1;\nimport \"m2/deep.proto\";\nmessage T { m2.D d = 1; }\n",
			"m1/unused.proto": "syntax = \"proto3\";\npackage m1;\n",
		}),
		mod("example.com/m2", "v1.0.0", false, map[string]string{
			"m2/deep.proto": "syntax = \"proto3\";\npackage m2;\nimport \"google/protobuf/duration.proto\";\nmessage D { google.protobuf.Duration d = 1; }\n",
		}),
		mod("example.com/m3", "v1.0.0", false, map[string]string{
			"m3/never.proto": "syntax = \"proto3\";\npackage m3;\n",
		}),
		// go_features.proto is no well-known import: it resolves
		// through modules and is exported like any file
		// (module-resolution.md, the well-known imports term).
		mod("example.com/features", "v1.0.0", false, map[string]string{
			"google/protobuf/go_features.proto": "syntax = \"proto3\";\npackage pb;\n",
		}),
	}
	res, err := Compile(ctx, mods)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a/a.proto", "a/z.proto", "google/protobuf/go_features.proto", "m1/types.proto", "m2/deep.proto"}
	if got := res.Closure(); !slices.Equal(got, want) {
		t.Fatalf("Closure = %v, want %v", got, want)
	}
}

// For every build the closure holds each workspace file, is closed
// under imports less the well-known ones, holds no well-known path
// and nothing the provider index does not name, and is sorted without
// repetition.
func TestClosureProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 7).Draw(rt, "n")
		names := rapid.SliceOfNDistinct(rapid.StringMatching(`[a-z]{1,5}\.proto`), n, n, rapid.ID[string]).Draw(rt, "names")
		// Imports point only at later names, so the graph is acyclic;
		// a well-known import may join any file.
		imports := map[string][]string{}
		for i, nm := range names {
			var imps []string
			for _, later := range names[i+1:] {
				if rapid.Bool().Draw(rt, "edge "+nm+"->"+later) {
					imps = append(imps, later)
				}
			}
			if rapid.Bool().Draw(rt, "wkt "+nm) {
				imps = append(imps, "google/protobuf/empty.proto")
			}
			imports[nm] = imps
		}
		// The first name is the workspace's; the rest split over two
		// external modules by a coin.
		local, m1, m2 := map[string]string{}, map[string]string{}, map[string]string{}
		for i, nm := range names {
			var b strings.Builder
			b.WriteString("syntax = \"proto3\";\n")
			for _, imp := range imports[nm] {
				b.WriteString("import \"" + imp + "\";\n")
			}
			switch {
			case i == 0:
				local[nm] = b.String()
			case rapid.Bool().Draw(rt, "m1 "+nm):
				m1[nm] = b.String()
			default:
				m2[nm] = b.String()
			}
		}
		// A workspace copy of a well-known path, sometimes: no file of
		// the build, so no member of the closure.
		if rapid.Bool().Draw(rt, "local wkt") {
			local["google/protobuf/empty.proto"] = "syntax = \"proto3\";\n"
		}
		mods := []modfiles.Module{mod("example.com/a", "", true, local), mod("example.com/m1", "v1.0.0", false, m1), mod("example.com/m2", "v1.0.0", false, m2)}
		res, err := Compile(ctx, mods)
		if err != nil {
			rt.Fatal(err)
		}
		index, err := Providers(mods)
		if err != nil {
			rt.Fatal(err)
		}
		got := res.Closure()
		if !slices.IsSorted(got) || len(got) != len(slices.Compact(slices.Clone(got))) {
			rt.Fatalf("closure not sorted or repeating: %v", got)
		}
		in := map[string]bool{}
		for _, p := range got {
			in[p] = true
		}
		for _, p := range got {
			if modfiles.WellKnown(p) {
				rt.Fatalf("well-known %s in the closure", p)
			}
			if _, ok := index[p]; !ok {
				rt.Fatalf("%s in the closure names no provider", p)
			}
			for _, imp := range imports[p] {
				if !modfiles.WellKnown(imp) && !in[imp] {
					rt.Fatalf("%s imports %s, absent from the closure %v", p, imp, got)
				}
			}
		}
		for p := range local {
			if !in[p] && !modfiles.WellKnown(p) {
				rt.Fatalf("workspace file %s absent from the closure %v", p, got)
			}
		}
		// Minimal: every member is a workspace file or imported by a
		// member — nothing unreached joins.
		importedBy := map[string]bool{}
		for _, p := range got {
			for _, imp := range imports[p] {
				importedBy[imp] = true
			}
		}
		for _, p := range got {
			if _, isLocal := local[p]; !isLocal && !importedBy[p] {
				rt.Fatalf("%s in the closure, reached by no workspace file: %v", p, got)
			}
		}
	})
}

// The topological order holds every reachable file once, the
// well-known imports among them, dependencies before importers,
// imports visited in declaration order, the roots in the order given
// (REQ-gen-request, build.md REQ-build-set).
func TestTopological(t *testing.T) {
	mods := []modfiles.Module{
		mod("example.com/a", "", true, map[string]string{
			"a/a.proto": "syntax = \"proto3\";\npackage a;\nimport \"m1/types.proto\";\nimport \"google/protobuf/timestamp.proto\";\nmessage A { m1.T m = 1; google.protobuf.Timestamp t = 2; }\n",
			"a/z.proto": "syntax = \"proto3\";\npackage a;\nimport \"google/protobuf/empty.proto\";\nimport \"m1/types.proto\";\n",
			// api.proto imports well-known files of its own: the walk
			// goes through them, the closure never holds them.
			"a/y.proto": "syntax = \"proto3\";\npackage a;\nimport \"google/protobuf/api.proto\";\nmessage Y { google.protobuf.Api api = 1; }\n",
		}),
		mod("example.com/m1", "v1.0.0", false, map[string]string{
			"m1/types.proto": "syntax = \"proto3\";\npackage m1;\nimport \"m2/deep.proto\";\nmessage T { m2.D d = 1; }\n",
		}),
		mod("example.com/m2", "v1.0.0", false, map[string]string{
			"m2/deep.proto": "syntax = \"proto3\";\npackage m2;\nmessage D {}\n",
		}),
	}
	res, err := Compile(ctx, mods)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, fd := range res.Topological() {
		got = append(got, fd.Path())
	}
	want := []string{"m2/deep.proto", "m1/types.proto", "google/protobuf/timestamp.proto", "a/a.proto", "google/protobuf/source_context.proto", "google/protobuf/any.proto", "google/protobuf/type.proto", "google/protobuf/api.proto", "a/y.proto", "google/protobuf/empty.proto", "a/z.proto"}
	if !slices.Equal(got, want) {
		t.Fatalf("Topological = %v, want %v", got, want)
	}
	if got := res.Closure(); !slices.Equal(got, []string{"a/a.proto", "a/y.proto", "a/z.proto", "m1/types.proto", "m2/deep.proto"}) {
		t.Fatalf("Closure = %v", got)
	}
}

// The descriptor set is the topological order's descriptors, each
// carrying its options and source information, converted fresh per
// call so a caller's rewrite touches its own copy alone
// (build.md REQ-build-set).
func TestDescriptorSet(t *testing.T) {
	mods := []modfiles.Module{
		mod("example.com/a", "", true, map[string]string{
			"a/a.proto": "syntax = \"proto3\";\npackage a;\noption go_package = \"example.com/a;a\";\nimport \"google/protobuf/empty.proto\";\n// A is documented.\nmessage A { google.protobuf.Empty e = 1; }\n",
		}),
	}
	res, err := Compile(ctx, mods)
	if err != nil {
		t.Fatal(err)
	}
	// A source-retention option is kept, where protoc would strip it.
	retained := []modfiles.Module{
		mod("example.com/r", "", true, map[string]string{
			"r/r.proto": "syntax = \"proto3\";\npackage r;\nimport \"o/o.proto\";\noption (o.src_opt) = \"kept\";\n",
		}),
		mod("example.com/o", "v1.0.0", false, map[string]string{
			"o/o.proto": "syntax = \"proto2\";\npackage o;\nimport \"google/protobuf/descriptor.proto\";\nextend google.protobuf.FileOptions { optional string src_opt = 50010 [retention = RETENTION_SOURCE]; }\n",
		}),
	}
	rres, err := Compile(ctx, retained)
	if err != nil {
		t.Fatal(err)
	}
	rset := rres.DescriptorSet()
	r := rset.File[len(rset.File)-1]
	if rb, _ := proto.Marshal(r.GetOptions()); r.GetName() != "r/r.proto" || !strings.Contains(string(rb), "kept") {
		t.Fatalf("the source-retention option was stripped: %v", r.GetOptions())
	}
	set := res.DescriptorSet()
	if len(set.File) != 2 || set.File[0].GetName() != "google/protobuf/empty.proto" || set.File[1].GetName() != "a/a.proto" {
		t.Fatalf("set files: %v", set.File)
	}
	// A well-known file's descriptor is as complete as any: its
	// options and its source information carried.
	if wk := set.File[0]; wk.GetOptions().GetGoPackage() == "" || wk.GetSourceCodeInfo() == nil {
		t.Fatalf("the well-known file's descriptor is stripped: %v", wk)
	}
	a := set.File[1]
	if a.GetOptions().GetGoPackage() != "example.com/a;a" {
		t.Fatalf("options: %v", a.GetOptions())
	}
	if a.GetSourceCodeInfo() == nil || len(a.GetSourceCodeInfo().GetLocation()) == 0 {
		t.Fatal("source information missing")
	}
	var documented bool
	for _, loc := range a.GetSourceCodeInfo().GetLocation() {
		if strings.Contains(loc.GetLeadingComments(), "A is documented") {
			documented = true
		}
	}
	if !documented {
		t.Fatal("the comment is not in the source information")
	}
	// Fresh per call.
	a.Options.GoPackage = nil
	if res.DescriptorSet().File[1].GetOptions().GetGoPackage() != "example.com/a;a" {
		t.Fatal("a rewrite reached the next call's set")
	}
}

// For every build the descriptor set's files are the topological
// order's, each reachable file once with the well-known imports among
// them, the closure is that order less the well-known imports sorted,
// and two sets serialize byte-identically (build.md REQ-build-set,
// REQ-build-determinism).
func TestDescriptorSetProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 6).Draw(rt, "n")
		names := rapid.SliceOfNDistinct(rapid.StringMatching(`[a-z]{1,5}\.proto`), n, n, rapid.ID[string]).Draw(rt, "names")
		files := map[string]string{}
		for i, nm := range names {
			var b strings.Builder
			b.WriteString("syntax = \"proto3\";\n")
			for _, later := range names[i+1:] {
				if rapid.Bool().Draw(rt, "edge "+nm+"->"+later) {
					b.WriteString("import \"" + later + "\";\n")
				}
			}
			if rapid.Bool().Draw(rt, "wkt "+nm) {
				b.WriteString("import \"google/protobuf/empty.proto\";\n")
			}
			files[nm] = b.String()
		}
		// The first name is the workspace's and any other may be, so
		// the roots are several; the rest split over two external
		// modules by a coin, so a file may be reached only through an
		// external module.
		local, m1, m2 := map[string]string{}, map[string]string{}, map[string]string{}
		for i, nm := range names {
			switch {
			case i == 0 || rapid.Bool().Draw(rt, "local "+nm):
				local[nm] = files[nm]
			case rapid.Bool().Draw(rt, "m1 "+nm):
				m1[nm] = files[nm]
			default:
				m2[nm] = files[nm]
			}
		}
		mods := []modfiles.Module{mod("example.com/a", "", true, local), mod("example.com/m1", "v1.0.0", false, m1), mod("example.com/m2", "v1.0.0", false, m2)}
		res, err := Compile(ctx, mods)
		if err != nil {
			rt.Fatal(err)
		}
		order := res.Topological()
		set := res.DescriptorSet()
		if len(set.File) != len(order) {
			rt.Fatalf("set holds %d files, the order %d", len(set.File), len(order))
		}
		inOrder := map[string]bool{}
		for _, fd := range order {
			inOrder[fd.Path()] = true
		}
		for _, f := range res.Files {
			if !inOrder[f.Path()] {
				rt.Fatalf("root %s absent from the order", f.Path())
			}
		}
		seen := map[string]bool{}
		var closure []string
		for i, fd := range order {
			if set.File[i].GetName() != fd.Path() {
				rt.Fatalf("set[%d] = %s, order %s", i, set.File[i].GetName(), fd.Path())
			}
			if seen[fd.Path()] {
				rt.Fatalf("%s twice in the order", fd.Path())
			}
			seen[fd.Path()] = true
			imports := fd.Imports()
			for j := 0; j < imports.Len(); j++ {
				if !seen[imports.Get(j).Path()] {
					rt.Fatalf("%s before its import %s", fd.Path(), imports.Get(j).Path())
				}
			}
			if !modfiles.WellKnown(fd.Path()) {
				closure = append(closure, fd.Path())
			}
		}
		slices.Sort(closure)
		if got := res.Closure(); !slices.Equal(got, closure) {
			rt.Fatalf("Closure = %v, the order less the well-known %v", got, closure)
		}
		a, _ := proto.MarshalOptions{Deterministic: true}.Marshal(set)
		b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(res.DescriptorSet())
		if !slices.Equal(a, b) {
			rt.Fatal("two sets of one result differ")
		}
	})
}
