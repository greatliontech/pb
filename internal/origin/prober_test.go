package origin

import (
	"context"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/greatliontech/pb/internal/gittest"
)

// The go-git wiring lists a real repository's references, hermetically:
// the fixture is a plumbing-built bare repository in an in-memory
// filesystem (see internal/gittest), listed over file:// through a
// loader rooted at that same filesystem.
func TestGitProberListsFixtureRepo(t *testing.T) {
	g := gittest.New(t)
	hash := g.Commit("init", time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC))
	g.Branch("main", hash)
	g.Tag("v1.0.0", hash)
	g.Head("main")

	p := GitProber{clientOptions: g.ClientOptions()}
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
// answering. The loader is rooted at an empty filesystem holding no
// repository at all, so no fixture builder applies.
func TestGitProberListError(t *testing.T) {
	p := GitProber{clientOptions: []client.Option{
		client.WithLoader(transport.NewFilesystemLoader(memfs.New(), false)),
	}}
	refs, err := p.List(context.Background(), "file:///")
	if err == nil || refs != nil {
		t.Fatalf("refs=%v err=%v, want error and nil refs", refs, err)
	}
}
