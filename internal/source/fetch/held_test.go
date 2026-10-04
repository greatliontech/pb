package fetch

import (
	"errors"
	"strings"
	"testing"

	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/gitprov/sigstoretest"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/provenance"
	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/pb/internal/testing/provtest"
)

// A pin recording no provenance is held to the trust policy of the
// day: a policy that has come to require provenance refuses the pin
// on every use — the module file's read and the archive's — naming
// the update that re-resolves it, and allow-unsigned still serves it
// (REQ-prov-pin-held).
func TestNonePinHeldToRequire(t *testing.T) {
	fx := newFixture(t)
	zip, _ := moduleZip(t, declaredFiles())
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	fx.Endpoint("example.com/m", "v1.0.0", "mod", declaredFiles()["pb.yaml"])
	c := fx.Client("proxy")
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	pin, _ := c.Lock.Module("example.com/m", "v1.0.0")
	if pin.Provenance != (lockfile.Provenance{}) {
		t.Fatalf("pinned %+v, want none", pin.Provenance)
	}
	for _, policy := range []*trust.Policy{
		{Default: trust.RequireProvenance},
		{Modules: []trust.Rule{{Prefix: "example.com/m", Keys: []gitprov.PinnedKey{someKey(t)}}}},
	} {
		strict := fx.Client("proxy")
		strict.Lock, strict.Policy = c.Lock, policy
		if _, err := strict.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, ErrPinUnderPolicy) || !strings.Contains(err.Error(), "pb dep update example.com/m@v1.0.0") {
			t.Fatalf("a none pin under a policy requiring provenance, the module file: %v", err)
		}
		if _, err := strict.Zip(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, ErrPinUnderPolicy) {
			t.Fatalf("a none pin under a policy requiring provenance, the archive: %v", err)
		}
	}
	lenient := fx.Client("proxy")
	lenient.Lock = c.Lock
	if _, err := lenient.Zip(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("a none pin under allow-unsigned: %v", err)
	}
	// A record of none holds no evidence: the archive's read asks no
	// source for an envelope beyond the first use's.
	if n := fx.Hits[proxyHost+"/example.com/m/@v/v1.0.0.prov"]; n != 1 {
		t.Fatalf("the envelope asked for %d times, want the first use's alone", n)
	}
}

// An identity record is held to the policy of the day: the rule's
// identity where one is written, refusing a record it does not
// accept; refused under a rule naming keys alone; standing under the
// origin default, which is the origin's at the pin
// (REQ-prov-pin-held).
func TestIdentityPinHeldToPolicy(t *testing.T) {
	fx := newProvFixture(t, provtest.New(t), sigstoretest.TagOptions{}, "v1.0.0")
	c := fx.clientWithPolicy(explicitRule(provtest.Subject, provtest.Issuer, trust.RequireProvenance))
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	under := func(p *trust.Policy) *Client {
		c2 := fx.clientWithPolicy(p)
		c2.Lock = c.Lock
		return c2
	}
	if _, err := under(explicitRule(provtest.Subject, provtest.Issuer, trust.RequireProvenance)).Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("the same rule: %v", err)
	}
	if _, err := under(explicitRule("https://github.com/other/**", provtest.Issuer, trust.RequireProvenance)).Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, ErrPinUnderPolicy) || !strings.Contains(err.Error(), provtest.Subject) {
		t.Fatalf("another identity rule: %v", err)
	}
	if _, err := under(explicitRule(provtest.Subject, "https://other.example", trust.AllowUnsigned)).Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, ErrPinUnderPolicy) {
		t.Fatalf("another issuer, under allow-unsigned: %v", err)
	}
	if _, err := under(&trust.Policy{Modules: []trust.Rule{{Prefix: "example.com/m", Keys: []gitprov.PinnedKey{someKey(t)}}}}).Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, ErrPinUnderPolicy) || !strings.Contains(err.Error(), "pinned keys alone") {
		t.Fatalf("keys alone: %v", err)
	}
	// The origin default, re-derived from the repository the record
	// names: this record was made under an explicit rule for an
	// origin with no default identity, so with the rule gone the
	// default accepts nothing of it.
	if _, err := under(&trust.Policy{Default: trust.RequireProvenance}).Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, ErrPinUnderPolicy) || !strings.Contains(err.Error(), "no default identity") {
		t.Fatalf("the origin default over a record made under an explicit rule: %v", err)
	}
	// And the archive's read re-verifies the evidence under the rule
	// of the day: the same rule accepts, the record reproduced.
	if _, err := under(explicitRule(provtest.Subject, provtest.Issuer, trust.RequireProvenance)).Zip(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("the archive under the same rule: %v", err)
	}
}

// An archive read re-verifies the pin's evidence: the cached envelope
// first, fetched where the cache holds none or what it holds fails;
// evidence no source reproduces refuses the read; a client holds a
// pair once (REQ-prov-pin-held, REQ-lock-no-silent-downgrade).
func TestArchiveReadReverifiesEvidence(t *testing.T) {
	fx := newProvFixture(t, provtest.New(t), sigstoretest.TagOptions{}, "v1.0.0")
	policy := explicitRule(provtest.Subject, provtest.Issuer, trust.RequireProvenance)
	c := fx.clientWithPolicy(policy)
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	v := ver(t, "v1.0.0")
	digest := pinDigest(t, c, "example.com/m", "v1.0.0")
	original, _ := c.Lock.Module("example.com/m", "v1.0.0")
	provKey := proxyHost + "/example.com/m/@v/v1.0.0.prov"
	served := fx.Hits[provKey]

	// The cached envelope tampered: the read fetches a sound one and
	// serves the archive; a second read holds nothing again.
	if err := c.Cache.Put("example.com/m", v, KindProv, digest, []byte("junk")); err != nil {
		t.Fatal(err)
	}
	reader := fx.clientWithPolicy(policy)
	reader.Lock, reader.Cache = c.Lock, c.Cache
	if _, err := reader.Zip(ctx, "example.com/m", v); err != nil {
		t.Fatalf("the archive with a tampered cached envelope: %v", err)
	}
	if fx.Hits[provKey] != served+1 {
		t.Fatalf("the envelope fetched %d times, want once more", fx.Hits[provKey]-served)
	}
	if b, ok, _ := c.Cache.Get("example.com/m", v, KindProv, digest); !ok || string(b) == "junk" {
		t.Fatal("the fetched envelope did not replace the tampered one")
	}
	// Held once for the client's life: the cache tampered again after
	// the hold, a second read looks at nothing and fetches nothing.
	if err := c.Cache.Put("example.com/m", v, KindProv, digest, []byte("junk")); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Zip(ctx, "example.com/m", v); err != nil || fx.Hits[provKey] != served+1 {
		t.Fatalf("a second read: %v, fetches %d", err, fx.Hits[provKey]-served)
	}

	// The record edited under the same client — the lockfile reloaded
	// with another signed object — is held anew: the memo is the
	// record's too, and the evidence reproduces the old record alone.
	for i := range c.Lock.Modules {
		c.Lock.Modules[i].Provenance.Object = strings.Repeat("ab", 20)
	}
	if _, err := reader.Zip(ctx, "example.com/m", v); !errors.Is(err, lockfile.ErrProvenanceDowngrade) || !strings.Contains(err.Error(), "pb dep update example.com/m@v1.0.0") {
		t.Fatalf("a record edited under one client: %v", err)
	}
	for i := range c.Lock.Modules {
		c.Lock.Modules[i].Provenance.Object = original.Provenance.Object
	}
	// No source reproduces the record: another client's read is
	// refused, the archive served to nobody.
	fx.Endpoints[provKey] = []byte("junk")
	refused := fx.clientWithPolicy(policy)
	refused.Lock, refused.Cache = c.Lock, c.Cache
	if _, err := refused.Zip(ctx, "example.com/m", v); !errors.Is(err, provenance.ErrEnvelopeMalformed) {
		t.Fatalf("the archive with no sound envelope anywhere: %v", err)
	}

}

// Evidence served with no trusted root to judge it fails the first
// use under either posture, nothing pinned: a judgement with a
// missing input has no answer, and none would be a fact about the
// machine (the trusted root term).
func TestEvidenceWithoutRootFails(t *testing.T) {
	fx := newProvFixture(t, provtest.New(t), sigstoretest.TagOptions{}, "v1.0.0")
	for _, mode := range []trust.Mode{trust.AllowUnsigned, trust.RequireProvenance} {
		c := fx.Client("proxy")
		c.Policy = explicitRule(provtest.Subject, provtest.Issuer, mode)
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil || !strings.Contains(err.Error(), "no trusted root is configured to judge it") {
			t.Fatalf("%v: %v", mode, err)
		}
		if len(c.Lock.Modules) != 0 {
			t.Fatalf("%v: a pin recorded with the evidence unjudged: %+v", mode, c.Lock.Modules)
		}
	}
}

// someKey is a pinned SSH key of nobody's, for a rule naming keys.
func someKey(t *testing.T) gitprov.PinnedKey {
	t.Helper()
	k, err := gitprov.ParsePinnedKey(gitprov.SSH, sigstoretest.NewSSHKey(t).Public())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// A record made under the origin default is held to it on every use,
// re-derived from the repository the record names: it stands under
// the default, is refused under an explicit rule naming another
// identity, and a record whose repository the default rejects is
// refused once the explicit rule that accepted it is gone
// (REQ-prov-pin-held, REQ-prov-origin-consistency).
func TestDefaultArmPinHeldToTheRecordedRepository(t *testing.T) {
	const (
		repoURL  = "https://github.com/example/m"
		ciSAN    = repoURL + "/.github/workflows/release.yml@refs/tags/v1.0.0"
		ciIssuer = "https://token.actions.githubusercontent.com"
	)
	signer := provtest.NewWithIdentity(t, ciSAN, ciIssuer)
	fx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
	originOverride(fx.Fixture, repoURL)
	c := fx.clientWithPolicy(&trust.Policy{Default: trust.RequireProvenance})
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	pin, _ := c.Lock.Module("example.com/m", "v1.0.0")
	if pin.Provenance.Repo != repoURL {
		t.Fatalf("the record names %q, want the origin's repository", pin.Provenance.Repo)
	}
	under := func(p *trust.Policy) *Client {
		c2 := fx.clientWithPolicy(p)
		c2.Lock, c2.Cache = c.Lock, c.Cache
		return c2
	}
	if _, err := under(&trust.Policy{Default: trust.RequireProvenance}).Zip(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("the default, the archive read: %v", err)
	}
	if _, err := under(explicitRule("https://github.com/other/**", ciIssuer, trust.RequireProvenance)).Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, ErrPinUnderPolicy) {
		t.Fatalf("an explicit rule naming another identity: %v", err)
	}
	// The record's repository edited to another: the default of that
	// repository accepts no workflow of this one.
	moved := c.Lock.Modules
	for i := range moved {
		moved[i].Provenance.Repo = "https://github.com/other/r"
	}
	if _, err := under(&trust.Policy{Default: trust.RequireProvenance}).Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, ErrPinUnderPolicy) || !strings.Contains(err.Error(), "not https://github.com/other/r's own") {
		t.Fatalf("the default of another repository: %v", err)
	}
	// A record naming no repository — written before one was recorded
	// — derives no default: refused, the update named.
	for i := range moved {
		moved[i].Provenance.Repo = ""
	}
	if _, err := under(&trust.Policy{}).Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, ErrPinUnderPolicy) || !strings.Contains(err.Error(), "names no repository") {
		t.Fatalf("a record without a repository under the default: %v", err)
	}
}

// Pinned-key evidence needs no trusted root: an SSH-signed module under
// a rule naming keys beside an identity is accepted and recorded with
// no root configured — only sigstore evidence an identity arm would
// judge fails for the root's absence (the trusted root term).
func TestKeyEvidenceWithoutRootAccepted(t *testing.T) {
	ssh := newSSHFixture(t, "v1.0.0")
	c := ssh.Client("proxy")
	c.Policy = keysRule("", &trust.IdentityRule{SAN: provtest.Subject, Issuer: provtest.Issuer}, ssh.key)
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("key evidence with no root: %v", err)
	}
	pin, _ := c.Lock.Module("example.com/m", "v1.0.0")
	if !lockfile.NamesKey(pin.Provenance) {
		t.Fatalf("recorded %+v, want the pinned key", pin.Provenance)
	}
	// And the archive's read holds it again, root or none.
	if _, err := c.Zip(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("the archive under the key: %v", err)
	}
}
