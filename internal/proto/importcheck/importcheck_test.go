package importcheck

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/proto/modfiles"
	"pgregory.net/rapid"
)

// The well-known set is exactly the toolchain's embedded sources: the
// classic types and compiler/plugin.proto are in; go_features.proto is
// deliberately out (protobuf installations do not ship it — it resolves
// through modules); nothing outside google/protobuf is ever in.
func TestWellKnownGolden(t *testing.T) {
	for path, want := range map[string]bool{
		"google/protobuf/timestamp.proto":       true,
		"google/protobuf/descriptor.proto":      true,
		"google/protobuf/any.proto":             true,
		"google/protobuf/compiler/plugin.proto": true,
		"google/protobuf/go_features.proto":     false,
		"google/protobuf/nonexistent.proto":     false,
		"example.com/x.proto":                   false,
		"timestamp.proto":                       false,
		"":                                      false,
		// Directories open successfully on an embedded FS but are not
		// importable source files.
		"google":                   false,
		"google/protobuf":          false,
		"google/protobuf/compiler": false,
		".":                        false,
	} {
		if got := WellKnown(path); got != want {
			t.Errorf("WellKnown(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestImportsGolden(t *testing.T) {
	src := []byte(`// import "not/this.proto"
syntax = "proto3";
package x;
/* import "nor/this.proto" */
import "a/b.proto";
import public "c/d.proto";
import weak "e/f.proto";
message M { string s = 1; } // trailing comment with import "x"
`)
	got, err := Imports("m.proto", src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a/b.proto", "c/d.proto", "e/f.proto"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("imports = %v, want %v", got, want)
	}
}

func TestImportsMalformedSource(t *testing.T) {
	_, err := Imports("bad.proto", []byte("syntax = ;;; nonsense"))
	if err == nil {
		t.Fatal("malformed source accepted")
	}
	// The diagnostic is the reporter's positioned syntax error
	// ("bad.proto:line:col: ..."), not the parser's bare sentinel.
	if !strings.Contains(err.Error(), "bad.proto:1:") {
		t.Fatalf("parse error %q carries no source position", err)
	}
}

func TestCheckGolden(t *testing.T) {
	modules := []Module{
		{Path: "example.com/app", Files: map[string][]string{
			"app/main.proto": {
				"app/helper.proto",                // own module
				"lib/lib.proto",                   // other module
				"google/protobuf/timestamp.proto", // well-known
			},
			"app/helper.proto": nil,
		}},
		{Path: "example.com/lib", Files: map[string][]string{
			"lib/lib.proto": {"google/protobuf/any.proto"},
		}},
	}
	if err := Check(modules); err != nil {
		t.Fatalf("satisfied build list failed: %v", err)
	}
}

func TestCheckUnsatisfied(t *testing.T) {
	modules := []Module{
		{Path: "example.com/app", Files: map[string][]string{
			"app/main.proto": {
				"missing/gone.proto",
				"google/protobuf/go_features.proto", // not well-known, not provided
			},
		}},
	}
	err := Check(modules)
	var ue *UnsatisfiedError
	if !errors.As(err, &ue) || len(ue.Unsatisfied) != 2 {
		t.Fatalf("err = %v, want UnsatisfiedError with 2 findings", err)
	}
	// The message names module, file, and import per finding, findings
	// joined by "; " — pinned exactly, in sorted order.
	want := `module example.com/app: file app/main.proto imports "google/protobuf/go_features.proto", which no module in the build list satisfies; ` +
		`module example.com/app: file app/main.proto imports "missing/gone.proto", which no module in the build list satisfies`
	if err.Error() != want {
		t.Fatalf("error = %q\nwant   %q", err.Error(), want)
	}
}

// A module dependency carrying go_features.proto satisfies the import —
// the module route is the sanctioned one for it.
func TestCheckGoFeaturesViaModule(t *testing.T) {
	modules := []Module{
		{Path: "example.com/app", Files: map[string][]string{
			"app/main.proto": {"google/protobuf/go_features.proto"},
		}},
		{Path: "example.com/gofeatures", Files: map[string][]string{
			"google/protobuf/go_features.proto": nil,
		}},
	}
	if err := Check(modules); err != nil {
		t.Fatalf("module-provided go_features not honored: %v", err)
	}
}

// The report is sorted by module, file, import — independent of module
// order and map iteration — and duplicates collapse. Input order is the
// one free variable; rapid drives it plus repeated map iteration.
func TestCheckReportOrderDeterminism(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// File order deliberately conflicts with module order (module a's
		// file sorts last; module b's files sort first) and import order
		// conflicts with file order, so each sort clause is load-bearing.
		mods := []Module{
			{Path: "example.com/b", Files: map[string][]string{
				"aa/x.proto": {"gone/9.proto", "gone/2.proto", "gone/9.proto"},
				"ab/y.proto": {"gone/1.proto"},
			}},
			{Path: "example.com/a", Files: map[string][]string{
				"zz/z.proto": {"gone/3.proto"},
			}},
		}
		shuffled := append([]Module(nil), mods...)
		if rapid.Bool().Draw(t, "swap") {
			shuffled[0], shuffled[1] = shuffled[1], shuffled[0]
		}
		err := Check(shuffled)
		var ue *UnsatisfiedError
		if !errors.As(err, &ue) {
			t.Fatalf("err = %v", err)
		}
		want := []Unsatisfied{
			{Module: "example.com/a", File: "zz/z.proto", Import: "gone/3.proto"},
			{Module: "example.com/b", File: "aa/x.proto", Import: "gone/2.proto"},
			{Module: "example.com/b", File: "aa/x.proto", Import: "gone/9.proto"},
			{Module: "example.com/b", File: "ab/y.proto", Import: "gone/1.proto"},
		}
		if !slices.Equal(ue.Unsatisfied, want) {
			t.Fatalf("findings = %+v, want %+v", ue.Unsatisfied, want)
		}
	})
}

// Every import in the input is either reported or genuinely satisfied —
// the checker neither drops nor invents findings.
func TestCheckCompletenessProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		fileName := func(label string) string {
			return string(rapid.SliceOfN(rapid.SampledFrom([]rune("abc")), 1, 3).Draw(t, label)) + ".proto"
		}
		nMods := rapid.IntRange(1, 3).Draw(t, "nMods")
		var mods []Module
		for i := range nMods {
			nFiles := rapid.IntRange(0, 3).Draw(t, fmt.Sprint("nFiles", i))
			files := make(map[string][]string)
			for j := range nFiles {
				name := fmt.Sprint("m", i, "/", fileName(fmt.Sprint("f", i, j)))
				nImp := rapid.IntRange(0, 3).Draw(t, fmt.Sprint("nImp", i, j))
				var imps []string
				for k := range nImp {
					// Imports drawn from plausible targets: sibling module
					// files, well-knowns, and never-provided paths.
					imps = append(imps, rapid.SampledFrom([]string{
						"m0/a.proto", "m1/b.proto",
						"google/protobuf/empty.proto",
						"gone/" + fileName(fmt.Sprint("g", i, j, k)),
					}).Draw(t, fmt.Sprint("imp", i, j, k)))
				}
				files[name] = imps
			}
			mods = append(mods, Module{Path: fmt.Sprint("example.com/m", i), Files: files})
		}

		provided := map[string]bool{}
		for _, m := range mods {
			for f := range m.Files {
				provided[f] = true
			}
		}
		reported := map[Unsatisfied]bool{}
		if err := Check(mods); err != nil {
			var ue *UnsatisfiedError
			if !errors.As(err, &ue) {
				t.Fatalf("err = %v", err)
			}
			for _, u := range ue.Unsatisfied {
				reported[u] = true
			}
		}
		for _, m := range mods {
			for f, imps := range m.Files {
				for _, imp := range imps {
					satisfied := WellKnown(imp) || provided[imp]
					if satisfied == reported[Unsatisfied{Module: m.Path, File: f, Import: imp}] {
						t.Fatalf("import %q of %s %s: satisfied=%v reported=%v",
							imp, m.Path, f, satisfied, !satisfied)
					}
				}
			}
		}
	})
}

// Views projects a build's modules onto the checker's view: every
// proto file with its imports, a malformed file failing with the
// caller's label wrapping the parse error, in module order.
func TestViewsProjectsEveryModule(t *testing.T) {
	mods := []modfiles.Module{
		{Path: "example.com/a", Files: map[string][]byte{"a/x.proto": []byte("syntax = \"proto3\";\nimport \"b/y.proto\";\n")}},
		{Path: "example.com/b", Files: map[string][]byte{"b/y.proto": []byte("syntax = \"proto3\";\n")}},
	}
	views, err := Views(mods, func(m modfiles.Module, f string) string { return m.Path + "/" + f })
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || views[0].Path != "example.com/a" || views[1].Path != "example.com/b" {
		t.Fatalf("views = %+v", views)
	}
	if got := views[0].Files; len(got) != 1 || len(got["a/x.proto"]) != 1 || got["a/x.proto"][0] != "b/y.proto" {
		t.Fatalf("a's view = %+v", got)
	}
	if got := views[1].Files; len(got) != 1 || len(got["b/y.proto"]) != 0 {
		t.Fatalf("b's view = %+v", got)
	}
	if err := Check(views); err != nil {
		t.Fatalf("the projected views: %v", err)
	}

	// Two malformed files: the first in sorted order is the one named,
	// whatever order the map yields them.
	mods[1].Files["b/z.proto"] = []byte("syntax = \"proto3\";\nimport \"unterminated\n")
	mods[1].Files["b/a.proto"] = []byte("syntax = \"proto3\";\nimport \"unterminated\n")
	for range 8 {
		_, err = Views(mods, func(m modfiles.Module, f string) string { return "label:" + m.Path + "/" + f })
		if err == nil || !strings.HasPrefix(err.Error(), "label:example.com/b/b/a.proto: ") {
			t.Fatalf("a malformed file: %v", err)
		}
	}
}
