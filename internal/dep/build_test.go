package dep

import (
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/greatliontech/pb/internal/proto/importcheck"
)

func readSet(t *testing.T, fx *depFixture, name string) *descriptorpb.FileDescriptorSet {
	t.Helper()
	b := fx.read(t, name)
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal([]byte(b), &set); err != nil {
		t.Fatal(err)
	}
	return &set
}

// The verb compiles the resolved build and writes its descriptor set:
// every reachable file in topological order, the well-known imports
// among them, a required module nothing reaches absent, a module's
// copy of a well-known path absent, options and source information
// carried; the build pinned; a file already there replaced; the
// report one line (REQ-build-compile, REQ-build-set, REQ-build-output,
// REQ-build-report).
func TestBuildWritesTheSet(t *testing.T) {
	fx := exportFixture(t)
	fx.write(t, "schema.binpb", "stale")
	var out strings.Builder
	if err := Build(ctx, fx.session(t, "."), "schema.binpb", "./schema.binpb", &out); err != nil {
		t.Fatal(err)
	}
	set := readSet(t, fx, "schema.binpb")
	var paths []string
	for _, f := range set.File {
		paths = append(paths, f.GetName())
	}
	want := []string{"google/protobuf/timestamp.proto", "m2/deep.proto", "m1/types.proto", "a.proto", "z.proto"}
	if !slices.Equal(paths, want) {
		t.Fatalf("set = %v, want %v", paths, want)
	}
	for _, f := range set.File {
		if f.GetSourceCodeInfo() == nil {
			t.Fatalf("%s carries no source information", f.GetName())
		}
	}
	if out.String() != "built 5 file(s) to ./schema.binpb\n" {
		t.Fatalf("report = %q", out.String())
	}
	if lock := fx.read(t, "pb.lock"); !strings.Contains(lock, "example.com/m2") {
		t.Fatalf("the build was not pinned: %q", lock)
	}
	if got := names(t, fx.ws, "."); strings.Join(got, ",") != "a,pb.lock,pb.work,schema.binpb" {
		t.Fatalf("the root holds %v", got)
	}
}

// The output file is judged before the build is resolved: a protobuf
// source's name, an absent parent and a directory are refused with
// nothing fetched and nothing written (REQ-build-output).
func TestBuildRefusals(t *testing.T) {
	fx := exportFixture(t)
	if err := fx.ws.MkdirAll("dir", 0o755); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	fx.write(t, "f", "x")
	for _, c := range []struct{ file, want string }{
		{"a/x.proto", "names a protobuf source"},
		{"absent/schema.binpb", "parent directory of absent/schema.binpb does not exist"},
		{"f/schema.binpb", "parent of f/schema.binpb is not a directory"},
		{"dir", "dir is a directory"},
	} {
		if err := Build(ctx, fx.session(t, "."), c.file, c.file, &out); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: %v", c.file, err)
		}
	}
	if _, err := fx.ws.Stat("pb.lock"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused build resolved the build: %v", err)
	}
	if got := names(t, fx.ws, "."); strings.Join(got, ",") != "a,dir,f,pb.work" {
		t.Fatalf("after refusals the root holds %v", got)
	}
	if out.String() != "" {
		t.Fatalf("reported %q", out.String())
	}
}

// A symbolic link at the target — to a file, to a directory — is
// replaced by a regular file, its target untouched; a link on the
// way, relative or absolute, is resolved and the set lands where it
// leads; a dangling one is refused (REQ-build-output).
func TestBuildThroughLinks(t *testing.T) {
	fx := exportFixture(t)
	fx.write(t, "elsewhere/keep", "k")
	for _, c := range []struct{ target, link string }{{"elsewhere/keep", "tofile"}, {"elsewhere", "todir"}} {
		if err := fx.ws.Symlink(c.target, c.link); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		if err := Build(ctx, fx.session(t, "."), c.link, c.link, &out); err != nil {
			t.Fatalf("%s: %v", c.link, err)
		}
		if fi, err := fx.ws.Lstat(c.link); err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("%s is not a regular file: %v %v", c.link, fi, err)
		}
	}
	if fx.read(t, "elsewhere/keep") != "k" {
		t.Fatal("a link's target was written through")
	}
	if err := fx.ws.Symlink("/elsewhere", "absaway"); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := Build(ctx, fx.session(t, "."), "absaway/schema.binpb", "absaway/schema.binpb", &out); err != nil {
		t.Fatalf("through an absolute link: %v", err)
	}
	if set := readSet(t, fx, "elsewhere/schema.binpb"); len(set.File) != 5 {
		t.Fatalf("through the link the set holds %d files", len(set.File))
	}
	if err := fx.ws.Symlink("nowhere", "dangling"); err != nil {
		t.Fatal(err)
	}
	if err := Build(ctx, fx.session(t, "."), "dangling/schema.binpb", "dangling/schema.binpb", &out); err == nil || !strings.Contains(err.Error(), "dangling symbolic link") {
		t.Fatalf("a dangling link: %v", err)
	}
}

// A resolved build with no protobuf file yields an empty set: a
// zero-length file, reported as such (REQ-build-set).
func TestBuildEmptySet(t *testing.T) {
	fx := newDepOn(t, osfs.New(t.TempDir()), map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", ""),
	})
	var out strings.Builder
	if err := Build(ctx, fx.session(t, "."), "schema.binpb", "schema.binpb", &out); err != nil {
		t.Fatal(err)
	}
	if fx.read(t, "schema.binpb") != "" || out.String() != "built 0 file(s) to schema.binpb\n" {
		t.Fatalf("empty set: %q %q", fx.read(t, "schema.binpb"), out.String())
	}
}

// A build that fails to compile writes nothing (REQ-build-compile).
func TestBuildFailure(t *testing.T) {
	fx := newDepOn(t, osfs.New(t.TempDir()), map[string]string{
		"pb.work":   "use:\n  - a\n",
		"a/pb.yaml": ws("example.com/a", ""),
		"a/a.proto": "syntax = \"proto3\";\npackage a;\nimport \"missing/m.proto\";\n",
	})
	var out strings.Builder
	var unsat *importcheck.UnsatisfiedError
	if err := Build(ctx, fx.session(t, "."), "schema.binpb", "schema.binpb", &out); !errors.As(err, &unsat) {
		t.Fatalf("unsatisfied: %v", err)
	}
	if _, err := fx.ws.Stat("schema.binpb"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("written despite the failure: %v", err)
	}
	if out.String() != "" {
		t.Fatalf("reported %q", out.String())
	}
}

// Two builds of one workspace — the second over the first's client,
// its cache warm, and over the pins — are byte-identical, report
// included; the generation file changes nothing
// (REQ-build-determinism).
func TestBuildDeterminism(t *testing.T) {
	fx := exportFixture(t)
	s := fx.session(t, ".")
	var out1, out2 strings.Builder
	if err := Build(ctx, s, "one.binpb", "out", &out1); err != nil {
		t.Fatal(err)
	}
	fx.write(t, "pb.gen.yaml", "plugins:\n  - ref: ghcr.io/o/p:v1\n    out: gen\n")
	if err := Build(ctx, s, "two.binpb", "out", &out2); err != nil {
		t.Fatal(err)
	}
	if fx.read(t, "one.binpb") != fx.read(t, "two.binpb") || out1.String() != out2.String() {
		t.Fatal("two builds differ")
	}
	if len(fx.read(t, "one.binpb")) == 0 {
		t.Fatal("an empty set")
	}
}
