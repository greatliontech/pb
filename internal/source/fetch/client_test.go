package fetch

import (
	"context"
	"testing"

	"github.com/greatliontech/pb/internal/source/origin"
)

// Release gives back what the client holds of every origin: the
// repositories opened are closed and forgotten, the origins resolved
// forgotten, so the next resolution resolves and opens anew — a
// reader outliving a verb's run holds nothing between its judgements
// (lsp.md REQ-lsp-session, module-proxy.md REQ-proxy-direct-fetch).
func TestReleaseForgetsTheOrigins(t *testing.T) {
	fx := newFixture(t)
	commit := fx.CommitFor(declaredFiles(), gitWhen)
	fx.Repo.Ref("refs/tags/v1.0.0", commit)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")
	resolved := 0
	fx.ResolveOverride = func(ctx context.Context, modPath string) (origin.Origin, error) {
		resolved++
		return origin.Origin{Repo: "file:///", Subtree: fx.Subtrees[modPath]}, nil
	}
	c := fx.Client("direct")
	if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if resolved != 1 || len(c.repos) != 1 || len(c.origins) != 1 {
		t.Fatalf("after a download: resolved %d, repos %d, origins %d", resolved, len(c.repos), len(c.origins))
	}
	c.Release()
	if len(c.repos) != 0 || len(c.origins) != 0 {
		t.Fatalf("after the release: repos %d, origins %d", len(c.repos), len(c.origins))
	}
	// The cache serves the pinned artifacts; a listing is asked for
	// again only by what needs the origin, which resolves it again.
	if _, err := c.Versions(ctx, "example.com/m"); err != nil {
		t.Fatalf("Versions after the release: %v", err)
	}
	if resolved != 2 || len(c.repos) != 1 {
		t.Fatalf("after the release's next listing: resolved %d, repos %d", resolved, len(c.repos))
	}
}
