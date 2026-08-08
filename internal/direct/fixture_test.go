package direct

import (
	"context"
	"time"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	butil "github.com/go-git/go-billy/v6/util"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage/filesystem"
)

// failer is the failure surface the fixture needs; both *testing.T and
// *rapid.T carry it, so the property tests build fixtures too.
type failer interface {
	Fatal(args ...any)
}

// repoFixture builds a bare origin repository in an in-memory
// filesystem through the plumbing layer — porcelain (Commit, CreateTag)
// merges system- and home-scoped git config, and an on-disk fixture
// drags its directory into the test's observed inputs; this way the
// tests touch no filesystem at all. Fetch reaches it over file://
// through a loader rooted at the same in-memory filesystem.
type repoFixture struct {
	t  failer
	fs billy.Filesystem
	st *filesystem.Storage
}

func newFixture(t failer) *repoFixture {
	fs := memfs.New()
	st := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
	if _, err := git.Init(st); err != nil {
		t.Fatal(err)
	}
	return &repoFixture{t: t, fs: fs, st: st}
}

func (f *repoFixture) set(o interface {
	Encode(plumbing.EncodedObject) error
}) plumbing.Hash {
	eo := f.st.NewEncodedObject()
	if err := o.Encode(eo); err != nil {
		f.t.Fatal(err)
	}
	h, err := f.st.SetEncodedObject(eo)
	if err != nil {
		f.t.Fatal(err)
	}
	return h
}

func (f *repoFixture) blob(content string) plumbing.Hash {
	eo := f.st.NewEncodedObject()
	eo.SetType(plumbing.BlobObject)
	w, err := eo.Writer()
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		f.t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		f.t.Fatal(err)
	}
	h, err := f.st.SetEncodedObject(eo)
	if err != nil {
		f.t.Fatal(err)
	}
	return h
}

// tree writes a one-file tree whose content is distinct per label, so
// every commit can carry its own tree.
func (f *repoFixture) tree(label string) plumbing.Hash {
	return f.set(&object.Tree{Entries: []object.TreeEntry{
		{Name: "f.proto", Mode: filemode.Regular, Hash: f.blob(label + "\n")},
	}})
}

func sig(when time.Time) object.Signature {
	return object.Signature{Name: "t", Email: "t@example.com", When: when}
}

func (f *repoFixture) commit(label string, when time.Time, parents ...plumbing.Hash) plumbing.Hash {
	return f.set(&object.Commit{
		Author:       sig(when),
		Committer:    sig(when),
		Message:      label,
		TreeHash:     f.tree(label),
		ParentHashes: parents,
	})
}

func (f *repoFixture) ref(name string, target plumbing.Hash) {
	if err := f.st.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(name), target)); err != nil {
		f.t.Fatal(err)
	}
}

// tag writes a lightweight tag: a ref straight at the target.
func (f *repoFixture) tag(name string, target plumbing.Hash) {
	f.ref("refs/tags/"+name, target)
}

// annotatedTag writes a tag object at the target plus its ref.
func (f *repoFixture) annotatedTag(name string, target plumbing.Hash, targetType plumbing.ObjectType, when time.Time) {
	h := f.set(&object.Tag{
		Name:       name,
		Tagger:     sig(when),
		Message:    name + "\n",
		Target:     target,
		TargetType: targetType,
	})
	f.ref("refs/tags/"+name, h)
}

func (f *repoFixture) branch(name string, target plumbing.Hash) {
	f.ref("refs/heads/"+name, target)
}

func (f *repoFixture) symref(name, target string) {
	ref := plumbing.NewSymbolicReference(plumbing.ReferenceName(name), plumbing.ReferenceName(target))
	if err := f.st.SetReference(ref); err != nil {
		f.t.Fatal(err)
	}
}

func (f *repoFixture) head(branch string) {
	f.symref(plumbing.HEAD.String(), "refs/heads/"+branch)
}

// corruptObject writes an object of the claimed type whose body does
// not decode as that type.
func (f *repoFixture) corruptObject(typ plumbing.ObjectType, body string) plumbing.Hash {
	eo := f.st.NewEncodedObject()
	eo.SetType(typ)
	w, err := eo.Writer()
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		f.t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		f.t.Fatal(err)
	}
	h, err := f.st.SetEncodedObject(eo)
	if err != nil {
		f.t.Fatal(err)
	}
	return h
}

// writeFile plants a raw file in the bare repository's directory —
// e.g. a corrupt packed-refs.
func (f *repoFixture) writeFile(name, content string) {
	if err := butil.WriteFile(f.fs, name, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// open opens the fixture's storage directly, with no clone in between:
// the storage-level failure modes it serves (corrupt objects, broken
// refs, dangling parents) cannot ride through a healthy fetch, and the
// fetch path itself is pinned by the clone-based tests.
func (f *repoFixture) open() *Repo {
	r, err := git.Open(f.st, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return &Repo{r: r}
}

func (f *repoFixture) fetchClientOptions() []client.Option {
	return []client.Option{client.WithLoader(transport.NewFilesystemLoader(f.fs, false))}
}

// fetch clones the fixture through the in-memory file transport.
func (f *repoFixture) fetch() *Repo {
	fe := Fetcher{clientOptions: f.fetchClientOptions()}
	repo, err := fe.Fetch(context.Background(), "file:///")
	if err != nil {
		f.t.Fatal(err)
	}
	return repo
}
