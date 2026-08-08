package direct

import (
	"context"

	git "github.com/go-git/go-git/v6"
	"github.com/greatliontech/pb/internal/gittest"
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

// fetch clones the fixture through the in-memory file transport.
func (f *repoFixture) fetch() *Repo {
	fe := Fetcher{clientOptions: f.ClientOptions()}
	repo, err := fe.Fetch(context.Background(), "file:///")
	if err != nil {
		f.T.Fatal(err)
	}
	return repo
}

// open opens the fixture's storage directly, with no clone in between:
// the storage-level failure modes it serves (corrupt objects, broken
// refs, dangling parents) cannot ride through a healthy fetch, and the
// fetch path itself is pinned by the clone-based tests.
func (f *repoFixture) open() *Repo {
	r, err := git.Open(f.St, nil)
	if err != nil {
		f.T.Fatal(err)
	}
	return &Repo{r: r}
}
