// Package discover finds a plugin image's signature carriers at the
// repository the image came from, in the order the contract fixes
// (provenance.md REQ-prov-plugin-carriers), yielding each as it is
// fetched so a judgement stops at the first it accepts. This is the
// one place cosign's storage conventions — media types, annotations,
// the signature tag — are spelled.
package discover

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/pb/internal/provenance/image"
)

// cosign's storage conventions: the bundle referrer's artifact type
// and the annotation naming what it attests; the legacy signature
// artifact's configuration media type, the simple-signing layer media
// type and the layer annotations carrying the envelope's parts; the
// signature tag's suffix.
const (
	bundleMediaType        = "application/vnd.dev.sigstore.bundle.v0.3+json"
	predicateAnnotation    = "dev.sigstore.bundle.predicateType"
	cosignSignPredicate    = "https://sigstore.dev/cosign/sign/v1"
	legacyConfigMediaType  = "application/vnd.dev.cosign.artifact.sig.v1+json"
	simpleSigningMediaType = "application/vnd.dev.cosign.simplesigning.v1+json"
	annotationSignature    = "dev.cosignproject.cosign/signature"
	annotationCertificate  = "dev.sigstore.cosign/certificate"
	annotationChain        = "dev.sigstore.cosign/chain"
	annotationRekorBundle  = "dev.sigstore.cosign/bundle"
	annotationTimestamp    = "dev.sigstore.cosign/rfc3161timestamp"
	signatureTagSuffix     = ".sig"
)

// MaxCarrierBytes bounds one carrier's bytes
// (REQ-prov-plugin-carriers): a bundle or a payload is kilobytes, and
// a registry serving more for one is serving something else.
const MaxCarrierBytes = 4 << 20

// Discover yields the carriers for digest from repo in the contract's
// order — bundle referrers, legacy signature referrers, then the
// signature tag's layers — fetching each as it is taken. Referrers are
// those the registry lists, or those its referrers fallback tag lists
// where it has no referrers API, in digest order. What is not
// evidence is passed over; an absent tag and an empty referrers list
// are no evidence; a carrier that is not cosign's shape — a bundle
// referrer with other than one layer, a carrier over MaxCarrierBytes
// — is yielded as rejected evidence; a registry error is yielded as
// the step's error.
func Discover(ctx context.Context, repo name.Repository, digest v1.Hash, opts ...remote.Option) image.Carriers {
	opts = append(slices.Clone(opts), remote.WithContext(ctx))
	return func(yield func(image.Carrier, error) bool) {
		idx, err := remote.Referrers(repo.Digest(digest.String()), opts...)
		if err != nil {
			yield(image.Carrier{}, fmt.Errorf("discover: referrers of %s: %w", digest, err))
			return
		}
		im, err := idx.IndexManifest()
		if err != nil {
			yield(image.Carrier{}, fmt.Errorf("discover: referrers of %s: %w", digest, err))
			return
		}
		referrers := slices.Clone(im.Manifests)
		slices.SortFunc(referrers, func(a, b v1.Descriptor) int { return strings.Compare(a.Digest.String(), b.Digest.String()) })
		for _, d := range referrers {
			if d.ArtifactType != bundleMediaType || d.Annotations[predicateAnnotation] != cosignSignPredicate {
				continue
			}
			if !yield(bundleReferrer(repo, d, opts)) {
				return
			}
		}
		for _, d := range referrers {
			if d.ArtifactType != legacyConfigMediaType {
				continue
			}
			where := "referrer " + d.Digest.String()
			img, err := remote.Image(repo.Digest(d.Digest.String()), opts...)
			if err != nil {
				yield(image.Carrier{}, fmt.Errorf("discover: %s: %w", where, err))
				return
			}
			if !envelopes(img, where, yield) {
				return
			}
		}
		tag := repo.Tag(digest.Algorithm + "-" + digest.Hex + signatureTagSuffix)
		img, err := remote.Image(tag, opts...)
		if err != nil {
			if !notFound(err) {
				yield(image.Carrier{}, fmt.Errorf("discover: tag %s: %w", tag.TagStr(), err))
			}
			return
		}
		envelopes(img, "tag "+tag.TagStr(), yield)
	}
}

// bundleReferrer reads the bundle a referrer carries as its one
// layer; another layer count is not cosign's shape.
func bundleReferrer(repo name.Repository, d v1.Descriptor, opts []remote.Option) (image.Carrier, error) {
	where := "referrer " + d.Digest.String()
	img, err := remote.Image(repo.Digest(d.Digest.String()), opts...)
	if err != nil {
		return image.Carrier{}, fmt.Errorf("discover: %s: %w", where, err)
	}
	layers, err := img.Layers()
	if err != nil {
		return image.Carrier{}, fmt.Errorf("discover: %s: %w", where, err)
	}
	if len(layers) != 1 {
		return image.Carrier{}, fmt.Errorf("discover: evidence rejected: %s: %d layers, want the bundle as one", where, len(layers))
	}
	raw, err := layerBytes(layers[0])
	if err != nil {
		return image.Carrier{}, fmt.Errorf("discover: %s: %w", where, err)
	}
	return image.Carrier{Where: where, Value: gitprov.SigstoreBundle{JSON: raw}}, nil
}

// envelopes yields every simple-signing layer of a signature image as
// an envelope, the parts from the layer's annotations; layers of
// another media type are not evidence. It reports whether the
// consumer wants more.
func envelopes(img v1.Image, where string, yield func(image.Carrier, error) bool) bool {
	m, err := img.Manifest()
	if err != nil {
		return yield(image.Carrier{}, fmt.Errorf("discover: %s: %w", where, err))
	}
	layers, err := img.Layers()
	if err != nil {
		return yield(image.Carrier{}, fmt.Errorf("discover: %s: %w", where, err))
	}
	if len(layers) != len(m.Layers) {
		return yield(image.Carrier{}, fmt.Errorf("discover: %s: %d layers for %d descriptors", where, len(layers), len(m.Layers)))
	}
	for i, d := range m.Layers {
		if string(d.MediaType) != simpleSigningMediaType {
			continue
		}
		at := fmt.Sprintf("%s layer %d", where, i)
		payload, err := layerBytes(layers[i])
		if err != nil {
			return yield(image.Carrier{}, fmt.Errorf("discover: %s: %w", at, err))
		}
		c := image.Carrier{Where: at, Value: gitprov.SimpleSigningEnvelope{
			Payload:          payload,
			Signature:        d.Annotations[annotationSignature],
			Certificate:      d.Annotations[annotationCertificate],
			Chain:            d.Annotations[annotationChain],
			RekorBundle:      d.Annotations[annotationRekorBundle],
			RFC3161Timestamp: d.Annotations[annotationTimestamp],
		}}
		if !yield(c, nil) {
			return false
		}
	}
	return true
}

// layerBytes reads a carrier layer as stored, bounded.
func layerBytes(l v1.Layer) ([]byte, error) {
	rc, err := l.Compressed()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, MaxCarrierBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxCarrierBytes {
		return nil, fmt.Errorf("evidence rejected: a carrier over %d bytes", MaxCarrierBytes)
	}
	return raw, nil
}

// notFound reports a registry answering that a manifest does not
// exist, the one registry answer that is no evidence.
func notFound(err error) bool {
	var te *transport.Error
	return errors.As(err, &te) && te.StatusCode == http.StatusNotFound
}
