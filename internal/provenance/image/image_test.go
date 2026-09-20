package image_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/gitprov/sigstoretest"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/provenance/image"
	"github.com/greatliontech/stipulator/stipulate/structural"
)

const (
	subject = "signer@example.com"
	issuer  = "https://accounts.example.com"
)

var ctx = context.Background()

func identity() gitprov.Identity { return gitprov.Identity{Subject: subject, Issuer: issuer} }

// step is one discovery step: a carrier, or the error fetching it.
type step struct {
	c   image.Carrier
	err error
}

// sequence is discovery yielding steps in order, recording how many
// were taken.
func sequence(taken *int, steps ...step) image.Carriers {
	return func(yield func(image.Carrier, error) bool) {
		for _, s := range steps {
			*taken++
			if !yield(s.c, s.err) {
				return
			}
		}
	}
}

// Judgement is the contract's classification
// (REQ-prov-plugin-classification): the first accepted carrier is
// recorded as an image-signature record naming the verified identity
// and nothing past it is taken; an unverifiable carrier and a refused
// signer are passed over; any other failure, and a step discovery
// could not take, rejects; and it runs offline (REQ-prov-offline).
func TestJudgeClassifies(t *testing.T) {
	s := sigstoretest.New(t)
	digest := "sha256:" + strings.Repeat("5a", 32)
	good := step{c: image.Carrier{Where: "good", Value: s.Bundle(t, digest, subject, issuer, sigstoretest.BundleOptions{})}}
	other := step{c: image.Carrier{Where: "other signer", Value: s.Bundle(t, digest, "other@example.com", issuer, sigstoretest.BundleOptions{})}}
	untimed := step{c: image.Carrier{Where: "untimed", Value: s.Envelope(t, digest, subject, issuer, sigstoretest.EnvelopeOptions{NoRekorBundle: true})}}
	rejected := step{c: image.Carrier{Where: "two signatures", Value: s.Bundle(t, digest, subject, issuer, sigstoretest.BundleOptions{TwoSignatures: true})}}
	wrongDigest := step{c: image.Carrier{Where: "another digest", Value: s.Bundle(t, "sha256:"+strings.Repeat("6b", 32), subject, issuer, sigstoretest.BundleOptions{})}}
	unfetched := step{err: errors.New("discover: referrer x: the registry went away")}
	garbage := step{c: image.Carrier{Where: "garbage", Value: gitprov.SigstoreBundle{JSON: []byte("{")}}}
	sigstoretest.RefuseNetwork(t)

	want := lockfile.Provenance{Type: lockfile.ProvenanceImageSignature, SAN: subject, Issuer: issuer}
	taken := 0
	if rec, err := image.Judge(ctx, digest, sequence(&taken, untimed, other, good, rejected, unfetched), identity(), s.TrustedRoot(), nil); err != nil || rec != want {
		t.Fatalf("accepted = %+v, %v; want %+v", rec, err, want)
	}
	if taken != 3 {
		t.Fatalf("%d steps taken, want 3: nothing past the accepted carrier", taken)
	}
	if _, err := image.Judge(ctx, digest, sequence(&taken), identity(), s.TrustedRoot(), nil); !errors.Is(err, image.ErrNoEvidence) {
		t.Fatalf("no carriers: %v", err)
	}
	if _, err := image.Judge(ctx, digest, sequence(&taken, untimed), identity(), s.TrustedRoot(), nil); !errors.Is(err, image.ErrNoEvidence) || !strings.Contains(err.Error(), "no signed time") {
		t.Fatalf("an unverifiable carrier alone: %v", err)
	}
	if _, err := image.Judge(ctx, digest, sequence(&taken, untimed, other), identity(), s.TrustedRoot(), nil); !errors.Is(err, image.ErrIdentityNotAccepted) {
		t.Fatalf("a refused signer: %v", err)
	}
	for _, bad := range []step{rejected, wrongDigest, garbage, unfetched} {
		_, err := image.Judge(ctx, digest, sequence(&taken, bad, good), identity(), s.TrustedRoot(), nil)
		if err == nil || errors.Is(err, image.ErrNoEvidence) || errors.Is(err, image.ErrIdentityNotAccepted) {
			t.Fatalf("%s before a good carrier: %v, want rejection", bad.c.Where, err)
		}
		if bad.c.Where != "" && !strings.Contains(err.Error(), bad.c.Where) {
			t.Fatalf("rejection does not name where: %v", err)
		}
	}
}

// A caller's acceptance — a pinned image's record — passes over a
// carrier the policy accepts but the record is not, while another
// may reproduce it; with none reproducing it the declined record
// comes back under ErrRecordNotReproduced.
func TestJudgePrefersTheAcceptedRecord(t *testing.T) {
	s := sigstoretest.New(t)
	digest := "sha256:" + strings.Repeat("5a", 32)
	recorded := lockfile.Provenance{Type: lockfile.ProvenanceImageSignature, SAN: subject, Issuer: issuer}
	pinned := func(rec lockfile.Provenance) bool { return rec == recorded }
	any := gitprov.Identity{IssuerGlob: "**", SubjectGlob: "**"}
	mine := step{c: image.Carrier{Where: "mine", Value: s.Bundle(t, digest, subject, issuer, sigstoretest.BundleOptions{})}}
	theirs := step{c: image.Carrier{Where: "theirs", Value: s.Bundle(t, digest, "other@example.com", issuer, sigstoretest.BundleOptions{})}}
	taken := 0
	if rec, err := image.Judge(ctx, digest, sequence(&taken, theirs, mine), any, s.TrustedRoot(), pinned); err != nil || rec != recorded {
		t.Fatalf("the reproducing carrier after a declined one: %+v %v", rec, err)
	}
	rec, err := image.Judge(ctx, digest, sequence(&taken, theirs), any, s.TrustedRoot(), pinned)
	if !errors.Is(err, image.ErrRecordNotReproduced) || rec.SAN != "other@example.com" {
		t.Fatalf("nothing reproducing the record: %+v %v", rec, err)
	}
	// Declined outranks refused and absent in the reason given.
	refused := step{c: image.Carrier{Where: "refused", Value: s.Bundle(t, digest, "third@example.com", issuer, sigstoretest.BundleOptions{})}}
	mineOnly := gitprov.Identity{Issuer: issuer, SubjectGlob: "{" + subject + ",other@example.com}"}
	if _, err := image.Judge(ctx, digest, sequence(&taken, refused, theirs), mineOnly, s.TrustedRoot(), pinned); !errors.Is(err, image.ErrRecordNotReproduced) {
		t.Fatalf("declined beside refused: %v", err)
	}
}

// A recorder keeps what the judgement took, in order, up to the
// stop, and never an erring step.
func TestRecorderKeepsWhatWasTaken(t *testing.T) {
	steps := []step{{c: image.Carrier{Where: "a"}}, {err: errors.New("b unfetched")}, {c: image.Carrier{Where: "c"}}, {c: image.Carrier{Where: "d"}}}
	taken := 0
	var r image.Recorder
	n := 0
	for c, err := range r.Of(sequence(&taken, steps...)) {
		n++
		if n == 3 && (err != nil || c.Where != "c") {
			t.Fatalf("step 3 = %+v %v", c, err)
		}
		if n == 3 {
			break
		}
	}
	if len(r.Taken) != 2 || r.Taken[0].Where != "a" || r.Taken[1].Where != "c" {
		t.Fatalf("taken = %+v, want a and c", r.Taken)
	}
}

// The judge reaches no network: its imports carry no capability to
// (REQ-prov-offline), the structural half of the witness above.
func TestJudgeImportsCarryNoNetworkCapability(t *testing.T) {
	structural.ImportAllowlist(t, "github.com/greatliontech/pb/internal/provenance/image", map[string]structural.ImportRule{
		"github.com/greatliontech/pb/internal/provenance/image": {
			Internal:                []string{"github.com/greatliontech/pb/internal/module/lockfile"},
			ThirdParty:              []string{"github.com/greatliontech/gitprov"},
			RestrictStandardLibrary: true,
			StandardLibrary:         []string{"context", "errors", "fmt", "iter"},
		},
	})
}
