package breaking

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/greatliontech/pb/internal/check/lintfile"
	"github.com/greatliontech/pb/internal/gitdir"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/testing/fetchtest"
	"github.com/greatliontech/pb/internal/testing/gittest"
	"github.com/greatliontech/pb/internal/testing/scratchtest"
)

// repo builds a repository in a scratch directory through the object
// store alone, no working tree and no configuration consulted: the
// workspace root in a subdirectory, one commit holding the module's
// files and a nested module, then a second commit changing them, a
// tag on the first, HEAD on the second.
func repo(t *testing.T) (dir string) {
	t.Helper()
	dir = scratchtest.NoRepo(t)
	r := gittest.NewAt(t, osfs.New(dir), git.GitDirName)
	file := func(name, content string) object.TreeEntry {
		return object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: r.Blob(content)}
	}
	sub := func(name string, entries ...object.TreeEntry) object.TreeEntry {
		return object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: r.Tree(entries...)}
	}
	// A directory named as a module file marks a nested module as the
	// working tree's walk takes it; a symbolic link is no file.
	module := func(x string) plumbing.Hash {
		return r.Tree(sub("ws",
			sub("a",
				file("README.md", "not proto"),
				sub("dirmarker", file("d.proto", "syntax = \"proto3\";\n"), sub("pb.yaml", file("keep", "x"))),
				object.TreeEntry{Name: "link.proto", Mode: filemode.Symlink, Hash: r.Blob("x.proto")},
				sub("nested", file("n.proto", "syntax = \"proto3\";\n"), file("pb.yaml", "module: example.com/a/nested\n")),
				file("pb.yaml", "module: example.com/a\n"),
				sub("sub", file("y.proto", "syntax = \"proto3\";\n")),
				file("x.proto", "syntax = \"proto3\";\nmessage X { string "+x+" = 1; }\n"),
			),
			file("pb.work", "use:\n  - a\n"),
		))
	}
	first := r.CommitTree(module("old"), "first", time.Unix(0, 0))
	second := r.CommitTree(module("new"), "second", time.Unix(1, 0), first)
	r.Tag("v1.0.0", first)
	r.Branch("main", second)
	r.Head("main")
	return dir
}

func names(files map[string][]byte) string {
	var out []string
	for p := range files {
		out = append(out, p)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// A git reference yields the module's directory at that reference,
// relative to the repository's root as the root lies now, protobuf
// files alone, a nested module excluded — one marked by any entry
// named as a module file — and a symbolic link no file; an
// unresolvable reference, a directory the tree lacks, and a root in
// no repository fail naming the form and the cause
// (REQ-break-base-materialized).
func TestFromRef(t *testing.T) {
	dir := repo(t)
	src := Sources{Repo: func() (*git.Repository, string, error) { return gitdir.RepoOf(filepath.Join(dir, "ws")) }}
	m := Module{Path: "example.com/a", Dir: "a"}
	for _, ref := range []string{"v1.0.0", "HEAD~1"} {
		b, err := Materialize(context.Background(), lintfile.Base{Form: lintfile.BaseRef, Value: ref}, m, src)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if names(b.Files) != "sub/y.proto,x.proto" || !strings.Contains(string(b.Files["x.proto"]), "old") || b.Label != "example.com/a@"+ref {
			t.Fatalf("%s: %s %q %s", ref, names(b.Files), b.Files["x.proto"], b.Label)
		}
	}
	b, err := Materialize(context.Background(), lintfile.Base{Form: lintfile.BaseRef, Value: "HEAD"}, m, src)
	if err != nil || !strings.Contains(string(b.Files["x.proto"]), "new") {
		t.Fatalf("HEAD: %v %q", err, b.Files["x.proto"])
	}
	for name, c := range map[string]struct {
		base lintfile.Base
		m    Module
		want string
	}{
		"no such ref":  {lintfile.Base{Form: lintfile.BaseRef, Value: "nonesuch"}, m, "ref nonesuch: the repository resolves no such reference"},
		"no such dir":  {lintfile.Base{Form: lintfile.BaseRef, Value: "v1.0.0"}, Module{Path: "example.com/b", Dir: "b"}, "has no directory ws/b"},
		"bad version":  {lintfile.Base{Form: lintfile.BaseVersion, Value: "nope"}, m, "version nope:"},
		"unknown form": {lintfile.Base{Form: "tag"}, m, `"tag" is no form`},
	} {
		_, err := Materialize(context.Background(), c.base, c.m, src)
		if err == nil || !errors.Is(err, ErrBase) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// No repository source wired.
	if _, err := Materialize(context.Background(), lintfile.Base{Form: lintfile.BaseRef, Value: "HEAD"}, m, Sources{}); err == nil || !strings.Contains(err.Error(), "no repository source is wired") {
		t.Fatalf("no repo source: %v", err)
	}
	// A root in no repository: the search stops at the ceiling.
	outside := Sources{Repo: func() (*git.Repository, string, error) { return gitdir.RepoOf(scratchtest.NoRepo(t)) }}
	if _, err := Materialize(context.Background(), lintfile.Base{Form: lintfile.BaseRef, Value: "HEAD"}, m, outside); err == nil || !strings.Contains(err.Error(), "lies in no git repository") {
		t.Fatalf("no repository: %v", err)
	}
	// The .git file form — a working tree pointing at its repository
	// directory, absolutely or relatively — opens as the directory
	// form does.
	pointer := scratchtest.NoRepo(t)
	for form, target := range map[string]string{"absolute": filepath.Join(dir, git.GitDirName), "relative": filepath.Join("..", filepath.Base(dir), git.GitDirName)} {
		if err := os.WriteFile(filepath.Join(pointer, git.GitDirName), []byte("gitdir: "+target+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		b, err := Materialize(context.Background(), lintfile.Base{Form: lintfile.BaseRef, Value: "v1.0.0"}, Module{Path: "example.com/a", Dir: "ws/a"}, Sources{Repo: func() (*git.Repository, string, error) { return gitdir.RepoOf(pointer) }})
		if err != nil {
			t.Fatalf("through a %s .git file: %v", form, err)
		}
		if names(b.Files) != "sub/y.proto,x.proto" {
			t.Fatalf("through a %s .git file: %s", form, names(b.Files))
		}
	}
	// The repository's root itself as the workspace root; a relative
	// directory is made absolute; a nested directory yields its
	// slash-separated path within the repository.
	repo, rel, err := gitdir.RepoOf(dir)
	if err != nil || rel != "" || repo == nil {
		t.Fatalf("gitdir.RepoOf(root) = %q %v", rel, err)
	}
	if _, rel, err := gitdir.RepoOf(filepath.Join(dir, "ws", "a")); err != nil || rel != "ws/a" {
		t.Fatalf("gitdir.RepoOf(nested) = %q %v", rel, err)
	}
	if _, rel, err := gitdir.RepoOf(filepath.Join(dir, "ws", "a", "..")); err != nil || rel != "ws" {
		t.Fatalf("gitdir.RepoOf(unclean) = %q %v", rel, err)
	}
	t.Chdir(dir)
	if _, rel, err := gitdir.RepoOf(filepath.Join("ws", "a")); err != nil || rel != "ws/a" {
		t.Fatalf("gitdir.RepoOf(relative) = %q %v", rel, err)
	}
	b, err = Materialize(context.Background(), lintfile.Base{Form: lintfile.BaseRef, Value: "v1.0.0"}, Module{Path: "example.com/a", Dir: "ws/a"}, Sources{Repo: func() (*git.Repository, string, error) { return repo, rel, nil }})
	if err != nil || names(b.Files) != "sub/y.proto,x.proto" {
		t.Fatalf("at the repository root: %v %s", err, names(b.Files))
	}
}

// A tagged version and the pinned version — the highest the lockfile
// holds — are acquired through the client at the module's own path
// and yield its protobuf files; no pin fails naming the module
// (REQ-break-base).
func TestFromVersion(t *testing.T) {
	zip, _ := fetchtest.ModuleZip(t, map[string]string{
		"pb.yaml":   "module: example.com/a\n",
		"x.proto":   "syntax = \"proto3\";\n",
		"d/y.proto": "syntax = \"proto3\";\n",
		"LICENSE":   "x",
	})
	var asked []string
	src := Sources{
		Zip: func(_ context.Context, p string, v version.Version) ([]byte, error) {
			asked = append(asked, p+"@"+v.String())
			if v.String() == "v9.9.9" {
				return nil, errors.New("not served")
			}
			return zip, nil
		},
		Lock: &lockfile.File{Modules: []lockfile.ModulePin{{Path: "example.com/a", Version: "v1.0.0"}, {Path: "example.com/other", Version: "v3.0.0"}, {Path: "example.com/a", Version: "v1.2.0"}}},
	}
	m := Module{Path: "example.com/a", Dir: "a"}
	b, err := Materialize(context.Background(), lintfile.Base{Form: lintfile.BaseVersion, Value: "v1.1.0"}, m, src)
	if err != nil || names(b.Files) != "d/y.proto,x.proto" || b.Label != "example.com/a@v1.1.0" {
		t.Fatalf("version: %v %+v", err, b)
	}
	b, err = Materialize(context.Background(), lintfile.Base{Form: lintfile.BasePinned}, m, src)
	if err != nil || b.Label != "example.com/a@v1.2.0" {
		t.Fatalf("pinned: %v %+v", err, b)
	}
	if strings.Join(asked, " ") != "example.com/a@v1.1.0 example.com/a@v1.2.0" {
		t.Fatalf("asked %v", asked)
	}
	if _, err := Materialize(context.Background(), lintfile.Base{Form: lintfile.BaseVersion, Value: "v9.9.9"}, m, src); err == nil || !errors.Is(err, ErrBase) || !strings.Contains(err.Error(), "version v9.9.9: not served") {
		t.Fatalf("unserved: %v", err)
	}
	if _, err := Materialize(context.Background(), lintfile.Base{Form: lintfile.BasePinned}, Module{Path: "example.com/none"}, src); err == nil || !strings.Contains(err.Error(), "pinned: the lockfile pins no version of example.com/none") {
		t.Fatalf("unpinned: %v", err)
	}
	// A pin whose version does not parse is named, never skipped.
	bad := Sources{Zip: src.Zip, Lock: &lockfile.File{Modules: []lockfile.ModulePin{{Path: "example.com/a", Version: "latest"}, {Path: "example.com/a", Version: "v1.0.0"}}}}
	if _, err := Materialize(context.Background(), lintfile.Base{Form: lintfile.BasePinned}, m, bad); err == nil || !strings.Contains(err.Error(), `pins example.com/a at "latest", which is no version`) {
		t.Fatalf("malformed pin: %v", err)
	}
	// The pinned form's unserved version is named.
	unserved := Sources{Zip: src.Zip, Lock: &lockfile.File{Modules: []lockfile.ModulePin{{Path: "example.com/a", Version: "v9.9.9"}}}}
	if _, err := Materialize(context.Background(), lintfile.Base{Form: lintfile.BasePinned}, m, unserved); err == nil || !strings.Contains(err.Error(), "pinned v9.9.9: not served") {
		t.Fatalf("pinned unserved: %v", err)
	}
	if _, err := Materialize(context.Background(), lintfile.Base{Form: lintfile.BasePinned}, m, Sources{Zip: src.Zip}); err == nil || !strings.Contains(err.Error(), "pins no version") {
		t.Fatalf("no lockfile: %v", err)
	}
}

// A descriptor set file is read as it is, trusted by being named: the
// set returned with the form as the label; a file that cannot be read
// or does not parse as a descriptor set fails naming the form and the
// cause (REQ-break-base-materialized).
func TestFromFile(t *testing.T) {
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{Name: proto.String("a.proto"), Package: proto.String("a")}}}
	data, err := proto.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	src := Sources{Read: func(p string) ([]byte, error) {
		switch p {
		case "build/base.binpb":
			return data, nil
		case "build/text.binpb":
			return []byte("syntax = \"proto3\";"), nil
		}
		return nil, errors.New("no such file")
	}}
	form := lintfile.Base{Form: lintfile.BaseFile, Value: "build/base.binpb"}
	b, err := Materialize(context.Background(), form, Module{Path: "example.com/a"}, src)
	if err != nil || b.Files != nil || b.Label != "file build/base.binpb" || len(b.Set.GetFile()) != 1 || b.Set.File[0].GetName() != "a.proto" {
		t.Fatalf("file: %v %+v", err, b)
	}
	if _, err := Materialize(context.Background(), lintfile.Base{Form: lintfile.BaseFile, Value: "build/none.binpb"}, Module{}, src); err == nil || !errors.Is(err, ErrBase) || !strings.Contains(err.Error(), "file build/none.binpb: no such file") {
		t.Fatalf("unreadable: %v", err)
	}
	if _, err := Materialize(context.Background(), lintfile.Base{Form: lintfile.BaseFile, Value: "build/text.binpb"}, Module{}, src); err == nil || !errors.Is(err, ErrBase) || !strings.Contains(err.Error(), "file build/text.binpb: not a descriptor set") {
		t.Fatalf("not a set: %v", err)
	}
	if _, err := Materialize(context.Background(), form, Module{}, Sources{}); err == nil || !strings.Contains(err.Error(), "no workspace source is wired") {
		t.Fatalf("no source: %v", err)
	}
}
