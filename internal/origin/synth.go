package origin

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/greatliontech/pb/internal/version"
)

// ErrPseudoMismatch is wrapped when a pseudo-version does not bind the
// commit it is checked against (REQ-resolve-pseudo-commit).
var ErrPseudoMismatch = errors.New("pseudo-version does not match commit")

// Commit is a commit's resolution-relevant identity: the full lowercase
// hex hash — 40 digits (SHA-1) or 64 (SHA-256), the two git object
// formats — and the commit time. VerifyPseudo enforces the shape.
type Commit struct {
	Hash string
	Time time.Time
}

// fullCommitHash reports whether s is a full lowercase-hex commit hash
// in either git object format.
func fullCommitHash(s string) bool {
	return (len(s) == 40 || len(s) == 64) && version.IsLowerHex(s)
}

// SynthesizedVersions maps advertised refs to a synthesized module's
// releases (REQ-resolve-synthesized-tags): the repository-level tags when
// any exist, else the pseudo-version of head — the origin's
// default-branch head commit, resolved by the caller — whose Tag names
// the commit it resolves to.
func SynthesizedVersions(refs []Ref, head Commit) ([]Tag, error) {
	if tags := ReleaseTags(refs, ""); len(tags) > 0 {
		return tags, nil
	}
	// The emitted Tag's Hash flows where full advertised hashes flow; an
	// abbreviation would be unresolvable downstream, so the shape is
	// enforced here, not just at verification.
	if !fullCommitHash(head.Hash) {
		return nil, fmt.Errorf("head commit hash %q is not a full lowercase-hex commit hash", head.Hash)
	}
	v, err := version.PseudoVersion(nil, head.Time, head.Hash)
	if err != nil {
		return nil, fmt.Errorf("pseudo-version for commit %s: %w", head.Hash, err)
	}
	return []Tag{{Version: v, Hash: head.Hash}}, nil
}

// VerifyPseudo checks a pseudo-version against the commit it names
// (REQ-resolve-pseudo-commit): the version's embedded hash prefix and UTC
// commit time must both match the commit. The other half of the
// requirement — a commit absent from the origin fails resolution — lives
// with the caller that looks the hash up; nothing absent can reach this
// check with a commit in hand.
func VerifyPseudo(v version.Version, c Commit) error {
	ts, hash, ok := v.Pseudo()
	if !ok {
		return fmt.Errorf("%w: %s is not a pseudo-version", ErrPseudoMismatch, v)
	}
	// A full-hash commit is the caller's proof of an origin lookup: the
	// version's own 12-digit prefix (or any abbreviation) can never
	// stand in for the commit it claims to bind.
	if !fullCommitHash(c.Hash) {
		return fmt.Errorf("%w: %q is not a full lowercase-hex commit hash", ErrPseudoMismatch, c.Hash)
	}
	if !strings.HasPrefix(c.Hash, hash) {
		return fmt.Errorf("%w: %s names commit %s, checked against %s", ErrPseudoMismatch, v, hash, c.Hash)
	}
	if got := c.Time.UTC().Format(version.PseudoTimeLayout); got != ts {
		return fmt.Errorf("%w: %s embeds commit time %s, commit has %s", ErrPseudoMismatch, v, ts, got)
	}
	return nil
}
