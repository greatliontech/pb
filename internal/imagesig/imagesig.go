// Package imagesig judges a plugin image's signature evidence: cosign's
// carriers, found by the discover package, are judged in order through
// gitprov against the pinned trusted root until one is accepted
// (provenance.md REQ-prov-plugin-classification). The judgement is
// offline — this package reaches no network — and a carrier is
// fetched only as it is judged, so nothing past the accepted one is
// fetched. What a carrier means is gitprov's.
package imagesig

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/pb/internal/lockfile"
)

// Evidence-classification sentinels for a judgement that accepted
// nothing for a tolerable reason — absence, a signer the policy does
// not accept — and for one that accepted nothing the caller's
// acceptance admits. Any other failure is rejected evidence.
var (
	ErrNoEvidence          = errors.New("imagesig: no provenance evidence found")
	ErrIdentityNotAccepted = errors.New("imagesig: evidence identity not accepted by policy")
	ErrRecordNotReproduced = errors.New("imagesig: no evidence reproduces the recorded provenance")
)

// Carrier is one piece of evidence found for a digest, and where it
// was found, for the message a rejection names.
type Carrier struct {
	Where string
	Value gitprov.ImageCarrier
}

// Carriers is the evidence for a digest as discovery yields it, one
// carrier at a time in the contract's order; a step's error is a
// carrier that could not be fetched or is not cosign's shape, which
// rejects the evidence.
type Carriers = iter.Seq2[Carrier, error]

// Judge verifies carriers in order until one is accepted, returning
// its record (REQ-prov-plugin-classification): a carrier without the
// material a signed time comes from is unverifiable and passed over;
// one signed by an identity the policy does not accept is a
// non-acceptance and passed over; one failing any other way, or one
// discovery could not fetch, is rejected evidence, the error naming
// where it was found. A carrier the policy accepts but accept
// declines — a pinned image's record it does not reproduce — is
// passed over while another may reproduce it; nil accepts every one.
// With none accepted the error is ErrRecordNotReproduced when one was
// declined, ErrIdentityNotAccepted when a signer was refused,
// ErrNoEvidence otherwise; a declined record is returned with its
// error for the caller's message.
func Judge(ctx context.Context, digest string, carriers Carriers, id gitprov.Identity, root *gitprov.TrustedRoot, accept func(lockfile.Provenance) bool) (lockfile.Provenance, error) {
	unverifiable, refused := 0, 0
	var declined *lockfile.Provenance
	for c, err := range carriers {
		if err != nil {
			return lockfile.Provenance{}, err
		}
		timed, err := gitprov.HasImageTime(c.Value)
		if err != nil {
			return lockfile.Provenance{}, fmt.Errorf("imagesig: evidence rejected: %s: %w", c.Where, err)
		}
		if !timed {
			unverifiable++
			continue
		}
		vi, err := gitprov.VerifyImage(ctx, digest, c.Value, id, root)
		switch {
		case err == nil:
			rec := lockfile.Provenance{Type: lockfile.ProvenanceImageSignature, SAN: vi.Subject, Issuer: vi.Issuer}
			if accept != nil && !accept(rec) {
				if declined == nil {
					declined = &rec
				}
				continue
			}
			return rec, nil
		case errors.Is(err, gitprov.ErrIdentityMismatch):
			refused++
		default:
			return lockfile.Provenance{}, fmt.Errorf("imagesig: evidence rejected: %s: %w", c.Where, err)
		}
	}
	switch {
	case declined != nil:
		return *declined, fmt.Errorf("%w: %s by %s", ErrRecordNotReproduced, declined.SAN, declined.Issuer)
	case refused > 0:
		return lockfile.Provenance{}, fmt.Errorf("%w: %d carrier(s) signed by an identity the policy does not accept", ErrIdentityNotAccepted, refused)
	case unverifiable > 0:
		return lockfile.Provenance{}, fmt.Errorf("%w: %d carrier(s) carry no signed time", ErrNoEvidence, unverifiable)
	}
	return lockfile.Provenance{}, ErrNoEvidence
}
