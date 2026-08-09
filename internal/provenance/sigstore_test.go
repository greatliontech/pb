package provenance

// Test-side gitsign-equivalent signing: a virtual sigstore (fulcio CA +
// rekor log) signs tag payloads and embeds a genuine, offline-verifiable
// Rekor transparency proof in the CMS unsigned attributes — the exact
// shape gitsign's offline Rekor mode produces, so the verification path
// under test runs real cryptography end to end.

import (
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"testing"
	"time"

	cms "github.com/github/smimesign/ietf-cms"
	"github.com/github/smimesign/ietf-cms/protocol"
	"github.com/go-openapi/strfmt"
	"github.com/go-openapi/swag/conv"
	"github.com/greatliontech/gitprov"
	gitsign "github.com/sigstore/gitsign/pkg/git"
	commonpb "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	rekorpb "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/rekor/pkg/generated/models"
	"github.com/sigstore/rekor/pkg/types"
	hashedrekord_v001 "github.com/sigstore/rekor/pkg/types/hashedrekord/v0.0.1"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"google.golang.org/protobuf/proto"
)

// oidRekorTLE is the CMS unsigned-attribute OID carrying a serialized
// Rekor TransparencyLogEntry (the gitsign embedded-proof location).
var oidRekorTLE = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 3, 1}

const (
	testSubject = "signer@example.com"
	testIssuer  = "https://accounts.example.com"
)

// virtualSigner is a virtual sigstore with one leaf identity and the
// pinned trusted root built from it.
type virtualSigner struct {
	vs     *ca.VirtualSigstore
	leaf   *x509.Certificate
	signer crypto.Signer
	root   *gitprov.TrustedRoot
}

func newVirtualSigner(t *testing.T) *virtualSigner {
	t.Helper()
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatalf("NewVirtualSigstore: %v", err)
	}
	leaf, key, err := vs.GenerateLeafCert(testSubject, testIssuer)
	if err != nil {
		t.Fatalf("GenerateLeafCert: %v", err)
	}
	tr, err := root.NewTrustedRoot(
		root.TrustedRootMediaType01,
		vs.FulcioCertificateAuthorities(),
		vs.CTLogs(),
		vs.TimestampingAuthorities(),
		vs.RekorLogs(),
	)
	if err != nil {
		t.Fatalf("NewTrustedRoot: %v", err)
	}
	raw, err := tr.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	proot, err := gitprov.ParseTrustedRoot(raw)
	if err != nil {
		t.Fatalf("ParseTrustedRoot: %v", err)
	}
	return &virtualSigner{vs: vs, leaf: leaf, signer: key.(crypto.Signer), root: proot}
}

func (s *virtualSigner) identity() gitprov.Identity {
	return gitprov.Identity{Issuer: testIssuer, Subject: testSubject}
}

// signedTag signs payload and returns the raw tag bytes with the
// signature appended in-body, with or without an embedded Rekor proof.
func (s *virtualSigner) signedTag(t *testing.T, payload []byte, embedProof bool) []byte {
	t.Helper()
	der, err := cms.SignDetached(payload, []*x509.Certificate{s.leaf}, s.signer)
	if err != nil {
		t.Fatalf("SignDetached: %v", err)
	}
	if embedProof {
		der = s.embedRekor(t, der)
	}
	sig := pem.EncodeToMemory(&pem.Block{Type: "SIGNED MESSAGE", Bytes: der})
	raw, err := gitsign.JoinTag(&gitsign.TagSig{Payload: payload, InBody: sig})
	if err != nil {
		t.Fatalf("JoinTag: %v", err)
	}
	return raw
}

// embedRekor logs the CMS signature in the virtual Rekor and embeds the
// resulting entry — SET, inclusion proof, checkpoint — as the gitsign
// unsigned attribute, returning the re-encoded DER.
func (s *virtualSigner) embedRekor(t *testing.T, der []byte) []byte {
	t.Helper()
	ci, err := protocol.ParseContentInfo(der)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := ci.SignedDataContent()
	if err != nil {
		t.Fatal(err)
	}
	si := &sd.SignerInfos[0]
	message, err := si.SignedAttrs.MarshaledForVerification()
	if err != nil {
		t.Fatal(err)
	}

	body := hashedRekordBody(t, message, si.Signature, s.leaf)
	proof, err := s.vs.GetInclusionProof(body)
	if err != nil {
		t.Fatalf("GetInclusionProof: %v", err)
	}
	logID, err := s.vs.RekorLogID()
	if err != nil {
		t.Fatal(err)
	}
	// sigstore-go quirk: NewTrustedRoot's MarshalJSON treats the hex
	// log-id map key as raw bytes, so the JSON round-trip double-hex-
	// encodes the id — the parsed pinned root knows the log as
	// hex(ascii(logID)). The embedded entry and the SET must speak the
	// parsed root's dialect: KeyId is the ASCII hex bytes, and the SET
	// covers their hex encoding.
	keyID := []byte(logID)
	integrated := time.Now().Unix()
	set, err := s.vs.RekorSignPayload(tlog.RekorPayload{
		Body:           base64.StdEncoding.EncodeToString(body),
		IntegratedTime: integrated,
		LogIndex:       *proof.LogIndex,
		LogID:          hex.EncodeToString(keyID),
	})
	if err != nil {
		t.Fatalf("RekorSignPayload: %v", err)
	}
	hashes := make([][]byte, len(proof.Hashes))
	for i, h := range proof.Hashes {
		if hashes[i], err = hex.DecodeString(h); err != nil {
			t.Fatal(err)
		}
	}
	rootHash, err := hex.DecodeString(*proof.RootHash)
	if err != nil {
		t.Fatal(err)
	}
	entry := &rekorpb.TransparencyLogEntry{
		LogIndex:         *proof.LogIndex,
		LogId:            &commonpb.LogId{KeyId: keyID},
		KindVersion:      &rekorpb.KindVersion{Kind: "hashedrekord", Version: "0.0.1"},
		IntegratedTime:   integrated,
		InclusionPromise: &rekorpb.InclusionPromise{SignedEntryTimestamp: set},
		InclusionProof: &rekorpb.InclusionProof{
			LogIndex: *proof.LogIndex,
			RootHash: rootHash,
			TreeSize: *proof.TreeSize,
			Hashes:   hashes,
			Checkpoint: &rekorpb.Checkpoint{
				Envelope: *proof.Checkpoint,
			},
		},
		CanonicalizedBody: body,
	}
	pbBytes, err := proto.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	attr, err := protocol.NewAttribute(oidRekorTLE, pbBytes)
	if err != nil {
		t.Fatal(err)
	}
	si.UnsignedAttrs = append(si.UnsignedAttrs, attr)
	out, err := sd.ContentInfoDER()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// hashedRekordBody canonicalizes the Rekor HashedRekord for the CMS
// triple — the exact body the verifier recomputes and binds.
func hashedRekordBody(t *testing.T, message, sig []byte, cert *x509.Certificate) []byte {
	t.Helper()
	hash := sha256.Sum256(message)
	certPEM, err := cryptoutils.MarshalCertificateToPEM(cert)
	if err != nil {
		t.Fatal(err)
	}
	re := &hashedrekord_v001.V001Entry{
		HashedRekordObj: models.HashedrekordV001Schema{
			Data: &models.HashedrekordV001SchemaData{
				Hash: &models.HashedrekordV001SchemaDataHash{
					Algorithm: conv.Pointer("sha256"),
					Value:     conv.Pointer(hex.EncodeToString(hash[:])),
				},
			},
			Signature: &models.HashedrekordV001SchemaSignature{
				Content: strfmt.Base64(sig),
				PublicKey: &models.HashedrekordV001SchemaSignaturePublicKey{
					Content: strfmt.Base64(certPEM),
				},
			},
		},
	}
	body, err := types.CanonicalizeEntry(context.Background(), re)
	if err != nil {
		t.Fatalf("CanonicalizeEntry: %v", err)
	}
	return body
}
