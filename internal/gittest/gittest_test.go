package gittest

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/filesystem"
	"github.com/go-git/go-git/v6/storage/memory"
)

// The builder's whole surface round-trips through go-git itself: what
// it writes must read back as the objects and refs it claims to write,
// both through direct storage opens and over the file transport.
func TestBuilderRoundTrip(t *testing.T) {
	when := time.Date(2026, 7, 1, 12, 30, 0, 0, time.UTC)
	g := New(t)

	c1 := g.Commit("one", when)
	c2 := g.Commit("two", when.Add(time.Hour), c1)
	g.Branch("main", c2)
	g.Head("main")
	tagObj := g.AnnotatedTag("v1.0.0", c1, plumbing.CommitObject, when)
	g.Tag("light", c2)
	g.Symref("refs/tags/alias", "refs/heads/main")

	repo, err := git.Open(g.St, nil)
	if err != nil {
		t.Fatal(err)
	}

	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	if head.Name().String() != "refs/heads/main" || head.Hash() != c2 {
		t.Fatalf("HEAD = %s %s, want refs/heads/main at %s", head.Name(), head.Hash(), c2)
	}

	commit, err := repo.CommitObject(c2)
	if err != nil {
		t.Fatal(err)
	}
	if commit.Message != "two" || !commit.Committer.When.Equal(when.Add(time.Hour)) ||
		len(commit.ParentHashes) != 1 || commit.ParentHashes[0] != c1 {
		t.Fatalf("c2 read back as %+v", commit)
	}
	if commit.Committer.Name != "t" || commit.Committer.Email != "t@example.com" {
		t.Fatalf("committer identity read back as %+v", commit.Committer)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	f, err := tree.File("f.proto")
	if err != nil {
		t.Fatal(err)
	}
	content, err := f.Contents()
	if err != nil {
		t.Fatal(err)
	}
	if content != "two\n" {
		t.Fatalf("f.proto = %q, want the label plus newline", content)
	}

	tag, err := repo.TagObject(tagObj)
	if err != nil {
		t.Fatal(err)
	}
	if tag.Name != "v1.0.0" || tag.Target != c1 || tag.TargetType != plumbing.CommitObject {
		t.Fatalf("tag read back as %+v", tag)
	}
	if tag.Message != "v1.0.0\n" {
		t.Fatalf("tag message %q", tag.Message)
	}
	tagRef, err := g.St.Reference(plumbing.ReferenceName("refs/tags/v1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if tagRef.Hash() != tagObj {
		t.Fatalf("annotated tag ref at %s, want the tag object %s", tagRef.Hash(), tagObj)
	}
	lightRef, err := g.St.Reference(plumbing.ReferenceName("refs/tags/light"))
	if err != nil {
		t.Fatal(err)
	}
	if lightRef.Hash() != c2 {
		t.Fatalf("lightweight tag at %s, want %s", lightRef.Hash(), c2)
	}
	aliasRef, err := g.St.Reference(plumbing.ReferenceName("refs/tags/alias"))
	if err != nil {
		t.Fatal(err)
	}
	if aliasRef.Type() != plumbing.SymbolicReference || aliasRef.Target().String() != "refs/heads/main" {
		t.Fatalf("symref read back as %+v", aliasRef)
	}
}

// CorruptObject's guarantee is an object of the claimed type whose
// body does not decode as that type; WriteFile plants raw repository
// files. Both round-trip.
func TestBuilderCorruption(t *testing.T) {
	g := New(t)
	h := g.CorruptObject(plumbing.TagObject, "not a decodable tag object")

	repo, err := git.Open(g.St, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.TagObject(h); err == nil || err == plumbing.ErrObjectNotFound {
		t.Fatalf("corrupt tag decode err = %v, want a decode failure distinct from not-found", err)
	}
	eo, err := g.St.EncodedObject(plumbing.TagObject, h)
	if err != nil {
		t.Fatal(err)
	}
	r, err := eo.Reader()
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "not a decodable tag object" {
		t.Fatalf("corrupt body = %q", body)
	}

	g.WriteFile("packed-refs", "garbage\n")
	fh, err := g.FS.Open("packed-refs")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	planted, err := io.ReadAll(fh)
	if err != nil {
		t.Fatal(err)
	}
	if string(planted) != "garbage\n" {
		t.Fatalf("packed-refs = %q", planted)
	}
}

// The transport seam: a fixture is reachable over file:// through the
// rooted loader, and only through it — the fixture never leaks onto
// the real filesystem.
func TestBuilderTransport(t *testing.T) {
	when := time.Date(2026, 7, 1, 12, 30, 0, 0, time.UTC)
	g := New(t)
	c := g.Commit("only", when)
	g.Branch("main", c)
	g.Head("main")

	repo, err := git.CloneContext(context.Background(), memory.NewStorage(), nil, &git.CloneOptions{
		URL:           "file:///",
		Tags:          git.AllTags,
		ClientOptions: g.ClientOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.CommitObject(c)
	if err != nil {
		t.Fatal(err)
	}
	if got.Message != "only" {
		t.Fatalf("cloned commit message %q", got.Message)
	}
}

// recordingFailer stands in for testing.T on a fixture that is meant
// to fail: it records the Fatal and aborts the builder by panicking,
// as testing.T aborts via runtime.Goexit.
type recordingFailer struct {
	hit bool
}

func (r *recordingFailer) Fatal(args ...any) {
	r.hit = true
	panic("gittest: recorded Fatal")
}

// failFS turns every write into an error, so storage-level failures
// become reachable.
type failFS struct {
	billy.Filesystem
}

func (failFS) Create(string) (billy.File, error) { return nil, errors.New("failFS: create") }
func (failFS) OpenFile(string, int, os.FileMode) (billy.File, error) {
	return nil, errors.New("failFS: open")
}
func (failFS) TempFile(string, string) (billy.File, error) { return nil, errors.New("failFS: temp") }
func (failFS) MkdirAll(string, os.FileMode) error          { return errors.New("failFS: mkdir") }

func expectFatal(t *testing.T, op func(g *Repo)) {
	t.Helper()
	rec := &recordingFailer{}
	fs := failFS{memfs.New()}
	g := &Repo{T: rec, FS: fs, St: filesystem.NewStorage(fs, cache.NewObjectLRUDefault())}
	defer func() {
		v := recover()
		if !rec.hit {
			t.Fatalf("builder swallowed the storage failure instead of failing the test (recovered: %v)", v)
		}
	}()
	op(g)
}

// Every builder step that writes through the filesystem forwards a
// storage failure to the test — a fixture must never come up silently
// half-built.
func TestBuilderSurfacesStorageFailures(t *testing.T) {
	cases := []struct {
		name string
		op   func(g *Repo)
	}{
		{"Blob", func(g *Repo) { g.Blob("x") }},
		{"CorruptObject", func(g *Repo) { g.CorruptObject(plumbing.TagObject, "x") }},
		{"Tree", func(g *Repo) { g.Tree() }},
		{"Ref", func(g *Repo) { g.Ref("refs/heads/x", plumbing.ZeroHash) }},
		{"Symref", func(g *Repo) { g.Symref("refs/tags/alias", "refs/heads/x") }},
		{"WriteFile", func(g *Repo) { g.WriteFile("f", "x") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { expectFatal(t, c.op) })
	}
}

// Blob writes content-addressed bytes: the same content twice is one
// object, and the hash matches git's blob hashing.
func TestBuilderBlobIdentity(t *testing.T) {
	g := New(t)
	a := g.Blob("same")
	b := g.Blob("same")
	if a != b {
		t.Fatalf("identical blobs hashed %s and %s", a, b)
	}
	obj := &object.Blob{}
	eo, err := g.St.EncodedObject(plumbing.BlobObject, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := obj.Decode(eo); err != nil {
		t.Fatal(err)
	}
	if obj.Size != int64(len("same")) {
		t.Fatalf("blob size %d", obj.Size)
	}
}
