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
	"net/url"
	"slices"
	"strings"

	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/source"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ErrNoOrigin is wrapped when no discovery mechanism yields a repository
// for a module path.
var ErrNoOrigin = errors.New("no origin found")

// Origin is a resolved repository split: the HTTPS repository URL,
// the repository's identity — what the module's provenance is held
// to (REQ-prov-origin-consistency) and the name every record keeps —
// whether the ssh setting routes the module (REQ-resolve-ssh: the
// setting routes transport alone, so the identity is the same URL
// either way), and the module's subtree within the repository (""
// for a module rooted at the repository root). The URL the
// repository is reached at is Remote.
type Origin struct {
	Repo    string
	SSH     bool
	Subtree string
}

// Remote is the URL the repository is reached at: the HTTPS
// repository URL, or its SSH spelling where the ssh setting routes
// the module.
func (o Origin) Remote() (string, error) {
	if !o.SSH {
		return o.Repo, nil
	}
	return SSHRepo(o.Repo)
}

// String names the origin as a failure reports it: the remote it was
// reached at, the one the user routed.
func (o Origin) String() string {
	remote, err := o.Remote()
	if err != nil {
		return o.Repo
	}
	return remote
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

// Deps carries the two effectful seams — reference listing and HTTPS
// vanity discovery — and the ssh setting's patterns, the modules
// reached over SSH (REQ-resolve-ssh). Everything else in the package
// is pure. Both seams are required — a nil seam panics at its first
// use rather than carrying an error path for static miswiring of an
// internal package.
type Deps struct {
	Prober Prober
	Client *http.Client
	SSH    []string
}

// SSHRepo spells an HTTPS repository URL as the SSH one the same host
// serves (REQ-resolve-ssh): ssh://git@<host name>/<path>, the HTTPS
// port dropped — the SSH port is the OpenSSH configuration's for the
// host, else 22 — and the path as escaped.
func SSHRepo(httpsRepo string) (string, error) {
	u, err := url.Parse(httpsRepo)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", fmt.Errorf("spelling %q over SSH: not an HTTPS repository URL", httpsRepo)
	}
	host := u.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return "ssh://git@" + host + u.EscapedPath(), nil
}

// probeFailed reports whether a prefix's listing failed for a reason
// no other prefix can answer for — the run's own: no agent to
// authenticate through, a host key no known-hosts file holds — so
// probing on would misreport a module the run cannot reach as one
// that does not exist. A host refusing authentication is not among
// them: a forge answers an anonymous listing of a repository that
// does not exist with a refusal, so that its private repositories
// stay unknown, and a deeper prefix may well answer.
func probeFailed(err error) bool {
	var keyErr *knownhosts.KeyError
	return errors.Is(err, source.ErrNoAgent) || errors.As(err, &keyErr)
}

// refused reports whether a prefix's listing was an HTTPS
// authentication refusal: a prefix that does not answer, named as
// such when no prefix answers, since the refusal may be the reason.
// An SSH refusal has no sentinel of its own — the transport passes
// the handshake's error through — and is carried in the attempt's
// text alone.
func refused(err error) bool {
	return errors.Is(err, transport.ErrAuthenticationRequired) || errors.Is(err, transport.ErrAuthorizationFailed)
}

// Prober lists a repository's advertised references — the spec's `git
// ls-remote` operation (REQ-resolve-probing). Success, even with zero
// refs, means the prefix answers as a repository.
type Prober interface {
	List(ctx context.Context, repoURL string) ([]Ref, error)
}

// Resolve maps a module path to its origin, in the spec's precedence
// order. The prober is consulted only when neither the `.git` rule nor a
// vanity redirect decides (REQ-resolve-vanity's precedence clause). A
// module the ssh setting matches is reached over SSH whichever arm
// decides: the HTTPS repository each arm names — the `.git` prefix,
// the vanity redirect's declaration, a probed prefix — stays the
// origin's identity, its SSH spelling the remote, and probing lists
// over that remote (REQ-resolve-ssh).
func Resolve(ctx context.Context, deps Deps, path string) (Origin, error) {
	if err := module.ValidatePath(path); err != nil {
		return Origin{}, err
	}
	ssh := source.MatchAny(deps.SSH, path)
	if o, ok := SplitVCS(path); ok {
		o.SSH = ssh
		if _, err := o.Remote(); err != nil {
			return Origin{}, err
		}
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
		o := Origin{Repo: red.Repo, SSH: ssh, Subtree: sub}
		if _, err := o.Remote(); err != nil {
			return Origin{}, err
		}
		return o, nil
	}
	var attempts []string
	anyRefused := false
	for _, p := range prefixes(path) {
		o := Origin{Repo: "https://" + p, SSH: ssh}
		repo, err := o.Remote()
		if err != nil {
			return Origin{}, err
		}
		if _, err := deps.Prober.List(ctx, repo); err != nil {
			// A canceled or expired context is not evidence of absence:
			// reporting ErrNoOrigin here would misclassify a repository
			// that exists but was never reached. Nor is the run's own
			// failure to authenticate or to trust the host.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Origin{}, fmt.Errorf("probing %s: %w", repo, ctxErr)
			}
			if probeFailed(err) {
				return Origin{}, fmt.Errorf("probing %s: %w", repo, err)
			}
			anyRefused = anyRefused || refused(err)
			attempts = append(attempts, fmt.Sprintf("%s: %v", repo, err))
			continue
		}
		o.Subtree, _ = subtreeOf(path, p)
		return o, nil
	}
	if anyRefused {
		return Origin{}, fmt.Errorf("%w for %s: no vanity redirect and no prefix answered a reference listing, one refusing authentication — a private repository needs a credential file entry or an ssh route (%s)",
			ErrNoOrigin, path, strings.Join(attempts, "; "))
	}
	return Origin{}, fmt.Errorf("%w for %s: no vanity redirect and no prefix answered a reference listing (%s)",
		ErrNoOrigin, path, strings.Join(attempts, "; "))
}
