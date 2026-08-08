package origin

import (
	"context"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing/client"
)

// GitProber lists references with go-git's protocol-level equivalent of
// `git ls-remote`. The zero value is ready to use.
type GitProber struct {
	// clientOptions extends the transport client per listing. The fixture
	// test roots the file-transport loader inside its temp directory —
	// go-git's default loader is rooted at the filesystem root, which
	// would make the whole filesystem an observed input of the test.
	clientOptions []client.Option
}

// List advertises the repository's references over its native transport.
func (p GitProber) List(ctx context.Context, repoURL string) ([]Ref, error) {
	rem := git.NewRemote(nil, &config.RemoteConfig{Name: "origin", URLs: []string{repoURL}})
	refs, err := rem.ListContext(ctx, &git.ListOptions{ClientOptions: p.clientOptions})
	if err != nil {
		return nil, err
	}
	out := make([]Ref, 0, len(refs))
	for _, r := range refs {
		out = append(out, Ref{Name: r.Name().String(), Hash: r.Hash().String()})
	}
	return out, nil
}
