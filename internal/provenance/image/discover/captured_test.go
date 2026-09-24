package discover_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/pb/internal/provenance/image/discover"
	"github.com/greatliontech/pb/internal/testing/imagetest"
)

// The capture under testdata/cosign: an image cosign v3.1.3+dirty (a distribution build) signed
// keyless twice at ghcr.io, and every answer the registry gave
// (testdata/cosign/README.md).
const (
	capturedDir      = "testdata/cosign"
	capturedRepo     = "greatliontech/gitprov-cosign-capture"
	capturedHex      = "facb5564762d06aa0d30bba81be04b6d078cd289cb861e864dafd36a85322f28"
	capturedReferrer = "b1e9df191fbadbc3821100ef1fd73035f10f619a78eca18c8cfa900068c622b8"
	capturedIdentity = "nikolas@greatlion.tech"
	capturedIssuer   = "https://accounts.google.com"
)

func captured(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(capturedDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Discovery over the carriers cosign itself pushed and the answers
// a real registry gave (REQ-prov-plugin-carriers): ghcr has no
// referrers API, so the bundle referrer is found through the
// fallback tag cosign wrote — whose descriptor carries no
// annotations, the predicate read from the manifest — then the
// legacy layer under the signature tag; each carrier's bytes are the
// registry's, and each verifies against the trusted root in force at
// signing under the signer's identity.
func TestDiscoverCapturedCosignCarriers(t *testing.T) {
	referrerStem := "referrer-" + capturedReferrer
	status, err := strconv.Atoi(strings.TrimSpace(string(captured(t, "referrers.status"))))
	if err != nil || status != 404 {
		t.Fatalf("the capture's referrers API status is %q; ghcr answers 404", captured(t, "referrers.status"))
	}
	repo := imagetest.Replay(t, capturedRepo, map[string][]byte{
		"sha256:" + capturedHex:          captured(t, "image-manifest.json"),
		"sha256-" + capturedHex:          captured(t, "referrers-fallback-tag.manifest.json"),
		"sha256:" + capturedReferrer:     captured(t, referrerStem+".manifest.json"),
		"sha256-" + capturedHex + ".sig": captured(t, "signature-tag.manifest.json"),
	}, [][]byte{
		captured(t, referrerStem+".layer-0.bin"), captured(t, referrerStem+".config.bin"),
		captured(t, "signature-tag.layer-0.bin"), captured(t, "signature-tag.config.bin"),
	}, status, captured(t, "referrers.json"))
	digest := v1.Hash{Algorithm: "sha256", Hex: capturedHex}
	carriers, err := all(discover.Discover(ctx, repo, digest))
	if err != nil {
		t.Fatal(err)
	}
	if len(carriers) != 2 {
		t.Fatalf("found %d carriers, want 2: %+v", len(carriers), carriers)
	}
	if b, ok := carriers[0].Value.(gitprov.SigstoreBundle); !ok || !bytes.Equal(b.JSON, captured(t, referrerStem+".layer-0.bin")) || carriers[0].Where != "referrer sha256:"+capturedReferrer {
		t.Fatalf("first carrier = %+v, want the bundle referrer through the fallback tag", carriers[0])
	}
	var manifest struct {
		Layers []struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(captured(t, "signature-tag.manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Layers) != 1 {
		t.Fatalf("the signature tag's manifest names %d layers", len(manifest.Layers))
	}
	a := manifest.Layers[0].Annotations
	e, ok := carriers[1].Value.(gitprov.SimpleSigningEnvelope)
	if !ok || carriers[1].Where != "tag sha256-"+capturedHex+".sig layer 0" {
		t.Fatalf("second carrier = %+v, want the signature tag's layer", carriers[1])
	}
	if e.Signature != a["dev.cosignproject.cosign/signature"] || e.Certificate != a["dev.sigstore.cosign/certificate"] ||
		e.RekorBundle != a["dev.sigstore.cosign/bundle"] || e.Chain != a["dev.sigstore.cosign/chain"] || e.RFC3161Timestamp != "" {
		t.Fatalf("envelope parts differ from the layer's annotations: %+v", e)
	}
	if _, has := a["dev.sigstore.cosign/rfc3161timestamp"]; has {
		t.Fatal("the legacy layer carries a timestamp annotation the capture did not record")
	}
	tr, err := gitprov.LoadTrustedRoot(filepath.Join(capturedDir, "trusted-root.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range carriers {
		vi, err := gitprov.VerifyImage(ctx, digest.String(), c.Value, gitprov.Identity{Subject: capturedIdentity, Issuer: capturedIssuer}, tr)
		if err != nil {
			t.Fatalf("%s: %v", c.Where, err)
		}
		if vi.Digest != digest.String() || vi.Subject != capturedIdentity {
			t.Fatalf("%s: verified %+v", c.Where, vi)
		}
	}
}
