package modfiles

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/greatliontech/pb/internal/modfetchtest"
	"github.com/greatliontech/pb/internal/mvs"
	"github.com/greatliontech/pb/internal/version"
	"github.com/greatliontech/pb/internal/workspace"
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
		"pb.work":              {Data: []byte("use:\n  - b\n  - a\n")},
		"a/pb.yaml":            {Data: []byte(ws("example.com/a", "  example.com/m1: v1.0.0\n"))},
		"a/x.proto":            {Data: []byte("syntax = \"proto3\";\n")},
		"a/sub/y.proto":        {Data: []byte("syntax = \"proto3\";\n")},
		"a/README.md":          {Data: []byte("not proto")},
		"a/nested/pb.yaml":     {Data: []byte(ws("example.com/a/nested", ""))},
		"a/nested/z.proto":     {Data: []byte("syntax = \"proto3\";\n")},
		"b/pb.yaml":            {Data: []byte(ws("example.com/b", ""))},
		"b/deep/dir/w.proto":   {Data: []byte("syntax = \"proto3\";\n")},
		"b/deep/dir/notes.txt": {Data: []byte("x")},
		// A directory whose name ends .proto is a directory: walked
		// through, never read as a file.
		"b/odd.proto/inner.txt": {Data: []byte("x")},
	}
	root, err := workspace.LoadFor(fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	zip, _ := modfetchtest.ModuleZip(t, map[string]string{
		"pb.yaml":   ws("example.com/m1", ""),
		"m1.proto":  "syntax = \"proto3\";\n",
		"LICENSE":   "x",
		"d/e.proto": "syntax = \"proto3\";\n",
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
