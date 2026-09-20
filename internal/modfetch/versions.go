package modfetch

import (
	"context"
	"errors"
	"strings"

	"github.com/greatliontech/pb/internal/direct"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/origin"
	"github.com/greatliontech/pb/internal/proxy"
)

// Versions lists a module's discovered tagged releases, ascending — the
// @v/list surface (REQ-proxy-endpoints) through the source list, the
// origin's release tags through the direct construction. The listing is
// advisory (REQ-proxy-list-advisory): callers use it to discover newer
// versions, never for the correctness of resolving declared ones —
// which is why nothing here verifies, records, or pins.
func (c *Client) Versions(ctx context.Context, modPath string) ([]version.Version, error) {
	sources := c.Sources.SourcesFor(modPath)
	b, err := proxy.Fallthrough(sources, func(s proxy.Source) (proxy.Unverified, error) {
		if s.Direct {
			return c.directList(ctx, modPath)
		}
		return proxy.Get(ctx, c.HTTP, proxy.ListURL(s.URL, modPath), fetchLimit)
	})
	if err != nil {
		return nil, err
	}
	return proxy.ParseList(b)
}

// directList renders the origin's release listing in the @v/list wire
// shape, so both legs flow through one parser. The tag namespace
// follows REQ-resolve-release-tags: subtree-prefixed tags for a module
// declared at a subtree, repository-level tags for a root or
// synthesized module (REQ-resolve-synthesized-tags's listing half) —
// synthesized meaning no module file at the subtree on the origin's
// default-branch head, the only state the origin can answer for.
func (c *Client) directList(ctx context.Context, modPath string) (proxy.Unverified, error) {
	o, err := c.origin(ctx, modPath)
	if err != nil {
		return nil, err
	}
	repo, err := c.repo(ctx, o.Repo)
	if err != nil {
		return nil, err
	}
	namespace := o.Subtree
	if namespace != "" {
		head, err := repo.Head()
		if err != nil {
			return nil, err
		}
		_, declared, err := repo.ModuleFileBytes(head.Hash, o.Subtree)
		switch {
		case errors.Is(err, direct.ErrNoModuleRoot):
			// The subtree does not exist at head at all: certainly not a
			// declared module there — declared is already false.
		case err != nil:
			return nil, err
		}
		if !declared {
			namespace = ""
		}
	}
	refs, err := repo.Refs()
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, tag := range origin.ReleaseTags(refs, namespace) {
		b.WriteString(tag.Version.String())
		b.WriteByte('\n')
	}
	return proxy.Unverified(b.String()), nil
}
