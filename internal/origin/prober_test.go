package origin

import (
	"context"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage/filesystem"
)

// The go-git wiring lists a real repository's references, hermetically.
// The fixture is a bare repository in an in-memory filesystem, its
// objects and refs written through the plumbing layer — porcelain
// (Commit, CreateTag) merges system- and home-scoped git config, and any
// on-disk fixture drags its directory into the test's observed inputs;
// this way the test touches no filesystem at all. It is listed over
// file:// through a loader rooted at that same in-memory filesystem.
func TestGitProberListsFixtureRepo(t *testing.T) {
	fs := memfs.New()
	st := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
	if _, err := git.Init(st); err != nil {
		t.Fatal(err)
	}

	blob := st.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	bw, err := blob.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bw.Write([]byte("syntax\n")); err != nil {
		t.Fatal(err)
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}
	blobHash, err := st.SetEncodedObject(blob)
	if err != nil {
		t.Fatal(err)
	}

	tree := &object.Tree{Entries: []object.TreeEntry{
		{Name: "f.proto", Mode: filemode.Regular, Hash: blobHash},
	}}
	to := st.NewEncodedObject()
	if err := tree.Encode(to); err != nil {
		t.Fatal(err)
	}
	treeHash, err := st.SetEncodedObject(to)
	if err != nil {
		t.Fatal(err)
	}

	sig := object.Signature{Name: "t", Email: "t@example.com", When: time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)}
	commit := &object.Commit{Author: sig, Committer: sig, Message: "init", TreeHash: treeHash}
	co := st.NewEncodedObject()
	if err := commit.Encode(co); err != nil {
		t.Fatal(err)
	}
	hash, err := st.SetEncodedObject(co)
	if err != nil {
		t.Fatal(err)
	}

	for _, ref := range []*plumbing.Reference{
		plumbing.NewHashReference("refs/heads/main", hash),
		plumbing.NewHashReference("refs/tags/v1.0.0", hash),
		plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main"),
	} {
		if err := st.SetReference(ref); err != nil {
			t.Fatal(err)
		}
	}

	p := GitProber{clientOptions: []client.Option{
		client.WithLoader(transport.NewFilesystemLoader(fs, false)),
	}}
	refs, err := p.List(context.Background(), "file:///")
	if err != nil {
		t.Fatal(err)
	}
	tags := ReleaseTags(refs, "")
	if len(tags) != 1 || tags[0].Version.String() != "v1.0.0" || tags[0].Hash != hash.String() {
		t.Fatalf("tags = %+v, want v1.0.0 at %s", tags, hash)
	}
}

// A listing failure surfaces as an error with no refs — never as an
// empty successful listing, which Resolve would take as a repository
// answering.
func TestGitProberListError(t *testing.T) {
	p := GitProber{clientOptions: []client.Option{
		client.WithLoader(transport.NewFilesystemLoader(memfs.New(), false)),
	}}
	refs, err := p.List(context.Background(), "file:///")
	if err == nil || refs != nil {
		t.Fatalf("refs=%v err=%v, want error and nil refs", refs, err)
	}
}
