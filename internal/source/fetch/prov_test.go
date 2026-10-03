package fetch

import (
	"context"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/greatliontech/gitprov/sigstoretest"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/pb/internal/source/origin"
	"github.com/greatliontech/pb/internal/testing/provtest"
)

// provFixture serves a root module at v1.0.0 whose provenance envelope
// carries one signed tag over the fixture repository's real objects,
// so binding recomputation runs against genuine git trees.
type provFixture struct {
	*fixture
	signer *provtest.Signer
	commit plumbing.Hash
}

func newProvFixture(t *testing.T, signer *provtest.Signer, o sigstoretest.TagOptions, tagName string) *provFixture {
	fx, commit := newSignedFixture(t, tagName, func(payload []byte) []byte { return signer.SignedTag(t, payload, o) })
	return &provFixture{fixture: fx, signer: signer, commit: commit}
}

func (fx *provFixture) clientWithPolicy(p *trust.Policy) *Client {
	c := fx.Client("proxy")
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
	fx := newProvFixture(t, provtest.New(t), sigstoretest.TagOptions{}, "v1.0.0")
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
	if _, ok, err := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), KindProv, pinDigest(t, c, "example.com/m", "v1.0.0")); err != nil || !ok {
		t.Fatalf("prov not cached: ok=%v err=%v", ok, err)
	}
}

// A subject governed by require-provenance with no evidence at any
// source fails the operation (REQ-prov-policy-eval).
func TestRequireProvenanceNoEvidenceFails(t *testing.T) {
	fx := newFixture(t)
	zip, _ := moduleZip(t, declaredFiles())
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	c := fx.Client("proxy")
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
		fx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
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
		fx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
		c := fx.clientWithPolicy(explicitRule("someone-else@example.com", provtest.Issuer, trust.RequireProvenance))
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil {
			t.Fatal("an unaccepted identity satisfied require-provenance")
		}
	})
}

// Evidence with no embedded transparency proof is unverifiable and
// treated as absent (REQ-prov-signed-tag): none under allow-unsigned.
func TestNoTransparencyTreatedAsAbsent(t *testing.T) {
	fx := newProvFixture(t, provtest.New(t), sigstoretest.TagOptions{NoEntry: true}, "v1.0.0")
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
	fx := newProvFixture(t, provtest.New(t), sigstoretest.TagOptions{}, "v2.0.0")
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
	fx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
	originOverride(fx.Fixture, repoURL)
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
		fx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
		c := fx.clientWithPolicy(&trust.Policy{Default: trust.RequireProvenance})
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil {
			t.Fatal("a forge with no known CI issuer produced an acceptable identity")
		}
	})

	t.Run("allow-unsigned records none", func(t *testing.T) {
		fx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
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
	fx.ResolveOverride = func(_ context.Context, modPath string) (origin.Origin, error) {
		return origin.Origin{Repo: repoURL, Subtree: fx.Subtrees[modPath]}, nil
	}
}

// A synthesized subtree's release is the repository's tag, and its
// evidence is the repository's signed tag with the tree path to the
// subtree (REQ-prov-tag-binding, REQ-resolve-release-tags): a proxy
// serving that pack verifies and the pin records it; a declared
// subtree's evidence is its own tag's, the repository's tag over the
// same commit naming no version of it and rejected as tampering.
func TestSubtreeEvidenceBindsToTheNamingTag(t *testing.T) {
	signer := provtest.New(t)
	synthesized := map[string]string{"pb.yaml": "module: example.com/m\n", "sub/s.proto": "syntax = \"proto3\";\n"}
	declared := map[string]string{"pb.yaml": "module: example.com/m\n", "sub/pb.yaml": "module: example.com/m/sub\n", "sub/s.proto": "syntax = \"proto3\";\n"}
	for _, tc := range []struct {
		name    string
		files   map[string]string
		tagName string
		tamper  func(zip map[string]string) // the archive a proxy serves, altered
		want    string                      // "" for accepted, else the rejection's text
	}{
		{"synthesized, the repository's tag", synthesized, "v1.0.0", nil, ""},
		{"declared, its own tag", declared, "sub/v1.0.0", nil, ""},
		{"declared, the repository's tag", declared, "v1.0.0", nil, "does not name"},
		// The archive decides the namespace and is itself bound: a module
		// file stripped or added changes the subtree's tree, so the tag
		// the altered archive points at never binds.
		{"declared, the module file stripped, the repository's tag", declared, "v1.0.0", func(z map[string]string) { delete(z, "pb.yaml") }, "git tree mismatch"},
		{"synthesized, a module file added, the subtree's tag", synthesized, "sub/v1.0.0", func(z map[string]string) { z["pb.yaml"] = "module: example.com/m/sub\n" }, "git tree mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t)
			tree := fx.TreeFor(fx.Repo, tc.files)
			commit := fx.Repo.CommitTree(tree, "release", gitWhen)
			subFiles := map[string]string{}
			for p, text := range tc.files {
				if rest, ok := strings.CutPrefix(p, "sub/"); ok {
					subFiles[rest] = text
				}
			}
			if tc.tamper != nil {
				tc.tamper(subFiles)
			}
			zip, _ := moduleZip(t, subFiles)
			fx.Endpoint("example.com/m/sub", "v1.0.0", "zip", string(zip))
			if mod, ok := subFiles["pb.yaml"]; ok {
				fx.Endpoint("example.com/m/sub", "v1.0.0", "mod", mod)
			}
			tag := signer.SignedTag(t, tagPayload(commit, tc.tagName), sigstoretest.TagOptions{})
			env := envelope(t, "sha1", tag, fx.Repo.Raw(plumbing.CommitObject, commit), [][]byte{fx.Repo.Raw(plumbing.TreeObject, tree)})
			fx.Endpoint("example.com/m/sub", "v1.0.0", "prov", string(env))
			fx.Subtrees["example.com/m/sub"] = "sub"
			c := fx.Client("proxy")
			c.Policy = &trust.Policy{Modules: []trust.Rule{{Prefix: "example.com/m", Require: trust.RequireProvenance, Identity: &trust.IdentityRule{SAN: provtest.Subject, Issuer: provtest.Issuer}}}}
			c.TrustedRoot = signer.TrustedRoot()
			_, err := c.Module(ctx, "example.com/m/sub", ver(t, "v1.0.0"))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Module: %v", err)
				}
				if pin, _ := c.Lock.Module("example.com/m/sub", "v1.0.0"); pin.Provenance.SAN != provtest.Subject {
					t.Fatalf("the pin's provenance: %+v", pin.Provenance)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a rejection at %q: %v", tc.want, err)
			}
		})
	}
}

// Through the origin, a synthesized subtree's release under the
// repository's signed tag verifies and is pinned: the pack the direct
// source renders carries that tag with the tree path to the subtree
// (REQ-prov-tag-binding, REQ-proxy-direct-equivalence).
func TestSynthesizedSubtreeEvidenceThroughTheOrigin(t *testing.T) {
	signer := provtest.New(t)
	fx := newFixture(t)
	tree := fx.TreeFor(fx.Repo, map[string]string{"pb.yaml": "module: example.com/m\n", "sub/s.proto": "syntax = \"proto3\";\n"})
	commit := fx.Repo.CommitTree(tree, "release", gitWhen)
	tag := fx.Repo.TagObject(signer.SignedTag(t, tagPayload(commit, "v1.0.0"), sigstoretest.TagOptions{}))
	fx.Repo.Ref("refs/tags/v1.0.0", tag)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")
	fx.Subtrees["example.com/m/sub"] = "sub"
	c := fx.Client("direct")
	c.Policy = &trust.Policy{Modules: []trust.Rule{{Prefix: "example.com/m", Require: trust.RequireProvenance, Identity: &trust.IdentityRule{SAN: provtest.Subject, Issuer: provtest.Issuer}}}}
	c.TrustedRoot = signer.TrustedRoot()
	if mf, err := c.Module(ctx, "example.com/m/sub", ver(t, "v1.0.0")); err != nil || mf.Module != "example.com/m/sub" {
		t.Fatalf("Module: %+v, %v", mf, err)
	}
	if pin, _ := c.Lock.Module("example.com/m/sub", "v1.0.0"); pin.Provenance.SAN != provtest.Subject {
		t.Fatalf("the pin's provenance: %+v", pin.Provenance)
	}
}

// The ssh setting routes transport alone: an ssh-routed module's
// default identity is its HTTPS repository's, so its provenance
// verdict and its record are the same whichever route reached it
// (REQ-resolve-ssh, REQ-prov-origin-consistency).
func TestDefaultIdentityUnchangedBySSHRouting(t *testing.T) {
	const (
		repoURL  = "https://github.com/example/m"
		ciSAN    = repoURL + "/.github/workflows/release.yml@refs/tags/v1.0.0"
		ciIssuer = "https://token.actions.githubusercontent.com"
	)
	signer := provtest.NewWithIdentity(t, ciSAN, ciIssuer)
	fx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
	fx.ResolveOverride = func(_ context.Context, modPath string) (origin.Origin, error) {
		return origin.Origin{Repo: repoURL, SSH: true, Subtree: fx.Subtrees[modPath]}, nil
	}
	c := fx.clientWithPolicy(&trust.Policy{Default: trust.RequireProvenance})
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("Module over an ssh-routed origin: %v", err)
	}
	pin, _ := c.Lock.Module("example.com/m", "v1.0.0")
	if pin.Provenance.SAN != ciSAN || pin.Provenance.Issuer != ciIssuer {
		t.Fatalf("recorded identity = %q/%q", pin.Provenance.SAN, pin.Provenance.Issuer)
	}
}

// A pinned subtree module's record names the subtree the binding was
// verified under, and re-verification binds there: a redirect that
// moves the module's path elsewhere in the repository later changes
// no attested fact, so the unchanged archive and evidence still
// reproduce the record (REQ-lock-provenance-record,
// REQ-prov-tag-binding).
func TestReverificationBindsUnderTheRecordedSubtree(t *testing.T) {
	signer := provtest.New(t)
	fx := newFixture(t)
	files := map[string]string{"pb.yaml": "module: example.com/m\n", "sub/pb.yaml": "module: example.com/m/sub\n", "sub/s.proto": "syntax = \"proto3\";\n"}
	tree := fx.TreeFor(fx.Repo, files)
	commit := fx.Repo.CommitTree(tree, "release", gitWhen)
	zip, _ := moduleZip(t, map[string]string{"pb.yaml": "module: example.com/m/sub\n", "s.proto": "syntax = \"proto3\";\n"})
	fx.Endpoint("example.com/m/sub", "v1.0.0", "zip", string(zip))
	fx.Endpoint("example.com/m/sub", "v1.0.0", "mod", "module: example.com/m/sub\n")
	fx.Endpoint("example.com/m/sub", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	tag := signer.SignedTag(t, tagPayload(commit, "sub/v1.0.0"), sigstoretest.TagOptions{})
	env := envelope(t, "sha1", tag, fx.Repo.Raw(plumbing.CommitObject, commit), [][]byte{fx.Repo.Raw(plumbing.TreeObject, tree)})
	fx.Endpoint("example.com/m/sub", "v1.0.0", "prov", string(env))
	fx.Subtrees["example.com/m/sub"] = "sub"
	policy := &trust.Policy{Modules: []trust.Rule{{Prefix: "example.com/m", Require: trust.RequireProvenance, Identity: &trust.IdentityRule{SAN: provtest.Subject, Issuer: provtest.Issuer}}}}
	c := fx.Client("proxy")
	c.Policy, c.TrustedRoot = policy, signer.TrustedRoot()
	if _, err := c.Module(ctx, "example.com/m/sub", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("first use: %v", err)
	}
	pin, _ := c.Lock.Module("example.com/m/sub", "v1.0.0")
	if pin.Provenance.Subtree != "sub" {
		t.Fatalf("the record's subtree: %+v", pin.Provenance)
	}
	// The module's path now resolves elsewhere; the pinned record is
	// re-verified from the served evidence (a fresh cache holds no
	// envelope) where it was bound.
	fx.Subtrees["example.com/m/sub"] = "moved"
	again := fx.Client("proxy")
	again.Policy, again.TrustedRoot, again.Lock = policy, signer.TrustedRoot(), c.Lock
	if err := again.Download(ctx, "example.com/m/sub", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("re-verification after the redirect moved: %v", err)
	}
}
