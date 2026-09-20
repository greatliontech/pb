// Package provtest holds a signer for tests: a synthetic sigstore
// (gitprov's sigstoretest) with one leaf identity, signing tag
// payloads as gitsign's offline mode does — a genuine, offline-
// verifiable Rekor entry embedded in the CMS unsigned attributes — so
// verification paths under test run real cryptography end to end
// against the matching pinned trusted root.
package provtest

import (
	"crypto/ecdsa"
	"crypto/x509"
	"testing"

	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/gitprov/sigstoretest"
)

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

// SignedTag signs payload as gitsign's offline mode writes a tag, the
// log's entry embedded unless o says otherwise, and returns the raw
// tag bytes with the signature appended in-body.
func (s *Signer) SignedTag(t testing.TB, payload []byte, o sigstoretest.TagOptions) []byte {
	t.Helper()
	return s.ss.SignedTag(t, s.leaf, s.key, payload, o)
}
