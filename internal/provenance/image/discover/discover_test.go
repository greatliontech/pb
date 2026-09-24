package discover_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/gitprov/sigstoretest"
	"github.com/greatliontech/pb/internal/provenance/image"
	"github.com/greatliontech/pb/internal/provenance/image/discover"
	"github.com/greatliontech/pb/internal/testing/imagetest"
)

const (
	subject = "signer@example.com"
	issuer  = "https://accounts.example.com"
)

var ctx = context.Background()

// all takes every step of a discovery, the carriers and the first
// error.
func all(seq image.Carriers) ([]image.Carrier, error) {
	var out []image.Carrier
	for c, err := range seq {
		if err != nil {
			return out, err
		}
		out = append(out, c)
	}
	return out, nil
}

var modes = map[string]bool{"referrers API": true, "fallback tag": false}

// Discovery finds cosign's three locations in the contract's order —
// bundle referrers, legacy referrers, the signature tag's layers —
// with what is not evidence passed over (REQ-prov-plugin-carriers),
// against a registry answering the referrers API and against one
// whose referrers are the fallback tag's.
func TestDiscoverFindsEveryLocationInOrder(t *testing.T) {
	for mode, api := range modes {
		t.Run(mode, func(t *testing.T) {
			s := sigstoretest.New(t)
			repo := imagetest.Registry(t, api)
			digest := imagetest.PushIndex(t, repo, "v1")
			bundle := s.Bundle(t, digest.String(), subject, issuer, sigstoretest.BundleOptions{})
			legacy := s.Envelope(t, digest.String(), subject, issuer, sigstoretest.EnvelopeOptions{})
			tagged := s.Envelope(t, digest.String(), subject, issuer, sigstoretest.EnvelopeOptions{Timestamp: true})

			// The tag first, then the referrers, so discovery's order is
			// the contract's and not the push order.
			imagetest.Tag(t, repo, imagetest.SignatureTag(digest), []imagetest.Layer{
				{MediaType: "application/vnd.oci.image.layer.v1.tar", Content: []byte("not a signature")},
				imagetest.EnvelopeLayer(tagged),
			})
			imagetest.Attach(t, repo, digest, imagetest.Artifact{ArtifactType: imagetest.LegacyConfigMediaType, Layers: []imagetest.Layer{imagetest.EnvelopeLayer(legacy)}})
			// An attestation under the bundle media type is not a signature.
			imagetest.Attach(t, repo, digest, imagetest.BundleArtifact(s.Bundle(t, digest.String(), subject, issuer, sigstoretest.BundleOptions{Predicate: "https://slsa.dev/provenance/v1"}), "https://slsa.dev/provenance/v1"))
			// An unrelated referrer is not evidence either.
			imagetest.Attach(t, repo, digest, imagetest.Artifact{ArtifactType: "application/vnd.example.sbom", Layers: []imagetest.Layer{{MediaType: "application/json", Content: []byte("{}")}}})
			imagetest.Attach(t, repo, digest, imagetest.BundleArtifact(bundle, imagetest.CosignSignPredicate))

			carriers, err := all(discover.Discover(ctx, repo, digest))
			if err != nil {
				t.Fatal(err)
			}
			if len(carriers) != 3 {
				t.Fatalf("found %d carriers, want 3: %+v", len(carriers), carriers)
			}
			if b, ok := carriers[0].Value.(gitprov.SigstoreBundle); !ok || !bytes.Equal(b.JSON, bundle.JSON) || !strings.HasPrefix(carriers[0].Where, "referrer ") {
				t.Fatalf("first carrier = %+v, want the bundle referrer", carriers[0])
			}
			if e, ok := carriers[1].Value.(gitprov.SimpleSigningEnvelope); !ok || e.Signature != legacy.Signature || !strings.HasPrefix(carriers[1].Where, "referrer ") {
				t.Fatalf("second carrier = %+v, want the legacy referrer's envelope", carriers[1])
			}
			if e, ok := carriers[2].Value.(gitprov.SimpleSigningEnvelope); !ok || e.Signature != tagged.Signature || e.RFC3161Timestamp != tagged.RFC3161Timestamp || carriers[2].Where != "tag "+imagetest.SignatureTag(digest)+" layer 1" {
				t.Fatalf("third carrier = %+v, want the tag's second layer", carriers[2])
			}
			// Every carrier found verifies as fetched.
			for _, c := range carriers {
				if _, err := gitprov.VerifyImage(ctx, digest.String(), c.Value, gitprov.Identity{Subject: subject, Issuer: issuer}, s.TrustedRoot()); err != nil {
					t.Fatalf("%s: %v", c.Where, err)
				}
			}
		})
	}
}

// An image with nothing attached and no signature tag has no
// evidence: no carriers, no error.
func TestDiscoverNothing(t *testing.T) {
	for mode, api := range modes {
		repo := imagetest.Registry(t, api)
		digest := imagetest.PushIndex(t, repo, "v1")
		carriers, err := all(discover.Discover(ctx, repo, digest))
		if err != nil || len(carriers) != 0 {
			t.Fatalf("%s: %v %v", mode, carriers, err)
		}
	}
}

// Referrers are taken in digest order whatever the push order, so the
// carrier recorded is a function of the repository's content.
func TestDiscoverOrdersReferrersByDigest(t *testing.T) {
	s := sigstoretest.New(t)
	repo := imagetest.Registry(t, true)
	digest := imagetest.PushIndex(t, repo, "v1")
	bundle := func() imagetest.Artifact {
		return imagetest.BundleArtifact(s.Bundle(t, digest.String(), subject, issuer, sigstoretest.BundleOptions{}), imagetest.CosignSignPredicate)
	}
	first := imagetest.Attach(t, repo, digest, bundle())
	// The second pushed sorts before the first: the push order is not
	// the digest order.
	imagetest.AttachOrdered(t, repo, digest, bundle(), imagetest.Before, first)
	carriers, err := all(discover.Discover(ctx, repo, digest))
	if err != nil || len(carriers) != 2 {
		t.Fatalf("%v %v", carriers, err)
	}
	var found []string
	for _, c := range carriers {
		found = append(found, strings.TrimPrefix(c.Where, "referrer "))
	}
	if !slices.IsSorted(found) || found[1] != first.String() {
		t.Fatalf("carriers out of digest order: %v", found)
	}
}

// Carriers are fetched as they are taken: a carrier past the one a
// consumer stops at is never fetched, so a malformed carrier sorting
// after an accepted one does not fail the acquisition, while one
// sorting before it is rejected evidence (REQ-prov-plugin-carriers).
func TestDiscoverFetchesAsTaken(t *testing.T) {
	s := sigstoretest.New(t)
	repo := imagetest.Registry(t, true)
	digest := imagetest.PushIndex(t, repo, "v1")
	good := imagetest.Attach(t, repo, digest, imagetest.BundleArtifact(s.Bundle(t, digest.String(), subject, issuer, sigstoretest.BundleOptions{}), imagetest.CosignSignPredicate))
	malformed := imagetest.BundleArtifact(s.Bundle(t, digest.String(), subject, issuer, sigstoretest.BundleOptions{}), imagetest.CosignSignPredicate)
	malformed.Layers = append(malformed.Layers, malformed.Layers[0])
	imagetest.AttachOrdered(t, repo, digest, malformed, imagetest.After, good)

	// Stopping at the first carrier: the good one, no error.
	for c, err := range discover.Discover(ctx, repo, digest) {
		if err != nil || c.Where != "referrer "+good.String() {
			t.Fatalf("first step = %+v %v", c, err)
		}
		break
	}
	// Taking every step reaches the malformed one: rejected evidence.
	if _, err := all(discover.Discover(ctx, repo, digest)); err == nil || !strings.Contains(err.Error(), "evidence rejected") {
		t.Fatalf("the malformed carrier taken: %v", err)
	}
	// One sorting before the good one is reached first.
	imagetest.AttachOrdered(t, repo, digest, malformed, imagetest.Before, good)
	for c, err := range discover.Discover(ctx, repo, digest) {
		if err == nil || !strings.Contains(err.Error(), "evidence rejected") || !strings.Contains(err.Error(), "2 layers") {
			t.Fatalf("first step = %+v %v, want the malformed carrier's rejection", c, err)
		}
		break
	}
}

// A carrier over the bound is rejected evidence, not read
// (REQ-prov-plugin-carriers).
func TestDiscoverRejectsOversizedCarrier(t *testing.T) {
	repo := imagetest.Registry(t, true)
	digest := imagetest.PushIndex(t, repo, "v1")
	huge := imagetest.BundleArtifact(gitprov.SigstoreBundle{JSON: bytes.Repeat([]byte("x"), discover.MaxCarrierBytes+1)}, imagetest.CosignSignPredicate)
	imagetest.Attach(t, repo, digest, huge)
	if _, err := all(discover.Discover(ctx, repo, digest)); err == nil || !strings.Contains(err.Error(), "evidence rejected") || !strings.Contains(err.Error(), "over") {
		t.Fatalf("an oversized carrier: %v", err)
	}
}

// A registry that cannot be reached is an error, never absence.
func TestDiscoverRegistryErrorIsAnError(t *testing.T) {
	repo, err := name.NewRepository("127.0.0.1:1/org/plugin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := all(discover.Discover(ctx, repo, v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("ab", 32)})); err == nil {
		t.Fatal("an unreachable registry discovered nothing without error")
	}
}

// A registry failing the signature tag's fetch with anything but
// "no such manifest" is an error, never absence.
func TestDiscoverTagErrorIsAnError(t *testing.T) {
	inner := imagetest.Handler(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, ".sig") {
			http.Error(w, "the tag is unavailable", http.StatusInternalServerError)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	repo, err := name.NewRepository(srv.Listener.Addr().String() + "/org/plugin")
	if err != nil {
		t.Fatal(err)
	}
	digest := imagetest.PushIndex(t, repo, "v1")
	// One attempt: the client's retries on a server error are its own.
	if _, err := all(discover.Discover(ctx, repo, digest, remote.WithRetryBackoff(remote.Backoff{Steps: 1}))); err == nil || !strings.Contains(err.Error(), "tag ") {
		t.Fatalf("a failing tag fetch: %v", err)
	}
}

// A listing's descriptor decides the predicate where it carries
// annotations — a bundle-typed referrer annotated without the
// predicate is passed over unfetched, its manifest gone or not — and
// the manifest decides where the descriptor carries none: a
// fallback tag naming a referrer whose manifest is gone is a registry
// error, which fails the acquisition (REQ-prov-plugin-carriers).
func TestDiscoverReadsThePredicateWhereTheListingCarriesAnnotations(t *testing.T) {
	s := sigstoretest.New(t)
	bundle := imagetest.BundleArtifact(s.Bundle(t, "sha256:"+strings.Repeat("ab", 32), subject, issuer, sigstoretest.BundleOptions{}), imagetest.CosignSignPredicate)
	t.Run("referrers API: annotated without the predicate, passed over unfetched", func(t *testing.T) {
		repo := imagetest.Registry(t, true)
		digest := imagetest.PushIndex(t, repo, "v1")
		content := bundle
		content.Annotations = map[string]string{imagetest.ContentAnnotation: "dsse-envelope"}
		gone := imagetest.Attach(t, repo, digest, content)
		if err := remote.Delete(repo.Digest(gone.String())); err != nil {
			t.Fatal(err)
		}
		carriers, err := all(discover.Discover(ctx, repo, digest))
		if err != nil || len(carriers) != 0 {
			t.Fatalf("carriers %+v, err %v; want none and no fetch of the gone manifest", carriers, err)
		}
	})
	t.Run("fallback tag: unannotated, the gone manifest is a registry error", func(t *testing.T) {
		repo := imagetest.Registry(t, false)
		digest := imagetest.PushIndex(t, repo, "v1")
		gone := imagetest.Attach(t, repo, digest, bundle)
		if err := remote.Delete(repo.Digest(gone.String())); err != nil {
			t.Fatal(err)
		}
		if _, err := all(discover.Discover(ctx, repo, digest)); err == nil || !strings.Contains(err.Error(), "referrer "+gone.String()) {
			t.Fatalf("Discover = %v, want the gone referrer's registry error", err)
		}
	})
}
