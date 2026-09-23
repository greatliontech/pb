package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/pb/internal/module/archive"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/provenance"
)

// verifyEvidence walks an envelope's evidence objects against the
// subject and the policy's two arms — an identity, for a sigstore
// signature, and pinned keys, for an OpenPGP or SSH one — with one
// classification for every caller (REQ-prov-signed-tag,
// REQ-prov-tag-binding, REQ-prov-pinned-key-eval): an object a
// verifier cannot judge — a sigstore signature with no embedded
// transparency proof or no identity and trusted root to hold it to —
// is absent, a valid signature by a signer the identity does not
// accept, or by no pinned key, a non-acceptance; both are skipped. Any
// other failure is a rejection and aborts: tampering, an unsigned or
// malformed tag, and a signature a pinned key made in a form the
// verifier refuses — a weak digest, text mode — which the origin
// published as evidence under a rule that cannot take it. An object
// that verifies yields its record; the first one accept admits wins
// (deterministically — envelope order), and objects whose record
// accept declines are skipped like unaccepted signers — the caller is
// looking for a specific record, not indicting bystander evidence. A
// nil identity is no identity arm; no keys no key arm.
//
// Returns the accepted record, whether one was accepted, and why each
// skipped object was, in envelope order.
func (c *Client) verifyEvidence(ctx context.Context, subtree string, v version.Version, zip []byte, evidence []provenance.Evidence, id *gitprov.Identity, keys []gitprov.PinnedKey, accept func(lockfile.Provenance) bool) (lockfile.Provenance, bool, []string, error) {
	// The tag that names the version is the subtree's own where the
	// module is declared at the tagged commit and the repository's
	// where it is synthesized there (REQ-resolve-release-tags,
	// REQ-prov-tag-binding) — and the archive, the subtree at that
	// commit, shows which: a module file at its root or none.
	namespace := ""
	if subtree != "" {
		// The zip was verified before this: its error arm is
		// unreachable here.
		_, declared, err := archive.ZipModuleFile(bytes.NewReader(zip), int64(len(zip)))
		if err != nil {
			return lockfile.Provenance{}, false, nil, err
		}
		if declared {
			namespace = subtree
		}
	}
	trees := map[archive.ObjectFormat][]byte{}
	var skipped []string
	for i, ev := range evidence {
		tree, ok := trees[ev.Format]
		if !ok {
			var err error
			tree, err = archive.ZipTreeHash(ev.Format, bytes.NewReader(zip), int64(len(zip)))
			if err != nil {
				return lockfile.Provenance{}, false, skipped, err
			}
			trees[ev.Format] = tree
		}
		sub := provenance.Subject{Version: v, Namespace: namespace, Subtree: subtree}
		rec, err := c.verifyOne(ctx, ev, sub, tree, id, keys)
		switch {
		case err == nil:
			if !accept(rec) {
				skipped = append(skipped, fmt.Sprintf("evidence[%d] verifies but renders another record", i))
				continue
			}
			return rec, true, skipped, nil
		case errors.Is(err, errEvidenceSkipped):
			skipped = append(skipped, fmt.Sprintf("evidence[%d] %v", i, skipReason(err)))
		default:
			return lockfile.Provenance{}, false, skipped, fmt.Errorf("fetch: evidence rejected: %w", err)
		}
	}
	return lockfile.Provenance{}, false, skipped, nil
}

// errEvidenceSkipped marks an evidence object that is absent or a
// non-acceptance: skipped, never tampering.
var errEvidenceSkipped = errors.New("evidence skipped")

// skippedEvidence is errEvidenceSkipped carrying its reason.
type skippedEvidence struct{ reason error }

func (e skippedEvidence) Error() string        { return errEvidenceSkipped.Error() + ": " + e.reason.Error() }
func (e skippedEvidence) Is(target error) bool { return target == errEvidenceSkipped }

func skip(reason error) error { return skippedEvidence{reason: reason} }

// skipReason is the reason a skipped object was, as the caller
// reports it.
func skipReason(err error) error {
	var e skippedEvidence
	if errors.As(err, &e) {
		return e.reason
	}
	return err
}

// skippable is the one classification of a verifier's failure across
// both arms: a sigstore signature with no transparency proof is absent,
// a signer the identity does not accept or the pinned keys do not
// vouch for is a non-acceptance; everything else is a rejection.
func skippable(err error) bool {
	return errors.Is(err, provenance.ErrNoTransparency) || errors.Is(err, gitprov.ErrIdentityMismatch) || errors.Is(err, gitprov.ErrUnpinnedKey)
}

// verifyOne verifies one evidence object under the arm its signature's
// kind takes, returning its record, errEvidenceSkipped for an absent
// or unaccepted object, or the rejection.
func (c *Client) verifyOne(ctx context.Context, ev provenance.Evidence, sub provenance.Subject, tree []byte, id *gitprov.Identity, keys []gitprov.PinnedKey) (lockfile.Provenance, error) {
	kind, err := provenance.Kind(ev)
	if err != nil {
		return lockfile.Provenance{}, err
	}
	if kind == gitprov.Sigstore {
		if id == nil || c.TrustedRoot == nil {
			// No identity arm, or nothing to verify a chain against:
			// unverifiable here, absent.
			return lockfile.Provenance{}, skip(errors.New("a sigstore signature with no identity or trusted root to hold it to"))
		}
		vi, err := provenance.Verify(ctx, ev, sub, tree, *id, c.TrustedRoot)
		if err != nil {
			if skippable(err) {
				return lockfile.Provenance{}, skip(err)
			}
			return lockfile.Provenance{}, err
		}
		return provenance.Record(ev, vi)
	}
	// The pinned arm takes the kind; with no keys pinned, none of the
	// kind vouches, and the verifier says so.
	vk, err := provenance.VerifyPinned(ev, sub, tree, keys)
	if err != nil {
		if skippable(err) {
			return lockfile.Provenance{}, skip(err)
		}
		return lockfile.Provenance{}, err
	}
	return provenance.RecordPinned(ev, vk)
}
