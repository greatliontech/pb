package modfiles

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/greatliontech/pb/internal/module/mvs"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/module/workspace"
	"github.com/greatliontech/pb/internal/testing/fetchtest"
)

var ctx = context.Background()

func ver(t *testing.T, s string) version.Version {
	t.Helper()
	v, err := version.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func ws(module, deps string) string {
	if deps == "" {
		return "module: " + module + "\n"
	}
	return "module: " + module + "\ndeps:\n" + deps
}

// Load yields workspace modules in use order with their own-directory
// files (a nested module's files excluded, non-proto files excluded),
// then build-list modules in list order with their archives' proto
// files (REQ-gen-compile's file-set clauses).
func TestLoadOrderAndMembership(t *testing.T) {
	fsys := fstest.MapFS{
		"pb.work":               {Data: []byte("use:\n  - b\n  - a\n")},
		"a/pb.yaml":             {Data: []byte(ws("example.com/a", "  example.com/m1: v1.0.0\n"))},
		"a/x.proto":             {Data: []byte("syntax = \"proto3\";\n")},
		"a/sub/y.proto":         {Data: []byte("syntax = \"proto3\";\n")},
		"a/README.md":           {Data: []byte("not proto")},
		"a/naming.rules.yaml":   {Data: []byte("celEnv: 1\nrules: []\n")},
		"a/sub/x.rules.yaml":    {Data: []byte("celEnv: 1\nrules: []\n")},
		"a/nested/n.rules.yaml": {Data: []byte("a nested module's")},
		"a/nested/pb.yaml":      {Data: []byte(ws("example.com/a/nested", ""))},
		"a/nested/z.proto":      {Data: []byte("syntax = \"proto3\";\n")},
		"b/pb.yaml":             {Data: []byte(ws("example.com/b", ""))},
		"b/deep/dir/w.proto":    {Data: []byte("syntax = \"proto3\";\n")},
		"b/deep/dir/notes.txt":  {Data: []byte("x")},
		// A directory whose name ends .proto is a directory: walked
		// through, never read as a file.
		"b/odd.proto/inner.txt":      {Data: []byte("x")},
		"a/dir.rules.yaml/inner.txt": {Data: []byte("x")},
	}
	root, err := workspace.LoadFor(fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	zip, _ := fetchtest.ModuleZip(t, map[string]string{
		"pb.yaml":        ws("example.com/m1", ""),
		"m1.proto":       "syntax = \"proto3\";\n",
		"LICENSE":        "x",
		"d/e.proto":      "syntax = \"proto3\";\n",
		"std.rules.yaml": "celEnv: 1\nrules: []\n",
	})
	var asked []string
	mods, err := Load(ctx, fsys, root, []mvs.Requirement{{Path: "example.com/m1", Version: ver(t, "v1.0.0")}},
		func(_ context.Context, p string, v version.Version) ([]byte, error) {
			asked = append(asked, p+"@"+v.String())
			return zip, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(asked, []string{"example.com/m1@v1.0.0"}) {
		t.Fatalf("archives fetched: %v", asked)
	}
	got := make([]string, len(mods))
	for i, m := range mods {
		got[i] = m.Path + "|" + m.Version + "|" + m.Dir + "|" + slices.Sorted(slices.Values(m.Protos()))[0]
	}
	want := []string{"example.com/b||b|deep/dir/w.proto", "example.com/a||a|sub/y.proto", "example.com/m1|v1.0.0||d/e.proto"}
	if !slices.Equal(got, want) {
		t.Fatalf("modules = %v, want %v", got, want)
	}
	if !slices.Equal(mods[1].Protos(), []string{"sub/y.proto", "x.proto"}) {
		t.Fatalf("a's files = %v (nested module or non-proto leaked)", mods[1].Protos())
	}
	if !slices.Equal(mods[2].Protos(), []string{"d/e.proto", "m1.proto"}) {
		t.Fatalf("m1's files = %v", mods[2].Protos())
	}
	if !mods[0].Local || !mods[1].Local || mods[2].Local {
		t.Fatal("Local flags wrong")
	}
	// Rule files ride beside the protos, module-relative, a nested
	// module's excluded, an archive's included.
	if got := slices.Sorted(maps.Keys(mods[1].Rules)); !slices.Equal(got, []string{"naming.rules.yaml", "sub/x.rules.yaml"}) {
		t.Fatalf("a's rule files = %v", got)
	}
	if got := slices.Sorted(maps.Keys(mods[2].Rules)); !slices.Equal(got, []string{"std.rules.yaml"}) {
		t.Fatalf("m1's rule files = %v", got)
	}
	if len(mods[0].Rules) != 0 {
		t.Fatalf("b's rule files = %v", mods[0].Rules)
	}
}

// A filesystem whose reads or walks fail surfaces the error.
type errFS struct {
	fstest.MapFS
	failRead string
	failWalk string
}

func (e errFS) ReadFile(name string) ([]byte, error) {
	if name == e.failRead {
		return nil, errors.New("injected read failure")
	}
	return e.MapFS.ReadFile(name)
}

func (e errFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == e.failWalk {
		return nil, errors.New("injected walk failure")
	}
	return e.MapFS.ReadDir(name)
}

func TestLoadWorkspaceFaults(t *testing.T) {
	base := fstest.MapFS{
		"pb.yaml":     {Data: []byte(ws("example.com/a", ""))},
		"sub/x.proto": {Data: []byte("syntax = \"proto3\";\n")},
	}
	root, err := workspace.LoadFor(base, ".")
	if err != nil {
		t.Fatal(err)
	}
	noZip := func(context.Context, string, version.Version) ([]byte, error) {
		t.Fatal("no externals")
		return nil, nil
	}
	if _, err := Load(ctx, errFS{MapFS: base, failRead: "sub/x.proto"}, root, nil, noZip); err == nil || !strings.Contains(err.Error(), "injected read failure") {
		t.Fatalf("read fault: %v", err)
	}
	if _, err := Load(ctx, errFS{MapFS: base, failWalk: "sub"}, root, nil, noZip); err == nil || !strings.Contains(err.Error(), "injected walk failure") {
		t.Fatalf("walk fault: %v", err)
	}
}

// Archive failures and unreadable trees surface as errors, not empty
// modules.
func TestLoadErrors(t *testing.T) {
	fsys := fstest.MapFS{"pb.yaml": {Data: []byte(ws("example.com/a", "  example.com/m1: v1.0.0\n"))}}
	root, err := workspace.LoadFor(fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	list := []mvs.Requirement{{Path: "example.com/m1", Version: ver(t, "v1.0.0")}}
	// The zip error itself must surface — nil bytes failing the archive
	// reader is a different (masking) failure.
	if _, err := Load(ctx, fsys, root, list, func(context.Context, string, version.Version) ([]byte, error) { return nil, context.Canceled }); !errors.Is(err, context.Canceled) {
		t.Fatalf("zip error = %v, want context.Canceled", err)
	}
	if _, err := Load(ctx, fsys, root, list, func(context.Context, string, version.Version) ([]byte, error) { return []byte("not a zip"), nil }); err == nil {
		t.Fatal("bad archive accepted")
	}
}

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

// A module's copy of a well-known path is loaded but is no file of the
// build: Protos leaves it out, and every enumeration reads Protos
// (REQ-gen-compile).
func TestProtosExcludeWellKnown(t *testing.T) {
	m := Module{Path: "example.com/a", Files: map[string][]byte{
		"b.proto":                           []byte("syntax = \"proto3\";\n"),
		"google/protobuf/empty.proto":       []byte("syntax = \"proto3\";\n"),
		"google/protobuf/go_features.proto": []byte("syntax = \"proto3\";\n"),
		"a.proto":                           []byte("syntax = \"proto3\";\n"),
	}}
	want := []string{"a.proto", "b.proto", "google/protobuf/go_features.proto"}
	if got := m.Protos(); !slices.Equal(got, want) {
		t.Fatalf("Protos = %v, want %v", got, want)
	}
	if len(m.Files) != 4 {
		t.Fatalf("Files changed: %d", len(m.Files))
	}
}

// A replaced pair's module carries both pairs: the requirement it
// stands for, as the module graph names it, and the source whose
// bytes it holds, the replacement's, fetched under the source's name
// and labelled `<path>@<version> => <replacement>`; a directory
// replacement holds the directory's bytes under its own name, no
// source pair, labelled the same way with the directory; an
// unreplaced pair's source is itself (workspace.md REQ-work-replace,
// REQ-work-replace-dir).
func TestLoadReplacedModuleCarriesItsSource(t *testing.T) {
	fsys := fstest.MapFS{
		"pb.work":         {Data: []byte("use:\n  - m\nreplace:\n  example.com/x: example.com/y@v2.0.0\n  example.com/z: ./forks/z\n")},
		"m/pb.yaml":       {Data: []byte(ws("example.com/m", "  example.com/x: v1.0.0\n  example.com/z: v1.0.0\n  example.com/w: v1.0.0\n"))},
		"forks/z/pb.yaml": {Data: []byte(ws("example.com/z", ""))},
		"forks/z/z.proto": {Data: []byte("syntax = \"proto3\";\n")},
	}
	root, err := workspace.LoadFor(fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	zips := map[string][]byte{}
	for _, mod := range []string{"example.com/y", "example.com/w"} {
		zips[mod], _ = fetchtest.ModuleZip(t, map[string]string{"pb.yaml": ws(mod, ""), "a.proto": "syntax = \"proto3\";\n"})
	}
	mods, err := Load(ctx, fsys, root, []mvs.Requirement{
		{Path: "example.com/x", Version: ver(t, "v1.0.0")},
		{Path: "example.com/z", Version: ver(t, "v1.0.0")},
		{Path: "example.com/w", Version: ver(t, "v1.0.0")},
	}, func(_ context.Context, p string, v version.Version) ([]byte, error) {
		asked = append(asked, p+"@"+v.String())
		return zips[p], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(asked, []string{"example.com/y@v2.0.0", "example.com/w@v1.0.0"}) {
		t.Fatalf("archives fetched: %v, want the replacement's pair and the unreplaced pair, never the replaced", asked)
	}
	if mods[0].Replaced() || mods[0].Label() != "example.com/m" {
		t.Fatalf("the workspace member: replaced %v, label %s", mods[0].Replaced(), mods[0].Label())
	}
	got := make([]string, 0, len(mods))
	for _, m := range mods[1:] {
		got = append(got, m.Path+"@"+m.Version+"|"+m.SourcePath+"@"+m.SourceVersion+"|"+m.Dir+"|"+m.Label()+"|"+strconv.FormatBool(m.Replaced()))
	}
	want := []string{
		"example.com/x@v1.0.0|example.com/y@v2.0.0||example.com/x@v1.0.0 => example.com/y@v2.0.0|true",
		"example.com/z@v1.0.0|@|forks/z|example.com/z@v1.0.0 => ./forks/z|true",
		"example.com/w@v1.0.0|example.com/w@v1.0.0||example.com/w@v1.0.0|false",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("modules:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
