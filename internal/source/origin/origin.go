// Package origin maps module paths to their origins: the repository
// backing a path and the subtree within it (module-resolution.md §Path
// resolution). The split is found three ways, in precedence order: an
// explicit `.git` segment closes the repository prefix
// (REQ-resolve-vcs-suffix); a vanity redirect declared at the module path
// names the repository (REQ-resolve-vanity); otherwise path prefixes are
// probed in increasing length and the first that answers a reference
// listing wins (REQ-resolve-probing). A probe result is never authority —
// every fetched artifact still verifies per its own contract.
//
// Transport is go-git (Remote.List), not the git binary: pb stays
// self-contained with no runtime dependency on an installed git, the
// reference-listing operation is protocol-level (the spec's `git
// ls-remote` names the operation, not the executable), and the library
// line is pinned in go.mod where a fork can replace it wholesale if a
// gap ever demands it. All network effects sit behind the Prober and
// http.Client seams; everything above them is pure.
package origin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/version"
)

// ErrNoOrigin is wrapped when no discovery mechanism yields a repository
// for a module path.
var ErrNoOrigin = errors.New("no origin found")

// Origin is a resolved repository split: the HTTPS repository URL and the
// module's subtree within it ("" for a module rooted at the repository
// root).
type Origin struct {
	Repo    string
	Subtree string
}

// SplitVCS resolves a path carrying a `.git` segment
// (REQ-resolve-vcs-suffix): the first such segment is the final segment
// of the repository prefix, the remainder is the subtree.
func SplitVCS(path string) (Origin, bool) {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if strings.HasSuffix(s, ".git") && s != ".git" {
			return Origin{
				Repo:    "https://" + strings.Join(segs[:i+1], "/"),
				Subtree: strings.Join(segs[i+1:], "/"),
			}, true
		}
	}
	return Origin{}, false
}

// prefixes lists the path's candidate repository prefixes in increasing
// length: host plus one segment, then each additional segment. The bare
// host is never a candidate (REQ-resolve-probing) — a host answering
// reference listings at its root would otherwise silently capture every
// module on that host; a repository genuinely rooted there declares
// itself via a `.git` segment or a vanity redirect. Callers pass
// validated paths (module.ValidatePath), so at least one segment
// follows the host.
func prefixes(path string) []string {
	segs := strings.Split(path, "/")
	out := make([]string, 0, len(segs)-1)
	for i := 2; i <= len(segs); i++ {
		out = append(out, strings.Join(segs[:i], "/"))
	}
	return out
}

// subtreeOf returns the path's remainder below a segment-exact prefix.
func subtreeOf(path, prefix string) (string, bool) {
	if path == prefix {
		return "", true
	}
	rest, ok := strings.CutPrefix(path, prefix+"/")
	if !ok {
		return "", false
	}
	return rest, true
}

// Ref is one advertised reference: the full ref name and its object hash.
type Ref struct {
	Name string
	Hash string
}

// Tag is one release tag mapped to its version: Hash is the advertised
// ref target (the tag object for annotated tags), Peeled the dereferenced
// commit when the listing advertised one.
type Tag struct {
	Version version.Version
	Hash    string
	Peeled  string
}

// ReleaseTags maps advertised refs to a module's release tags
// (REQ-resolve-release-tags): `refs/tags/<v>` for a root module,
// `refs/tags/<subtree>/<v>` for a subtree module — the separator makes
// prefix matching segment-exact. A synthesized module passes subtree ""
// and takes the repository-level tags (REQ-resolve-synthesized-tags's
// listing half). Refs that do not parse as canonical versions are not
// the module's releases and are skipped — as is a pseudo-version-shaped
// tag, which is never a release tag (REQ-resolve-pseudo-base): version
// resolution binds a pseudo-version to the commit its hash embeds, not
// to a tag, so listing one as a release would advertise an unresolvable
// version. The result is sorted ascending, so listing order never leaks
// (REQ-resolve-determinism).
func ReleaseTags(refs []Ref, subtree string) []Tag {
	prefix := "refs/tags/"
	if subtree != "" {
		prefix += subtree + "/"
	}
	byVersion := make(map[string]*Tag)
	var order []version.Version
	for _, r := range refs {
		name, peeled := strings.CutSuffix(r.Name, "^{}")
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok || strings.Contains(rest, "/") {
			continue
		}
		v, err := version.Parse(rest)
		if err != nil || v.IsPseudo() {
			continue
		}
		t, seen := byVersion[v.String()]
		if !seen {
			t = &Tag{Version: v}
			byVersion[v.String()] = t
			order = append(order, v)
		}
		if peeled {
			t.Peeled = r.Hash
		} else {
			t.Hash = r.Hash
		}
	}
	slices.SortFunc(order, version.Compare)
	out := make([]Tag, 0, len(order))
	for _, v := range order {
		out = append(out, *byVersion[v.String()])
	}
	return out
}

// Deps carries the two effectful seams: reference listing and HTTPS
// vanity discovery. Everything else in the package is pure. Both seams
// are required — a nil seam panics at its first use rather than
// carrying an error path for static miswiring of an internal package.
type Deps struct {
	Prober Prober
	Client *http.Client
}

// Prober lists a repository's advertised references — the spec's `git
// ls-remote` operation (REQ-resolve-probing). Success, even with zero
// refs, means the prefix answers as a repository.
type Prober interface {
	List(ctx context.Context, repoURL string) ([]Ref, error)
}

// Resolve maps a module path to its origin, in the spec's precedence
// order. The prober is consulted only when neither the `.git` rule nor a
// vanity redirect decides (REQ-resolve-vanity's precedence clause).
func Resolve(ctx context.Context, deps Deps, path string) (Origin, error) {
	if err := module.ValidatePath(path); err != nil {
		return Origin{}, err
	}
	if o, ok := SplitVCS(path); ok {
		return o, nil
	}
	if red, ok, err := discoverVanity(ctx, deps.Client, path); err != nil {
		return Origin{}, err
	} else if ok {
		sub, match := subtreeOf(path, red.Prefix)
		if !match {
			// Unreachable in-spec: discoverVanity only returns
			// segment-exact matching prefixes; fail closed regardless.
			return Origin{}, fmt.Errorf("vanity prefix %q does not prefix %q", red.Prefix, path)
		}
		return Origin{Repo: red.Repo, Subtree: sub}, nil
	}
	var attempts []string
	for _, p := range prefixes(path) {
		repo := "https://" + p
		if _, err := deps.Prober.List(ctx, repo); err != nil {
			// A canceled or expired context is not evidence of absence:
			// reporting ErrNoOrigin here would misclassify a repository
			// that exists but was never reached.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Origin{}, fmt.Errorf("probing %s: %w", repo, ctxErr)
			}
			attempts = append(attempts, fmt.Sprintf("%s: %v", repo, err))
			continue
		}
		sub, _ := subtreeOf(path, p)
		return Origin{Repo: repo, Subtree: sub}, nil
	}
	return Origin{}, fmt.Errorf("%w for %s: no vanity redirect and no prefix answered a reference listing (%s)",
		ErrNoOrigin, path, strings.Join(attempts, "; "))
}
