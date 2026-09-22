package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/source/direct"
	"github.com/greatliontech/pb/internal/source/origin"
	"github.com/greatliontech/pb/internal/source/proxy"
)

// Versions lists a module's discovered tagged releases, ascending — the
// @v/list surface (REQ-proxy-endpoints) through the source list, the
// origin's release tags through the direct construction. The listing is
// advisory (REQ-proxy-list-advisory): callers use it to discover newer
// versions, never for the correctness of resolving declared ones —
// which is why nothing here verifies, records, or pins.
func (c *Client) Versions(ctx context.Context, modPath string) ([]version.Version, error) {
	b, err := c.get(ctx, modPath, c.directList, proxy.ListURL)
	if err != nil {
		return nil, err
	}
	return proxy.ParseList(b)
}

// get walks the module's sources (REQ-proxy-fallthrough): the direct
// construction through direct, a proxy through the URL its base and
// the module path make.
func (c *Client) get(ctx context.Context, modPath string, fromOrigin func(context.Context, string) (proxy.Unverified, error), url func(base, modPath string) string) (proxy.Unverified, error) {
	return proxy.Fallthrough(c.Sources.SourcesFor(modPath), func(s proxy.Source) (proxy.Unverified, error) {
		if s.Direct {
			return fromOrigin(ctx, modPath)
		}
		return proxy.Get(ctx, c.HTTP, url(s.URL, modPath), fetchLimit)
	})
}

// Latest discovers a module's latest version: a proxy's @latest — the
// highest tagged release, or a recent commit's pseudo-version where
// no tag exists (REQ-proxy-endpoints) — through the source list, and
// through the direct construction the highest release tag in the
// module's namespace or, none existing, the pseudo-version of the
// origin's default-branch head (REQ-resolve-synthesized-tags). Like
// the listing it is advisory: what it names is resolved and verified
// as any declared version is.
func (c *Client) Latest(ctx context.Context, modPath string) (version.Version, error) {
	b, err := c.get(ctx, modPath, c.directLatest, proxy.LatestURL)
	if err != nil {
		return version.Version{}, err
	}
	info, err := proxy.ParseInfo(b)
	if err != nil {
		return version.Version{}, err
	}
	return info.Version, nil
}

// directLatest renders the origin's answer in @latest's wire shape:
// the highest release tag in the module's namespace, else the head's
// pseudo-version with no precedent, no tagged release preceding it.
func (c *Client) directLatest(ctx context.Context, modPath string) (proxy.Unverified, error) {
	repo, tags, err := c.directTags(ctx, modPath)
	if err != nil {
		return nil, err
	}
	var v version.Version
	if len(tags) > 0 {
		v = tags[len(tags)-1].Version
	} else {
		head, err := repo.Head(ctx)
		if err != nil {
			return nil, err
		}
		if v, err = version.PseudoVersion(nil, head.Time, head.Hash); err != nil {
			return nil, err
		}
	}
	body, err := json.Marshal(map[string]string{"version": v.String()})
	if err != nil {
		return nil, err
	}
	return proxy.Unverified(body), nil
}

// directList renders the origin's release listing in the @v/list wire
// shape, so both legs flow through one parser.
func (c *Client) directList(ctx context.Context, modPath string) (proxy.Unverified, error) {
	_, tags, err := c.directTags(ctx, modPath)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, tag := range tags {
		b.WriteString(tag.Version.String())
		b.WriteByte('\n')
	}
	return proxy.Unverified(b.String()), nil
}

// directTags lists the origin's release tags in the module's namespace,
// ascending, with the clone they were read from. The namespace follows
// REQ-resolve-release-tags: subtree-prefixed tags for a module declared
// at a subtree, repository-level tags for a root or synthesized module
// (REQ-resolve-synthesized-tags's listing half) — synthesized meaning
// no module file at the subtree on the origin's default-branch head,
// the only state the origin can answer for.
func (c *Client) directTags(ctx context.Context, modPath string) (*direct.Repo, []origin.Tag, error) {
	o, err := c.origin(ctx, modPath)
	if err != nil {
		return nil, nil, err
	}
	repo, err := c.repo(ctx, o.Repo)
	if err != nil {
		return nil, nil, err
	}
	namespace := o.Subtree
	if namespace != "" {
		head, err := repo.Head(ctx)
		if err != nil {
			return nil, nil, err
		}
		_, declared, err := repo.ModuleFileBytes(ctx, head.Hash, o.Subtree)
		switch {
		case errors.Is(err, direct.ErrNoModuleRoot):
			// The subtree does not exist at head at all: certainly not a
			// declared module there — declared is already false.
		case err != nil:
			return nil, nil, err
		}
		if !declared {
			namespace = ""
		}
	}
	refs, err := repo.Refs()
	if err != nil {
		return nil, nil, err
	}
	return repo, origin.ReleaseTags(refs, namespace), nil
}
