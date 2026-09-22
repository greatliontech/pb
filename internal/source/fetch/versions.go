package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// origin's default-branch head, which a subtree absent there has
// none of (REQ-resolve-release-tags, REQ-resolve-synthesized-tags).
// Like the listing it is advisory: what it names is resolved and
// verified as any declared version is.
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
// pseudo-version with no precedent, no tagged release preceding it —
// a version of the module as head holds it, which a subtree absent
// there has none of: the origin has no latest for it, the direct
// analog of a proxy's not-here (REQ-resolve-release-tags).
func (c *Client) directLatest(ctx context.Context, modPath string) (proxy.Unverified, error) {
	repo, at, tags, err := c.directTags(ctx, modPath)
	if err != nil {
		return nil, err
	}
	var v version.Version
	switch {
	case len(tags) > 0:
		v = tags[len(tags)-1].Version
	case !at.rooted:
		return nil, fmt.Errorf("%w: %s has no release and no root at the origin's head", proxy.ErrNotHere, modPath)
	default:
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
	_, _, tags, err := c.directTags(ctx, modPath)
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
// ascending, with the clone they were read from and the module's state
// at its head.
func (c *Client) directTags(ctx context.Context, modPath string) (*direct.Repo, atHead, []origin.Tag, error) {
	o, err := c.origin(ctx, modPath)
	if err != nil {
		return nil, atHead{}, nil, err
	}
	repo, err := c.repo(ctx, o.Repo)
	if err != nil {
		return nil, atHead{}, nil, err
	}
	at, err := c.atHead(ctx, repo, o)
	if err != nil {
		return nil, atHead{}, nil, err
	}
	refs, err := repo.Refs()
	if err != nil {
		return nil, atHead{}, nil, err
	}
	return repo, at, origin.ReleaseTags(refs, at.namespace), nil
}

// atHead is a module's state at the origin's default-branch head — the
// only state the origin can answer for: the tag namespace its versions
// live in, and whether its root exists there at all.
type atHead struct {
	namespace string
	rooted    bool
}

// atHead judges a module at the origin's head: the one decision the
// listing, a release's tag, a pseudo-version's base and the latest all
// take, so that a version named in one namespace is fetched from the
// same and ranked against the same releases (REQ-resolve-release-tags,
// REQ-resolve-pseudo-base). Subtree-prefixed tags for a module declared
// at a subtree, repository-level tags for a root or synthesized module
// (REQ-resolve-synthesized-tags) — synthesized meaning a subtree that
// exists holding no module file. A subtree absent at head is no
// synthesized module — synthesis is the judgment over a subtree that
// exists (REQ-resolve-synthesis) — and keeps its path's own namespace:
// a release tagged there resolves, or fails at the root the tagged
// commit lacks; head itself is no version of it.
func (c *Client) atHead(ctx context.Context, repo *direct.Repo, o origin.Origin) (atHead, error) {
	if o.Subtree == "" {
		return atHead{namespace: "", rooted: true}, nil
	}
	head, err := repo.Head(ctx)
	if err != nil {
		return atHead{}, err
	}
	declared, err := repo.Declared(ctx, head.Hash, o.Subtree)
	switch {
	case errors.Is(err, direct.ErrNoModuleRoot):
		return atHead{namespace: o.Subtree, rooted: false}, nil
	case err != nil:
		return atHead{}, err
	case declared:
		return atHead{namespace: o.Subtree, rooted: true}, nil
	}
	return atHead{namespace: "", rooted: true}, nil
}
