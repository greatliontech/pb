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

	"github.com/greatliontech/pb/internal/check/lintfile"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/testing/fetchtest"
	"github.com/greatliontech/pb/internal/testing/gittest"
	"github.com/greatliontech/pb/internal/testing/scratchtest"
)

// scratch is a fresh directory inside the module with the repository
// search bounded at the scratch root through GIT_CEILING_DIRECTORIES,
// so a scratch directory in no repository of its own never reaches
// this repository's .git above it.
func scratch(t *testing.T) string {
	t.Helper()
	dir := scratchtest.Dir(t)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	return dir
}

// repo builds a repository in a scratch directory through the object
// store alone, no working tree and no configuration consulted: the
// workspace root in a subdirectory, one commit holding the module's
// files and a nested module, then a second commit changing them, a
// tag on the first, HEAD on the second.
func repo(t *testing.T) (dir string) {
	t.Helper()
	dir = scratch(t)
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
	src := Sources{Repo: func() (*git.Repository, string, error) { return RepoOf(filepath.Join(dir, "ws")) }}
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
	outside := Sources{Repo: func() (*git.Repository, string, error) { return RepoOf(scratch(t)) }}
	if _, err := Materialize(context.Background(), lintfile.Base{Form: lintfile.BaseRef, Value: "HEAD"}, m, outside); err == nil || !strings.Contains(err.Error(), "lies in no git repository") {
		t.Fatalf("no repository: %v", err)
	}
	// The .git file form — a working tree pointing at its repository
	// directory, absolutely or relatively — opens as the directory
	// form does.
	pointer := scratch(t)
	for form, target := range map[string]string{"absolute": filepath.Join(dir, git.GitDirName), "relative": filepath.Join("..", filepath.Base(dir), git.GitDirName)} {
		if err := os.WriteFile(filepath.Join(pointer, git.GitDirName), []byte("gitdir: "+target+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		b, err := Materialize(context.Background(), lintfile.Base{Form: lintfile.BaseRef, Value: "v1.0.0"}, Module{Path: "example.com/a", Dir: "ws/a"}, Sources{Repo: func() (*git.Repository, string, error) { return RepoOf(pointer) }})
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
	repo, rel, err := RepoOf(dir)
	if err != nil || rel != "" || repo == nil {
		t.Fatalf("RepoOf(root) = %q %v", rel, err)
	}
	if _, rel, err := RepoOf(filepath.Join(dir, "ws", "a")); err != nil || rel != "ws/a" {
		t.Fatalf("RepoOf(nested) = %q %v", rel, err)
	}
	if _, rel, err := RepoOf(filepath.Join(dir, "ws", "a", "..")); err != nil || rel != "ws" {
		t.Fatalf("RepoOf(unclean) = %q %v", rel, err)
	}
	t.Chdir(dir)
	if _, rel, err := RepoOf(filepath.Join("ws", "a")); err != nil || rel != "ws/a" {
		t.Fatalf("RepoOf(relative) = %q %v", rel, err)
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

// The repository search reads GIT_CEILING_DIRECTORIES as git does —
// absolute entries alone, by identity until an empty entry and by
// spelling after it — never enters a ceiling, an ancestor spelled
// through a link included, never excludes the start directory, and
// searches the filesystem's root last (REQ-break-base-materialized).
func TestRepositorySearch(t *testing.T) {
	dir := scratchtest.Dir(t)
	real := filepath.Join(dir, "real")
	link := filepath.Join(dir, "link")
	// A second link into a subdirectory, so that ".." after it
	// resolves to that subdirectory's parent, not the link's.
	deep := filepath.Join(dir, "deep")
	link2 := filepath.Join(dir, "link2")
	for _, d := range []string{filepath.Join(real, "a", "b"), filepath.Join(deep, "target"), filepath.Join(deep, "other"), filepath.Join(dir, "other")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(deep, "target"), link2); err != nil {
		t.Fatal(err)
	}
	sep := string(filepath.Separator)
	got := ceilings("relative:" + sep + ":" + link + sep + ":" + filepath.Join(dir, "nonesuch") + "::" + dir + sep + "x" + sep + ".." + sep + "y" + sep + ":" + sep)
	if len(got) != 2 || got[0].path != link || got[0].dir == nil || got[1].path != dir+sep+"x"+sep+".."+sep+"y" || got[1].dir != nil {
		t.Fatalf("ceilings = %+v", got)
	}
	// An entry resolves as the operating system resolves it: through
	// the link first, then up.
	if c := ceilings(link2 + sep + ".." + sep + "other"); len(c) != 1 || !c[0].is(filepath.Join(deep, "other")) || c[0].is(filepath.Join(dir, "other")) {
		t.Fatalf("an entry through a link and up: %+v", c)
	}
	never := func(string) bool { return false }
	at := func(root string) func(string) bool { return func(d string) bool { return d == root } }
	viaLink := filepath.Join(link, "a", "b")
	viaReal := filepath.Join(real, "a", "b")
	if root, ok := repositoryRoot(viaLink, nil, never); ok || root != "" {
		t.Fatalf("no .git anywhere: %q %v", root, ok)
	}
	if root, ok := repositoryRoot(viaLink, nil, at(filepath.Join(link, "a"))); !ok || root != filepath.Join(link, "a") {
		t.Fatalf("the nearest ancestor: %q %v", root, ok)
	}
	if root, ok := repositoryRoot(viaLink, nil, at(string(filepath.Separator))); !ok || root != string(filepath.Separator) {
		t.Fatalf("the filesystem's root searched last: %q %v", root, ok)
	}
	// By identity, the ceiling stops a walk spelled either way; by
	// spelling, only the walk spelled as the entry is.
	if _, ok := repositoryRoot(viaLink, ceilings(real), at(dir)); ok {
		t.Fatal("entered a ceiling named by identity, reached through a link")
	}
	if _, ok := repositoryRoot(viaReal, ceilings(link), at(dir)); ok {
		t.Fatal("entered a ceiling named by identity through a link, reached directly")
	}
	if _, ok := repositoryRoot(viaLink, ceilings(":"+link), at(dir)); ok {
		t.Fatal("entered a ceiling named by spelling, reached as spelled")
	}
	if root, ok := repositoryRoot(viaReal, ceilings(":"+link), at(dir)); !ok || root != dir {
		t.Fatalf("a ceiling named by spelling stopped a walk spelled otherwise: %q %v", root, ok)
	}
	// A ceiling not on the path stops nothing; the start directory
	// itself is never excluded, even when named.
	if root, ok := repositoryRoot(viaLink, ceilings(filepath.Join(dir, "elsewhere")), at(dir)); !ok || root != dir {
		t.Fatalf("an unrelated ceiling: %q %v", root, ok)
	}
	if root, ok := repositoryRoot(viaLink, ceilings(viaLink), at(dir)); !ok || root != dir {
		t.Fatalf("the start directory as a ceiling: %q %v", root, ok)
	}
	// A ceiling that does not exist is no ceiling.
	if root, ok := repositoryRoot(viaLink, ceilings(filepath.Join(dir, "nonesuch")), at(dir)); !ok || root != dir {
		t.Fatalf("an absent ceiling: %q %v", root, ok)
	}
}
