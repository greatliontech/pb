// Package gittest builds bare git repositories in memory for tests.
// Repositories are assembled through the plumbing layer — porcelain
// (Commit, CreateTag) merges system- and home-scoped git config, and an
// on-disk fixture drags its directory into the test's observed inputs;
// a plumbing-built repository in an in-memory filesystem touches no
// filesystem at all. Transport-level tests reach the fixture over
// file:// through a loader rooted at that same in-memory filesystem
// (ClientOptions).
package gittest

import (
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

// Failer is the failure surface the builder needs; *testing.T and
// *rapid.T both carry it, so property tests build fixtures too.
type Failer interface {
	Fatal(args ...any)
}

// Repo is a bare repository under construction in an in-memory
// filesystem. Build failures abort the test through T.
type Repo struct {
	T  Failer
	FS billy.Filesystem
	St *filesystem.Storage
}

// New initializes a bare repository in a fresh in-memory filesystem.
func New(t Failer) *Repo {
	fs := memfs.New()
	st := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
	if _, err := git.Init(st); err != nil {
		t.Fatal(err)
	}
	return &Repo{T: t, FS: fs, St: st}
}

// Sig is the fixed test identity at the given time.
func Sig(when time.Time) object.Signature {
	return object.Signature{Name: "t", Email: "t@example.com", When: when}
}

func (r *Repo) set(o interface {
	Encode(plumbing.EncodedObject) error
}) plumbing.Hash {
	eo := r.St.NewEncodedObject()
	if err := o.Encode(eo); err != nil {
		r.T.Fatal(err)
	}
	h, err := r.St.SetEncodedObject(eo)
	if err != nil {
		r.T.Fatal(err)
	}
	return h
}

// Blob writes a blob with the given content.
func (r *Repo) Blob(content string) plumbing.Hash {
	return r.raw(plumbing.BlobObject, content)
}

func (r *Repo) raw(typ plumbing.ObjectType, body string) plumbing.Hash {
	eo := r.St.NewEncodedObject()
	eo.SetType(typ)
	w, err := eo.Writer()
	if err != nil {
		r.T.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		r.T.Fatal(err)
	}
	if err := w.Close(); err != nil {
		r.T.Fatal(err)
	}
	h, err := r.St.SetEncodedObject(eo)
	if err != nil {
		r.T.Fatal(err)
	}
	return h
}

// Tree writes a tree with the given entries.
func (r *Repo) Tree(entries ...object.TreeEntry) plumbing.Hash {
	return r.set(&object.Tree{Entries: entries})
}

// LabelTree writes a one-file tree whose content is distinct per
// label, so every commit can carry its own tree.
func (r *Repo) LabelTree(label string) plumbing.Hash {
	return r.Tree(object.TreeEntry{Name: "f.proto", Mode: filemode.Regular, Hash: r.Blob(label + "\n")})
}

// Commit writes a commit carrying LabelTree(label).
func (r *Repo) Commit(label string, when time.Time, parents ...plumbing.Hash) plumbing.Hash {
	return r.CommitTree(r.LabelTree(label), label, when, parents...)
}

// CommitTree writes a commit carrying the given tree.
func (r *Repo) CommitTree(tree plumbing.Hash, message string, when time.Time, parents ...plumbing.Hash) plumbing.Hash {
	return r.set(&object.Commit{
		Author:       Sig(when),
		Committer:    Sig(when),
		Message:      message,
		TreeHash:     tree,
		ParentHashes: parents,
	})
}

// Ref writes a hash reference.
func (r *Repo) Ref(name string, target plumbing.Hash) {
	if err := r.St.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(name), target)); err != nil {
		r.T.Fatal(err)
	}
}

// Symref writes a symbolic reference.
func (r *Repo) Symref(name, target string) {
	ref := plumbing.NewSymbolicReference(plumbing.ReferenceName(name), plumbing.ReferenceName(target))
	if err := r.St.SetReference(ref); err != nil {
		r.T.Fatal(err)
	}
}

// Tag writes a lightweight tag: a ref straight at the target.
func (r *Repo) Tag(name string, target plumbing.Hash) {
	r.Ref("refs/tags/"+name, target)
}

// AnnotatedTag writes a tag object at the target plus its ref, and
// returns the tag object's hash.
func (r *Repo) AnnotatedTag(name string, target plumbing.Hash, targetType plumbing.ObjectType, when time.Time) plumbing.Hash {
	h := r.set(&object.Tag{
		Name:       name,
		Tagger:     Sig(when),
		Message:    name + "\n",
		Target:     target,
		TargetType: targetType,
	})
	r.Ref("refs/tags/"+name, h)
	return h
}

// Branch writes a branch ref.
func (r *Repo) Branch(name string, target plumbing.Hash) {
	r.Ref("refs/heads/"+name, target)
}

// Head points HEAD at a branch.
func (r *Repo) Head(branch string) {
	r.Symref(plumbing.HEAD.String(), "refs/heads/"+branch)
}

// CorruptObject writes an object under the claimed type with the body
// verbatim. The non-decoding property is the caller's to uphold: any
// body is a valid blob, so a corrupt fixture object should claim a
// structured type (tag, commit, tree) its body does not parse as.
func (r *Repo) CorruptObject(typ plumbing.ObjectType, body string) plumbing.Hash {
	return r.raw(typ, body)
}

// WriteFile plants a raw file in the bare repository's directory —
// e.g. a corrupt packed-refs.
func (r *Repo) WriteFile(name, content string) {
	if err := butil.WriteFile(r.FS, name, []byte(content), 0o644); err != nil {
		r.T.Fatal(err)
	}
}

// ClientOptions roots the file-transport loader at the fixture's
// filesystem, so "file:///" reaches the repository without the real
// filesystem becoming an observed input.
func (r *Repo) ClientOptions() []client.Option {
	return []client.Option{client.WithLoader(transport.NewFilesystemLoader(r.FS, false))}
}
