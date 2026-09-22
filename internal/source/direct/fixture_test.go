package direct

import (
	"context"
	"slices"
	"strings"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/storer"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/plumbing/transport/file"
	"github.com/go-git/go-git/v6/storage"
	"github.com/greatliontech/pb/internal/testing/gittest"
)

type failer = gittest.Failer

// repoFixture is the shared in-memory repository builder plus the two
// ways this package's tests reach it.
type repoFixture struct {
	*gittest.Repo
}

func newFixture(t failer) *repoFixture {
	return &repoFixture{gittest.New(t)}
}

// fetch opens the fixture through the in-memory file transport.
func (f *repoFixture) fetch() *Repo {
	fe := Fetcher{ClientOptions: f.ClientOptions()}
	repo, err := fe.Fetch(context.Background(), "file:///")
	if err != nil {
		f.T.Fatal(err)
	}
	return repo
}

// open opens the fixture's storage directly as both repositories,
// with no fetch in between, over a listing built from the storage's
// refs as the wire would advertise them: the storage-level failure
// modes it serves (corrupt objects, dangling parents) cannot ride
// through a healthy fetch, and the fetch path itself is pinned by the
// transport-based tests.
func (f *repoFixture) open() *Repo {
	r, err := git.Open(f.St, nil)
	if err != nil {
		f.T.Fatal(err)
	}
	return newRepo(r, f.St, r, f.listing(r))
}

// listing advertises the storage's refs as the wire does: HEAD kept
// symbolic, every other symbolic ref resolved to its hash, and an
// annotated tag followed by its peeled `^{}` entry — none where the
// tag object cannot be read, as a server would advertise nothing it
// cannot peel.
func (f *repoFixture) listing(r *git.Repository) []*plumbing.Reference {
	iter, err := r.References()
	if err != nil {
		f.T.Fatal(err)
	}
	var listed []*plumbing.Reference
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name() != plumbing.HEAD && ref.Type() == plumbing.SymbolicReference {
			resolved, err := storer.ResolveReference(f.St, ref.Name())
			if err != nil {
				return err
			}
			ref = plumbing.NewHashReference(ref.Name(), resolved.Hash())
		}
		listed = append(listed, ref)
		if ref.Type() != plumbing.HashReference || !strings.HasPrefix(ref.Name().String(), "refs/tags/") {
			return nil
		}
		if _, err := r.TagObject(ref.Hash()); err != nil {
			return nil
		}
		if peeled, _, ok := peel(r, ref.Hash()); ok && peeled != ref.Hash() {
			listed = append(listed, plumbing.NewHashReference(ref.Name()+"^{}", peeled))
		}
		return nil
	})
	if err != nil {
		f.T.Fatal(err)
	}
	slices.SortFunc(listed, func(a, b *plumbing.Reference) int { return strings.Compare(a.Name().String(), b.Name().String()) })
	return listed
}

// recorder keeps every fetch request the transport carried: the need
// each fetch expressed — its wants, depth and filter — independent of
// what the in-process server makes of it.
type recorder struct {
	reqs []*transport.FetchRequest
}

// recording returns client options that reach the fixture through the
// file transport with every fetch request recorded.
func (f *repoFixture) recording() ([]client.Option, *recorder) {
	rec := &recorder{}
	inner := file.NewTransport(file.Options{Loader: transport.NewFilesystemLoader(f.FS, false)})
	return []client.Option{client.WithTransport("file", recordingTransport{inner: inner, rec: rec})}, rec
}

type recordingTransport struct {
	inner transport.Transport
	rec   *recorder
}

func (t recordingTransport) Handshake(ctx context.Context, req *transport.Request) (transport.Session, error) {
	s, err := t.inner.Handshake(ctx, req)
	if err != nil {
		return nil, err
	}
	return recordingSession{Session: s, rec: t.rec}, nil
}

type recordingSession struct {
	transport.Session
	rec *recorder
}

func (s recordingSession) Fetch(ctx context.Context, st storage.Storer, req *transport.FetchRequest) error {
	s.rec.reqs = append(s.rec.reqs, req)
	return s.Session.Fetch(ctx, st, req)
}
