// Package provtest builds gitsign-equivalent signed-tag evidence for
// tests: a synthetic sigstore (gitprov's sigstoretest — a Fulcio whose
// leaves carry a certificate timestamp, a Rekor log of its own signing
// entry timestamps and checkpoints) signs tag payloads and embeds a
// genuine, offline-verifiable Rekor transparency proof in the CMS
// unsigned attributes — the exact shape gitsign's offline Rekor mode
// produces — so verification paths under test run real cryptography
// end to end against the matching pinned trusted root.
package provtest

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"testing"
	"time"

	cms "github.com/github/smimesign/ietf-cms"
	"github.com/github/smimesign/ietf-cms/protocol"
	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/gitprov/sigstoretest"
	gitsign "github.com/sigstore/gitsign/pkg/git"
	"google.golang.org/protobuf/proto"
)

// oidRekorTLE is the CMS unsigned-attribute OID carrying a serialized
// Rekor TransparencyLogEntry (the gitsign embedded-proof location).
var oidRekorTLE = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 3, 1}

// The leaf identity New issues.
const (
	Subject = "signer@example.com"
	Issuer  = "https://accounts.example.com"
)

// Signer is a synthetic sigstore with one leaf identity and the pinned
// trusted root built from it.
type Signer struct {
	ss      *sigstoretest.Sigstore
	leaf    *x509.Certificate
	key     *ecdsa.PrivateKey
	subject string
	issuer  string
}

func New(t testing.TB) *Signer {
	return NewWithIdentity(t, Subject, Issuer)
}

// NewWithIdentity builds a Signer whose leaf carries the given SAN and
// OIDC issuer — for exercising identity policies beyond the fixed
// default (a CI workflow identity, an unaccepted signer).
func NewWithIdentity(t testing.TB, subject, issuer string) *Signer {
	t.Helper()
	ss := sigstoretest.New(t)
	leaf, key := ss.Leaf(t, subject, issuer)
	return &Signer{ss: ss, leaf: leaf, key: key, subject: subject, issuer: issuer}
}

// TrustedRoot is the pinned trusted root built from the synthetic
// sigstore — the root every SignedTag verifies against.
func (s *Signer) TrustedRoot() *gitprov.TrustedRoot { return s.ss.TrustedRoot() }

// Identity is the leaf's identity, as a rule accepting exactly it.
func (s *Signer) Identity() gitprov.Identity {
	return gitprov.Identity{Issuer: s.issuer, Subject: s.subject}
}

// EvidenceOptions shape a signed tag's embedded entry away from the
// default, the log's own: CheckpointBy is the sigstore whose log signs
// the entry's checkpoint in its place, a checkpoint no pinned key
// verifies.
type EvidenceOptions struct {
	CheckpointBy *sigstoretest.Sigstore
}

// SignedTag signs payload and returns the raw tag bytes with the
// signature appended in-body, with or without an embedded Rekor proof.
func (s *Signer) SignedTag(t testing.TB, payload []byte, embedProof bool) []byte {
	t.Helper()
	if !embedProof {
		return s.signedTag(t, payload, nil)
	}
	return s.SignedTagWith(t, payload, EvidenceOptions{})
}

// SignedTagWith is SignedTag with an embedded proof shaped by o.
func (s *Signer) SignedTagWith(t testing.TB, payload []byte, o EvidenceOptions) []byte {
	t.Helper()
	return s.signedTag(t, payload, &o)
}

// signedTag signs payload, embedding an entry when o is not nil.
func (s *Signer) signedTag(t testing.TB, payload []byte, o *EvidenceOptions) []byte {
	t.Helper()
	der, err := cms.SignDetached(payload, []*x509.Certificate{s.leaf}, s.key)
	if err != nil {
		t.Fatalf("SignDetached: %v", err)
	}
	if o != nil {
		der = s.embedRekor(t, der, *o)
	}
	sig := pem.EncodeToMemory(&pem.Block{Type: "SIGNED MESSAGE", Bytes: der})
	raw, err := gitsign.JoinTag(&gitsign.TagSig{Payload: payload, InBody: sig})
	if err != nil {
		t.Fatalf("JoinTag: %v", err)
	}
	return raw
}

// embedRekor logs the CMS signature in the synthetic sigstore's Rekor
// log and embeds the resulting entry — SET, inclusion proof,
// checkpoint — as the gitsign unsigned attribute, returning the
// re-encoded DER. The entry is integrated now, which the synthetic
// sigstore holds to the leaf's validity window, so the verifier
// judging the leaf at the integrated time accepts it.
func (s *Signer) embedRekor(t testing.TB, der []byte, o EvidenceOptions) []byte {
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
	body, err := gitprov.HashedRekordBody(context.Background(), message, si.Signature, s.leaf)
	if err != nil {
		t.Fatalf("HashedRekordBody: %v", err)
	}
	pbBytes, err := proto.Marshal(s.ss.LogEntryWith(t, s.leaf, body, time.Time{}, sigstoretest.EntryOptions{CheckpointBy: o.CheckpointBy}))
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
