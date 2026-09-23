package dep

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-billy/v6/util"
	"pgregory.net/rapid"

	"github.com/greatliontech/pb/internal/module/archive"
	"github.com/greatliontech/pb/internal/proto/compile"
	"github.com/greatliontech/pb/internal/proto/importcheck"
	"github.com/greatliontech/pb/internal/testing/fetchtest"
)

// exportFixture is a workspace module a over three served modules: a
// imports m1, m1 imports m2, m3 is required and never imported; a and
// m1 each ship a copy of a well-known path. The tree is a real one:
// the export lands by a directory rename, which the in-memory
// filesystem does not model faithfully.
func exportFixture(t *testing.T) *depFixture {
	t.Helper()
	return exportFixtureOn(t, osfs.New(t.TempDir()))
}

func exportFixtureOn(t *testing.T, fsys billy.Filesystem) *depFixture {
	t.Helper()
	fx := newDepOn(t, fsys, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n  example.com/m3: v1.0.0\n"),
		"a/a.proto": "syntax = \"proto3\";\npackage a;\nimport \"google/protobuf/timestamp.proto\";\nimport \"m1/types.proto\";\nmessage A { google.protobuf.Timestamp t = 1; m1.T m = 2; }\n",
		"a/z.proto": "syntax = \"proto3\";\npackage a;\nimport \"m1/types.proto\";\n",
		// A workspace copy of a well-known path: no file of the build.
		"a/google/protobuf/empty.proto": "syntax = \"proto3\";\npackage google.protobuf;\n",
	})
	fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{
		"pb.yaml":                        ws("example.com/m1", "  example.com/m2: v1.0.0\n"),
		"m1/types.proto":                 "syntax = \"proto3\";\npackage m1;\nimport \"m2/deep.proto\";\nmessage T { m2.D d = 1; }\n",
		"m1/unused.proto":                "syntax = \"proto3\";\npackage m1;\n",
		"google/protobuf/duration.proto": "syntax = \"proto3\";\npackage google.protobuf;\n",
	})
	fx.serve(t, "example.com/m2", "v1.0.0", map[string]string{
		"pb.yaml":       ws("example.com/m2", ""),
		"m2/deep.proto": "syntax = \"proto3\";\npackage m2;\nmessage D {}\n",
	})
	fx.serve(t, "example.com/m3", "v1.0.0", map[string]string{
		"pb.yaml":        ws("example.com/m3", ""),
		"m3/never.proto": "syntax = \"proto3\";\npackage m3;\n",
	})
	return fx
}

// tree lists a directory's files as path → content, paths relative to
// dir, sorted.
func tree(t *testing.T, fsys billy.Filesystem, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	var walk func(d, rel string)
	walk = func(d, rel string) {
		entries, err := fsys.ReadDir(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			p := d + "/" + e.Name()
			r := e.Name()
			if rel != "" {
				r = rel + "/" + e.Name()
			}
			if e.IsDir() {
				walk(p, r)
				continue
			}
			info, err := e.Info()
			if err != nil {
				t.Fatal(err)
			}
			if mode := info.Mode(); mode&0o111 != 0 || !mode.IsRegular() {
				t.Fatalf("%s written with mode %v", p, mode)
			}
			b, err := util.ReadFile(fsys, p)
			if err != nil {
				t.Fatal(err)
			}
			got[r] = string(b)
		}
	}
	walk(dir, "")
	return got
}

func keys(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// The default export is the import closure: every workspace file and
// every file reached through imports across modules, at its import
// path, the well-known imports and a module's copies of them never
// written, a required module nothing reaches contributing nothing; the
// build is pinned; the report names every module in build order with
// its count and the whole (REQ-export-build, REQ-export-selection,
// REQ-export-layout, REQ-export-report).
func TestExportClosure(t *testing.T) {
	fx := exportFixture(t)
	var out strings.Builder
	if err := Export(ctx, fx.session(t, "."), "out", "./out", ExportOptions{}, &out); err != nil {
		t.Fatal(err)
	}
	got := tree(t, fx.ws, "out")
	want := []string{"a.proto", "m1/types.proto", "m2/deep.proto", "z.proto"}
	if !equalStrings(keys(got), want) {
		t.Fatalf("tree = %v, want %v", keys(got), want)
	}
	if got["m2/deep.proto"] != "syntax = \"proto3\";\npackage m2;\nmessage D {}\n" || got["a.proto"] != fx.read(t, "a/a.proto") {
		t.Fatalf("contents: %q", got)
	}
	wantOut := "example.com/a: 2 file(s)\nexample.com/m1@v1.0.0: 1 file(s)\nexample.com/m2@v1.0.0: 1 file(s)\nexample.com/m3@v1.0.0: 0 file(s)\nexported 4 file(s) to ./out\n"
	if out.String() != wantOut {
		t.Fatalf("report = %q, want %q", out.String(), wantOut)
	}
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "example.com/m2") {
		t.Fatalf("the build was not pinned: %q", lock)
	}
	// Nothing but the tree lands beside it.
	if got := names(t, fx.ws, "."); strings.Join(got, ",") != "a,out,pb.lock,pb.work" {
		t.Fatalf("the root holds %v", got)
	}
}

// Under --all every protobuf file of every module is written, a file
// nothing imports as its archive holds it, a module's copy of a
// well-known path still never (REQ-export-selection).
func TestExportAll(t *testing.T) {
	fx := exportFixture(t)
	var out strings.Builder
	if err := Export(ctx, fx.session(t, "."), "out", "out", ExportOptions{All: true}, &out); err != nil {
		t.Fatal(err)
	}
	got := tree(t, fx.ws, "out")
	want := []string{"a.proto", "m1/types.proto", "m1/unused.proto", "m2/deep.proto", "m3/never.proto", "z.proto"}
	if !equalStrings(keys(got), want) {
		t.Fatalf("tree = %v, want %v", keys(got), want)
	}
	wantOut := "example.com/a: 2 file(s)\nexample.com/m1@v1.0.0: 2 file(s)\nexample.com/m2@v1.0.0: 1 file(s)\nexample.com/m3@v1.0.0: 1 file(s)\nexported 6 file(s) to out\n"
	if out.String() != wantOut {
		t.Fatalf("report = %q, want %q", out.String(), wantOut)
	}
}

// The output directory is judged before the build is resolved: inside
// a workspace module (the root itself, under the single-module
// default), a non-empty directory, a file or a symbolic link is
// refused with nothing fetched and nothing written; an empty directory
// is replaced (REQ-export-output).
func TestExportTargetRefusals(t *testing.T) {
	fx := exportFixture(t)
	var out strings.Builder
	for _, dir := range []string{"a/vendor", "a"} {
		if err := Export(ctx, fx.session(t, "."), dir, dir, ExportOptions{}, &out); err == nil || !strings.Contains(err.Error(), "inside the workspace module example.com/a") {
			t.Fatalf("%s: %v", dir, err)
		}
	}
	fx.write(t, "full/keep", "k")
	if err := Export(ctx, fx.session(t, "."), "full", "full", ExportOptions{}, &out); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("non-empty: %v", err)
	}
	if _, err := fx.ws.Stat("pb.lock"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused export resolved the build: %v", err)
	}
	fx.write(t, "file", "f")
	if err := Export(ctx, fx.session(t, "."), "file", "file", ExportOptions{}, &out); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("a file: %v", err)
	}
	if err := fx.ws.Symlink("full", "link"); err != nil {
		t.Fatal(err)
	}
	if err := Export(ctx, fx.session(t, "."), "link", "link", ExportOptions{}, &out); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("a link: %v", err)
	}
	// A link to the module directory is resolved and refused as the
	// module's.
	if err := fx.ws.Symlink("a", "alias"); err != nil {
		t.Fatal(err)
	}
	if err := Export(ctx, fx.session(t, "."), "alias/vendor", "alias/vendor", ExportOptions{}, &out); err == nil || !strings.Contains(err.Error(), "inside the workspace module example.com/a") {
		t.Fatalf("an alias of the module: %v", err)
	}
	// A spelling the path rule does not see, naming the module
	// directory all the same, is judged by the directory's identity —
	// the rule a case-insensitive filesystem needs, witnessed here by
	// an unclean spelling.
	if err := Export(ctx, fx.session(t, "."), "./a/vendor", "./a/vendor", ExportOptions{}, &out); err == nil || !strings.Contains(err.Error(), "inside the workspace module example.com/a") || !strings.Contains(err.Error(), "(a is that directory)") {
		t.Fatalf("another spelling of the module: %v", err)
	}
	// A link to a subdirectory of a module, resolved: the tree would be
	// the module's. A link to a directory outside every module is fine.
	if err := fx.ws.MkdirAll("a/sub", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fx.ws.Symlink("a/sub", "deep"); err != nil {
		t.Fatal(err)
	}
	if err := Export(ctx, fx.session(t, "."), "deep/vendor", "deep/vendor", ExportOptions{}, &out); err == nil || !strings.Contains(err.Error(), "inside the workspace module example.com/a") {
		t.Fatalf("a link into the module: %v", err)
	}
	if err := fx.ws.MkdirAll("elsewhere", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fx.ws.Symlink("elsewhere", "away"); err != nil {
		t.Fatal(err)
	}
	if err := Export(ctx, fx.session(t, "."), "away/out", "away/out", ExportOptions{}, &out); err != nil {
		t.Fatalf("a link out of the modules: %v", err)
	}
	if got := tree(t, fx.ws, "elsewhere/out"); len(got) != 4 {
		t.Fatalf("through the link the tree holds %v", keys(got))
	}
	// An absolute link is read against the tree's root, and the tree
	// lands where the link leads.
	if err := fx.ws.MkdirAll("elsewhere2", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fx.ws.Symlink("/elsewhere2", "absaway"); err != nil {
		t.Fatal(err)
	}
	if err := Export(ctx, fx.session(t, "."), "absaway/out", "absaway/out", ExportOptions{}, &out); err != nil {
		t.Fatalf("an absolute link out of the modules: %v", err)
	}
	if got := tree(t, fx.ws, "elsewhere2/out"); len(got) != 4 {
		t.Fatalf("through the absolute link the tree holds %v", keys(got))
	}
	// A link leading out of the tree is refused as such.
	if err := fx.ws.Symlink("../../..", "up"); err != nil {
		t.Fatal(err)
	}
	if err := Export(ctx, fx.session(t, "."), "up/out", "up/out", ExportOptions{}, &out); err == nil || !strings.Contains(err.Error(), "leading out of the tree") {
		t.Fatalf("a link out of the tree: %v", err)
	}
	// A dangling link on the way, and an absent parent, are refused.
	if err := fx.ws.Symlink("nowhere", "dangling"); err != nil {
		t.Fatal(err)
	}
	if err := Export(ctx, fx.session(t, "."), "dangling/out", "dangling/out", ExportOptions{}, &out); err == nil || !strings.Contains(err.Error(), "dangling symbolic link") {
		t.Fatalf("a dangling link: %v", err)
	}
	if err := Export(ctx, fx.session(t, "."), "absent/out", "absent/out", ExportOptions{}, &out); err == nil || !strings.Contains(err.Error(), "parent directory of absent/out does not exist") {
		t.Fatalf("an absent parent: %v", err)
	}
	if got := names(t, fx.ws, "."); strings.Join(got, ",") != "a,absaway,alias,away,dangling,deep,elsewhere,elsewhere2,file,full,link,pb.lock,pb.work,up" {
		t.Fatalf("after refusals the root holds %v", got)
	}
	// The single-module default: the module directory is the root.
	single := newDep(t, map[string]string{
		"pb.yaml": ws("example.com/s", ""),
		"s.proto": "syntax = \"proto3\";\npackage s;\n",
	})
	if err := Export(ctx, single.session(t, "."), "vendor", "vendor", ExportOptions{}, &out); err == nil || !strings.Contains(err.Error(), "inside the workspace module example.com/s") {
		t.Fatalf("single-module: %v", err)
	}
	// An empty directory is replaced.
	if err := fx.ws.MkdirAll("empty", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Export(ctx, fx.session(t, "."), "empty", "empty", ExportOptions{}, &out); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, fx.ws, "empty"); len(got) != 4 {
		t.Fatalf("tree = %v", keys(got))
	}
}

// Two modules' exported paths equal under case folding, or one a file
// where the other's path implies a directory, are refused before
// anything is written, naming both files and their modules
// (REQ-export-layout).
func TestExportCollision(t *testing.T) {
	fx := newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n  example.com/m4: v1.0.0\n"),
		"a/a.proto": "syntax = \"proto3\";\npackage a;\nimport \"m1/types.proto\";\nimport \"M1/types.proto\";\nmessage A { m1.T x = 1; m1u.T y = 2; }\n",
	})
	fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", ""), "m1/types.proto": "syntax = \"proto3\";\npackage m1;\nmessage T {}\n"})
	fx.serve(t, "example.com/m4", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m4", ""), "M1/types.proto": "syntax = \"proto3\";\npackage m1u;\nmessage T {}\n"})
	var out strings.Builder
	err := Export(ctx, fx.session(t, "."), "out", "out", ExportOptions{}, &out)
	if !errors.Is(err, archive.ErrPathCollision) || !strings.Contains(err.Error(), "m1/types.proto of example.com/m1@v1.0.0 and M1/types.proto of example.com/m4@v1.0.0") {
		t.Fatalf("collision: %v", err)
	}
	if _, err := fx.ws.Stat("out"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("written despite the collision: %v", err)
	}
	if out.String() != "" {
		t.Fatalf("reported: %q", out.String())
	}
	// A file where another module's path implies a directory: the
	// implying file and its module are named.
	fx = newDep(t, map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n  example.com/m5: v1.0.0\n"),
		"a/a.proto": "syntax = \"proto3\";\npackage a;\n",
	})
	fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", ""), "m1/types.proto": "syntax = \"proto3\";\npackage m1;\n"})
	fx.serve(t, "example.com/m5", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m5", ""), "M1/types.proto/y.proto": "syntax = \"proto3\";\npackage m5;\n"})
	err = Export(ctx, fx.session(t, "."), "out", "out", ExportOptions{All: true}, &out)
	if !errors.Is(err, archive.ErrPathCollision) || !strings.Contains(err.Error(), "m1/types.proto of example.com/m1@v1.0.0 and M1/types.proto/y.proto of example.com/m5@v1.0.0") {
		t.Fatalf("directory clash: %v", err)
	}
	if _, err := fx.ws.Stat("out"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("written despite the clash: %v", err)
	}
}

// An exclusion removes the named module's files from what is written
// and nothing else: what its files import is written when its own
// module is not excluded, an exclusion the closure never reaches
// removes nothing, each is reported with its version; a path naming
// no build-list module, a workspace module, or given twice fails the
// export before anything is written (REQ-export-exclusion).
func TestExportExclusion(t *testing.T) {
	fx := exportFixture(t)
	var out strings.Builder
	if err := Export(ctx, fx.session(t, "."), "out", "out", ExportOptions{Exclude: []string{"example.com/m3", "example.com/m1"}}, &out); err != nil {
		t.Fatal(err)
	}
	got := tree(t, fx.ws, "out")
	want := []string{"a.proto", "m2/deep.proto", "z.proto"}
	if !equalStrings(keys(got), want) {
		t.Fatalf("tree = %v, want %v", keys(got), want)
	}
	wantOut := "example.com/a: 2 file(s)\nexample.com/m1@v1.0.0: excluded\nexample.com/m2@v1.0.0: 1 file(s)\nexample.com/m3@v1.0.0: excluded\nexported 3 file(s) to out\n"
	if out.String() != wantOut {
		t.Fatalf("report = %q, want %q", out.String(), wantOut)
	}
	// Under --all the same modules are set aside.
	out.Reset()
	if err := Export(ctx, fx.session(t, "."), "all", "all", ExportOptions{All: true, Exclude: []string{"example.com/m1"}}, &out); err != nil {
		t.Fatal(err)
	}
	if got := keys(tree(t, fx.ws, "all")); !equalStrings(got, []string{"a.proto", "m2/deep.proto", "m3/never.proto", "z.proto"}) {
		t.Fatalf("tree = %v", got)
	}
	if !strings.Contains(out.String(), "example.com/m1@v1.0.0: excluded\n") || !strings.HasSuffix(out.String(), "exported 4 file(s) to all\n") {
		t.Fatalf("report = %q", out.String())
	}
	// Refusals, on a fresh workspace: a workspace module or a path
	// given twice is refused before the build is resolved (no pin
	// written); a path naming no module of the build needs the build
	// list, so its refusal follows the pins.
	fx = exportFixture(t)
	for _, c := range []struct {
		exclude  []string
		want     string
		resolves bool
	}{
		{[]string{"example.com/a"}, "--exclude example.com/a names a workspace module", false},
		{[]string{"example.com/m1", "example.com/m2", "example.com/m1"}, "--exclude example.com/m1 given twice", false},
		{[]string{"example.com/nope"}, "--exclude example.com/nope names no module of the build", true},
	} {
		out.Reset()
		if err := Export(ctx, fx.session(t, "."), "refused", "refused", ExportOptions{Exclude: c.exclude}, &out); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%v: %v", c.exclude, err)
		}
		if _, err := fx.ws.Stat("refused"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%v: written despite the refusal", c.exclude)
		}
		if _, err := fx.ws.Stat("pb.lock"); (err == nil) != c.resolves {
			t.Fatalf("%v: the build resolved before the refusal: %v", c.exclude, err)
		}
		if out.String() != "" {
			t.Fatalf("%v: reported %q", c.exclude, out.String())
		}
	}
	// A synthesized module is excluded like any other, by its path.
	fx = newDepOn(t, osfs.New(t.TempDir()), map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", "  example.com/repo/synth: v1.0.0\n"),
		"a/a.proto": "syntax = \"proto3\";\npackage a;\nimport \"synth/s.proto\";\nmessage A { synth.S s = 1; }\n",
	})
	fx.serve(t, "example.com/repo/synth", "v1.0.0", map[string]string{"synth/s.proto": "syntax = \"proto3\";\npackage synth;\nmessage S {}\n"})
	out.Reset()
	if err := Export(ctx, fx.session(t, "."), "out", "out", ExportOptions{Exclude: []string{"example.com/repo/synth"}}, &out); err != nil {
		t.Fatal(err)
	}
	if got := keys(tree(t, fx.ws, "out")); !equalStrings(got, []string{"a.proto"}) || out.String() != "example.com/a: 1 file(s)\nexample.com/repo/synth@v1.0.0: excluded\nexported 1 file(s) to out\n" {
		t.Fatalf("synthesized: %v %q", got, out.String())
	}
}

// A build that fails to compile — an unsatisfied import, an import
// path two modules provide — fails the export in either mode with
// nothing written (REQ-export-build).
func TestExportBuildFailure(t *testing.T) {
	for _, all := range []bool{false, true} {
		fx := newDepOn(t, osfs.New(t.TempDir()), map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", ""),
			"a/a.proto": "syntax = \"proto3\";\npackage a;\nimport \"missing/m.proto\";\n",
		})
		var out strings.Builder
		var unsat *importcheck.UnsatisfiedError
		if err := Export(ctx, fx.session(t, "."), "out", "out", ExportOptions{All: all}, &out); !errors.As(err, &unsat) {
			t.Fatalf("all=%v unsatisfied: %v", all, err)
		}
		if _, err := fx.ws.Stat("out"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("all=%v written despite the unsatisfied import: %v", all, err)
		}
		fx = newDepOn(t, osfs.New(t.TempDir()), map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m1: v1.0.0\n  example.com/m6: v1.0.0\n"),
			"a/a.proto": "syntax = \"proto3\";\npackage a;\n",
		})
		fx.serve(t, "example.com/m1", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m1", ""), "m1/types.proto": "syntax = \"proto3\";\npackage m1;\n"})
		fx.serve(t, "example.com/m6", "v1.0.0", map[string]string{"pb.yaml": ws("example.com/m6", ""), "m1/types.proto": "syntax = \"proto3\";\npackage m6;\n"})
		var amb *compile.AmbiguousError
		if err := Export(ctx, fx.session(t, "."), "out", "out", ExportOptions{All: all}, &out); !errors.As(err, &amb) {
			t.Fatalf("all=%v ambiguous: %v", all, err)
		}
		if _, err := fx.ws.Stat("out"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("all=%v written despite the ambiguity: %v", all, err)
		}
		if out.String() != "" {
			t.Fatalf("reported: %q", out.String())
		}
	}
}

// A tree lands whole or not at all: a failed move leaves no output
// directory and no sibling (REQ-export-output).
func TestExportLandsWholeOrNot(t *testing.T) {
	e := &fetchtest.ErrFS{Filesystem: osfs.New(t.TempDir()), FailRenameSfx: "/out", PutFailAfter: -1}
	fx := exportFixtureOn(t, e)
	if err := e.MkdirAll("parent", 0o755); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := Export(ctx, fx.session(t, "."), "parent/out", "parent/out", ExportOptions{}, &out); !errors.Is(err, fetchtest.ErrInjected) {
		t.Fatalf("Export: %v", err)
	}
	if got := names(t, fx.ws, "parent"); len(got) != 0 {
		t.Fatalf("parent holds %v", got)
	}
	if out.String() != "" {
		t.Fatalf("reported: %q", out.String())
	}
}

// For any served module — its files regular, executable, links named
// like protobuf files, submodule entries — an export writes exactly its
// regular protobuf files, none executable, none a link, a copy of a
// well-known path never, none at all when the module is excluded,
// and two exports are byte-identical, report included
// (REQ-export-materialization, REQ-export-exclusion,
// REQ-export-determinism, module-archive.md
// REQ-archive-no-exec-materialization).
func TestExportProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 6).Draw(rt, "n")
		var files []archive.File
		want := map[string]string{"a.proto": "syntax = \"proto3\";\npackage a;\n"}
		for i := range n {
			p := fmt.Sprintf("d%d/f%d.proto", rapid.IntRange(0, 2).Draw(rt, "dir"), i)
			body := "syntax = \"proto3\";\npackage p" + fmt.Sprint(i) + ";\n"
			switch rapid.IntRange(0, 3).Draw(rt, "kind") {
			case 0:
				files = append(files, archive.File{Path: p, Body: strings.NewReader(body)})
				want[p] = body
			case 1:
				files = append(files, archive.File{Path: p, Exec: true, Body: strings.NewReader(body)})
				want[p] = body
			case 2:
				files = append(files, archive.File{Path: p, Kind: archive.KindLink, Body: strings.NewReader("a.proto")})
			case 3:
				files = append(files, archive.File{Path: fmt.Sprintf("sub%d", i), Kind: archive.KindSubmodule, Body: bytes.NewReader(bytes.Repeat([]byte{byte(i + 1)}, 20))})
			}
		}
		if rapid.Bool().Draw(rt, "wkt copy") {
			files = append(files, archive.File{Path: "google/protobuf/empty.proto", Body: strings.NewReader("syntax = \"proto3\";\n")})
		}
		var zip bytes.Buffer
		if _, err := archive.WriteZip(&zip, files); err != nil {
			rt.Fatal(err)
		}
		fx := newDepOn(t, osfs.New(t.TempDir()), map[string]string{
			"pb.work":   "use:\n  - a\n",
			"a/pb.yaml": ws("example.com/a", "  example.com/m: v1.0.0\n"),
			"a/a.proto": want["a.proto"],
		})
		fx.Endpoint("example.com/m", "v1.0.0", "zip", zip.String())
		// Excluded, sometimes: then the workspace file alone is written
		// and the module is reported as excluded.
		opts := ExportOptions{All: true}
		if rapid.Bool().Draw(rt, "exclude") {
			opts.Exclude = []string{"example.com/m"}
			want = map[string]string{"a.proto": want["a.proto"]}
		}
		var out1, out2 strings.Builder
		if err := Export(ctx, fx.session(t, "."), "out1", "out", opts, &out1); err != nil {
			rt.Fatal(err)
		}
		if err := Export(ctx, fx.session(t, "."), "out2", "out", opts, &out2); err != nil {
			rt.Fatal(err)
		}
		if excluded := strings.Contains(out1.String(), "example.com/m@v1.0.0: excluded\n"); excluded != (opts.Exclude != nil) {
			rt.Fatalf("report = %q", out1.String())
		}
		got1, got2 := tree(t, fx.ws, "out1"), tree(t, fx.ws, "out2")
		if !equalStrings(keys(got1), keys(want)) {
			rt.Fatalf("tree = %v, want %v", keys(got1), keys(want))
		}
		for p, body := range want {
			if got1[p] != body || got2[p] != body {
				rt.Fatalf("%s = %q / %q, want %q", p, got1[p], got2[p], body)
			}
		}
		if out1.String() != out2.String() {
			rt.Fatalf("reports differ: %q %q", out1.String(), out2.String())
		}
	})
}

func names(t *testing.T, fsys billy.Filesystem, dir string) []string {
	t.Helper()
	entries, err := fsys.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	var ns []string
	for _, e := range entries {
		ns = append(ns, e.Name())
	}
	sort.Strings(ns)
	return ns
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
