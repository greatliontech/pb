package trust

import (
	"errors"
	"fmt"
	"strings"

	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/glob"
)

// ErrNoDefaultIdentity marks an origin whose forge has no derivable
// default identity: origin consistency cannot be checked offline, so an
// explicit identity rule is the only way to accept its signatures.
var ErrNoDefaultIdentity = errors.New("trust: no derivable default identity for origin")

// forgeIssuers maps an origin host to its CI workflow OIDC issuer — the
// one issuer whose certificates carry workflow-URI SANs under that
// host's repository URLs.
var forgeIssuers = map[string]string{
	"github.com": "https://token.actions.githubusercontent.com",
}

// ExplicitIdentity builds the verification policy for an explicit
// identity rule: the issuer exact, the SAN glob handed to the verifier
// verbatim — gitprov speaks the same greatliontech/glob pattern
// language the trust schema promises, so no translation exists to
// drift.
func ExplicitIdentity(rule IdentityRule) (gitprov.Identity, error) {
	id := gitprov.Identity{Issuer: rule.Issuer, SubjectGlob: rule.SAN}
	if err := id.Validate(); err != nil {
		return gitprov.Identity{}, fmt.Errorf("trust: identity rule: %w", err)
	}
	return id, nil
}

// DefaultIdentity builds the origin-consistency verification policy
// (REQ-prov-origin-consistency) for a module whose origin repository
// URL is repoURL: a URI SAN under the repository's URL — a CI workflow
// identity of that repository — issued by the origin forge's CI issuer.
// Identity kinds with no offline-verifiable binding to a repository (a
// maintainer's personal identity) are never accepted by default; an
// origin on a forge with no known CI issuer has no default at all and
// returns ErrNoDefaultIdentity.
//
// The pattern is glob.Quote(url) + "/*/**": strictly under the URL —
// the bare URL itself is not a designation — with the quoted part
// neutralizing any pattern syntax in the URL.
func DefaultIdentity(repoURL string) (gitprov.Identity, error) {
	host := repoHost(repoURL)
	issuer, ok := forgeIssuers[host]
	if !ok {
		return gitprov.Identity{}, fmt.Errorf("%w host %q", ErrNoDefaultIdentity, host)
	}
	id := gitprov.Identity{
		Issuer:      issuer,
		SubjectGlob: glob.Quote(strings.TrimSuffix(repoURL, "/")) + "/*/**",
	}
	if err := id.Validate(); err != nil {
		return gitprov.Identity{}, fmt.Errorf("trust: default identity: %w", err)
	}
	return id, nil
}

// repoHost extracts the host of a repository URL ("https://host/…").
func repoHost(repoURL string) string {
	rest := repoURL
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}
