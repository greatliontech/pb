package modfetch

import (
	"context"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/greatliontech/pb/internal/lockfile"
	"github.com/greatliontech/pb/internal/origin"
	"github.com/greatliontech/pb/internal/provtest"
	"github.com/greatliontech/pb/internal/trust"
)

// provFixture serves a root module at v1.0.0 whose provenance envelope
// carries one signed tag over the fixture repository's real objects,
// so binding recomputation runs against genuine git trees.
type provFixture struct {
	*fixture
	signer *provtest.Signer
	commit plumbing.Hash
}

func newProvFixture(t *testing.T, signer *provtest.Signer, embedProof bool, tagName string) *provFixture {
	fx := newFixture(t)
	files := declaredFiles()
	commit := fx.commitFor(files, gitWhen)
	zip, _ := moduleZip(t, files)
	fx.endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	tag := signer.SignedTag(t, tagPayload(commit, tagName), embedProof)
	env := envelope(t, "sha1", tag, fx.repo.Raw(plumbing.CommitObject, commit), nil)
	fx.endpoint("example.com/m", "v1.0.0", "prov", string(env))
	return &provFixture{fixture: fx, signer: signer, commit: commit}
}

func (fx *provFixture) clientWithPolicy(p *trust.Policy) *Client {
	c := fx.client("proxy")
	c.Policy = p
	c.TrustedRoot = fx.signer.TrustedRoot()
	return c
}

func explicitRule(san, issuer string, mode trust.Mode) *trust.Policy {
	return &trust.Policy{Modules: []trust.Rule{{
		Prefix:   "example.com/m",
		Require:  mode,
		Identity: &trust.IdentityRule{SAN: san, Issuer: issuer},
	}}}
}

// Accepted evidence is recorded in the first-use pin
// (REQ-prov-signed-tag, REQ-prov-tag-binding, REQ-lock-provenance-
// record): type, object format, the signed tag's own hash, and the
// verified identity; the envelope is cached alongside the artifacts.
func TestAcceptedEvidenceRecorded(t *testing.T) {
	fx := newProvFixture(t, provtest.New(t), true, "v1.0.0")
	c := fx.clientWithPolicy(explicitRule(provtest.Subject, provtest.Issuer, trust.RequireProvenance))

	mf, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0"))
	if err != nil {
		t.Fatalf("Module: %v", err)
	}
	if mf == nil || mf.Module != "example.com/m" {
		t.Fatalf("Module = %+v", mf)
	}
	pin, _ := c.Lock.Module("example.com/m", "v1.0.0")
	rec := pin.Provenance
	if rec.Type != "git-signed-tag" || rec.ObjectFormat != "sha1" {
		t.Fatalf("record = %+v", rec)
	}
	if rec.SAN != provtest.Subject || rec.Issuer != provtest.Issuer {
		t.Fatalf("recorded identity = %q/%q", rec.SAN, rec.Issuer)
	}
	if len(rec.Object) != 40 {
		t.Fatalf("recorded object = %q, want a sha1 hex hash", rec.Object)
	}
	if _, ok, err := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), KindProv); err != nil || !ok {
		t.Fatalf("prov not cached: ok=%v err=%v", ok, err)
	}
}

// A subject governed by require-provenance with no evidence at any
// source fails the operation (REQ-prov-policy-eval).
func TestRequireProvenanceNoEvidenceFails(t *testing.T) {
	fx := newFixture(t)
	zip, _ := moduleZip(t, declaredFiles())
	fx.endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	c := fx.client("proxy")
	c.Policy = &trust.Policy{Default: trust.RequireProvenance}

	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
		!strings.Contains(err.Error(), "requires provenance") {
		t.Fatalf("err = %v, want a require-provenance failure", err)
	}
	if _, ok := c.Lock.Module("example.com/m", "v1.0.0"); ok {
		t.Fatal("a failed policy evaluation recorded a pin")
	}
}

// A valid signature by a signer the policy does not accept is a
// non-acceptance, not tampering: recorded none under allow-unsigned
// (REQ-prov-unsigned-recorded), a failure under require-provenance
// (REQ-prov-policy-eval).
func TestUnacceptedIdentityClassifiedAsUnsigned(t *testing.T) {
	signer := provtest.New(t)

	t.Run("allow-unsigned records none", func(t *testing.T) {
		fx := newProvFixture(t, signer, true, "v1.0.0")
		c := fx.clientWithPolicy(explicitRule("someone-else@example.com", provtest.Issuer, trust.AllowUnsigned))
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatalf("Module: %v", err)
		}
		pin, _ := c.Lock.Module("example.com/m", "v1.0.0")
		if pin.Provenance != (lockfile.Provenance{}) {
			t.Fatalf("provenance = %+v, want none", pin.Provenance)
		}
	})

	t.Run("require-provenance fails", func(t *testing.T) {
		fx := newProvFixture(t, signer, true, "v1.0.0")
		c := fx.clientWithPolicy(explicitRule("someone-else@example.com", provtest.Issuer, trust.RequireProvenance))
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil {
			t.Fatal("an unaccepted identity satisfied require-provenance")
		}
	})
}

// Evidence with no embedded transparency proof is unverifiable and
// treated as absent (REQ-prov-signed-tag): none under allow-unsigned.
func TestNoTransparencyTreatedAsAbsent(t *testing.T) {
	fx := newProvFixture(t, provtest.New(t), false, "v1.0.0")
	c := fx.clientWithPolicy(explicitRule(provtest.Subject, provtest.Issuer, trust.AllowUnsigned))
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("Module: %v", err)
	}
	pin, _ := c.Lock.Module("example.com/m", "v1.0.0")
	if pin.Provenance != (lockfile.Provenance{}) {
		t.Fatalf("provenance = %+v, want none", pin.Provenance)
	}
}

// Evidence failing a binding step — here a signed tag naming a version
// other than the one being resolved — is rejected, not ignored
// (REQ-prov-tag-binding): the operation fails even under
// allow-unsigned, and nothing is pinned.
func TestMisboundEvidenceRejectedEvenWhenUnrequired(t *testing.T) {
	fx := newProvFixture(t, provtest.New(t), true, "v2.0.0")
	c := fx.clientWithPolicy(explicitRule(provtest.Subject, provtest.Issuer, trust.AllowUnsigned))
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
		!strings.Contains(err.Error(), "rejected") {
		t.Fatalf("err = %v, want a rejection", err)
	}
	if _, ok := c.Lock.Module("example.com/m", "v1.0.0"); ok {
		t.Fatal("rejected evidence recorded a pin")
	}
}

// Absent an explicit rule, the accepted identity must verifiably
// designate the module's own origin (REQ-prov-origin-consistency): a CI
// workflow identity strictly under the origin repository's URL, issued
// by the forge's CI issuer.
func TestDefaultIdentityAcceptsOriginWorkflow(t *testing.T) {
	const (
		repoURL  = "https://github.com/example/m"
		ciSAN    = repoURL + "/.github/workflows/release.yml@refs/tags/v1.0.0"
		ciIssuer = "https://token.actions.githubusercontent.com"
	)
	signer := provtest.NewWithIdentity(t, ciSAN, ciIssuer)
	fx := newProvFixture(t, signer, true, "v1.0.0")
	fx.originOverride(repoURL)
	c := fx.clientWithPolicy(&trust.Policy{Default: trust.RequireProvenance})

	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("Module: %v", err)
	}
	pin, _ := c.Lock.Module("example.com/m", "v1.0.0")
	if pin.Provenance.SAN != ciSAN || pin.Provenance.Issuer != ciIssuer {
		t.Fatalf("recorded identity = %q/%q", pin.Provenance.SAN, pin.Provenance.Issuer)
	}
}

// An origin on a forge with no known CI issuer has no default identity
// at all (REQ-prov-origin-consistency): evidence is unacceptable by
// default — require-provenance fails, allow-unsigned records none.
func TestDefaultIdentityUnknownForge(t *testing.T) {
	signer := provtest.New(t)

	t.Run("require-provenance fails", func(t *testing.T) {
		fx := newProvFixture(t, signer, true, "v1.0.0")
		c := fx.clientWithPolicy(&trust.Policy{Default: trust.RequireProvenance})
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil {
			t.Fatal("a forge with no known CI issuer produced an acceptable identity")
		}
	})

	t.Run("allow-unsigned records none", func(t *testing.T) {
		fx := newProvFixture(t, signer, true, "v1.0.0")
		c := fx.clientWithPolicy(nil)
		c.TrustedRoot = signer.TrustedRoot()
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatalf("Module: %v", err)
		}
		pin, _ := c.Lock.Module("example.com/m", "v1.0.0")
		if pin.Provenance != (lockfile.Provenance{}) {
			t.Fatalf("provenance = %+v, want none", pin.Provenance)
		}
	})
}

// originOverride points every module path at the given repository URL
// (identity derivation input) while keeping subtree resolution.
func (fx *fixture) originOverride(repoURL string) {
	fx.resolveOverride = func(_ context.Context, modPath string) (origin.Origin, error) {
		return origin.Origin{Repo: repoURL, Subtree: fx.subtrees[modPath]}, nil
	}
}
