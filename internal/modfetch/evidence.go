package modfetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/pb/internal/archive"
	"github.com/greatliontech/pb/internal/lockfile"
	"github.com/greatliontech/pb/internal/provenance"
	"github.com/greatliontech/pb/internal/version"
)

// verifyEvidence walks an envelope's evidence objects against the
// subject and identity policy with one classification for every caller
// (REQ-prov-signed-tag, REQ-prov-tag-binding): an object with no
// embedded transparency proof is absent, a valid signature by a signer
// the identity does not accept is a non-acceptance, and both are
// skipped; any other verification or binding failure is tampering and
// aborts. An object that verifies yields its record; the first one
// accept admits wins (deterministically — envelope order), and objects
// whose record accept declines are skipped like unaccepted signers —
// the caller is looking for a specific record, not indicting bystander
// evidence.
//
// Returns the accepted record, whether one was accepted, and how many
// objects were skipped.
func (c *Client) verifyEvidence(ctx context.Context, subtree string, v version.Version, zip []byte, evidence []provenance.Evidence, id gitprov.Identity, accept func(lockfile.Provenance) bool) (lockfile.Provenance, bool, int, error) {
	trees := map[archive.ObjectFormat][]byte{}
	skipped := 0
	for _, ev := range evidence {
		tree, ok := trees[ev.Format]
		if !ok {
			var err error
			tree, err = archive.ZipTreeHash(ev.Format, bytes.NewReader(zip), int64(len(zip)))
			if err != nil {
				return lockfile.Provenance{}, false, skipped, err
			}
			trees[ev.Format] = tree
		}
		vi, err := provenance.Verify(ctx, ev, provenance.Subject{Version: v, Subtree: subtree}, tree, id, c.TrustedRoot)
		switch {
		case err == nil:
			rec, err := provenance.Record(ev, vi)
			if err != nil {
				return lockfile.Provenance{}, false, skipped, err
			}
			if !accept(rec) {
				skipped++
				continue
			}
			return rec, true, skipped, nil
		case errors.Is(err, provenance.ErrNoTransparency):
			// Unverifiable, defined absent (REQ-prov-signed-tag).
			skipped++
		case errors.Is(err, gitprov.ErrIdentityMismatch):
			// A valid signature by a signer the policy does not accept:
			// a non-acceptance, not tampering.
			skipped++
		default:
			return lockfile.Provenance{}, false, skipped, fmt.Errorf("modfetch: evidence rejected: %w", err)
		}
	}
	return lockfile.Provenance{}, false, skipped, nil
}
