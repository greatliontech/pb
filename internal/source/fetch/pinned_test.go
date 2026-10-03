package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/gitprov/sigstoretest"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/pb/internal/testing/provtest"
)

// pinnedFixture serves a root module at v1.0.0 whose provenance
// envelope carries one tag signed by an SSH or OpenPGP key over the
// fixture repository's real objects, and the pinned key a policy names
// it by.
type pinnedFixture struct {
	*fixture
	commit plumbing.Hash
	key    gitprov.PinnedKey
}

// newSSHFixture signs the release tag with a fresh SSH key.
func newSSHFixture(t *testing.T, tagName string) *pinnedFixture {
	t.Helper()
	k := sigstoretest.NewSSHKey(t)
	return newPinnedFixture(t, tagName, func(payload []byte) []byte { return k.SignedTag(t, payload, sigstoretest.SSHOptions{}) }, gitprov.SSH, k.Public())
}

// newOpenPGPFixture signs the release tag with a fresh OpenPGP key.
func newOpenPGPFixture(t *testing.T, tagName string) *pinnedFixture {
	t.Helper()
	k := sigstoretest.NewOpenPGPKey(t)
	return newPinnedFixture(t, tagName, func(payload []byte) []byte { return k.SignedTag(t, payload, sigstoretest.OpenPGPOptions{}) }, gitprov.OpenPGP, k.Public(t))
}

func newPinnedFixture(t *testing.T, tagName string, sign func([]byte) []byte, kind gitprov.SignatureKind, public string) *pinnedFixture {
	t.Helper()
	fx, commit := newSignedFixture(t, tagName, sign)
	fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	key, err := gitprov.ParsePinnedKey(kind, public)
	if err != nil {
		t.Fatal(err)
	}
	return &pinnedFixture{fixture: fx, commit: commit, key: key}
}

// newSignedFixture serves a root module at v1.0.0 whose provenance
// envelope carries one tag, signed by sign over the fixture
// repository's real objects, so binding recomputation runs against
// genuine git trees.
func newSignedFixture(t *testing.T, tagName string, sign func([]byte) []byte) (*fixture, plumbing.Hash) {
	t.Helper()
	fx := newFixture(t)
	files := declaredFiles()
	commit := fx.CommitFor(files, gitWhen)
	zip, _ := moduleZip(t, files)
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	env := envelope(t, "sha1", sign(tagPayload(commit, tagName)), fx.Repo.Raw(plumbing.CommitObject, commit), nil)
	fx.Endpoint("example.com/m", "v1.0.0", "prov", string(env))
	return fx, commit
}

// keysRule is a policy whose one rule pins the keys, beside the
// identity where one is given, under the mode.
func keysRule(mode trust.Mode, id *trust.IdentityRule, keys ...gitprov.PinnedKey) *trust.Policy {
	return &trust.Policy{Modules: []trust.Rule{{Prefix: "example.com/m", Require: mode, Identity: id, Keys: keys}}}
}

func (fx *pinnedFixture) clientUnder(p *trust.Policy) *Client {
	c := fx.Client("proxy")
	c.Policy = p
	return c
}

// A signature by a pinned key verifies against exactly the rule's
// keys, no trusted root and no transparency consulted, and is
// recorded with the key's kind and fingerprint
// (REQ-prov-pinned-key-eval, REQ-prov-pinned-key-recorded,
// REQ-lock-pinned-key-record).
func TestPinnedKeyEvidenceAcceptedAndRecorded(t *testing.T) {
	for name, fx := range map[string]*pinnedFixture{
		"ssh":     newSSHFixture(t, "v1.0.0"),
		"openpgp": newOpenPGPFixture(t, "v1.0.0"),
	} {
		t.Run(name, func(t *testing.T) {
			c := fx.clientUnder(keysRule("", nil, fx.key))
			if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
				t.Fatalf("Module: %v", err)
			}
			pin, _ := c.Lock.Module("example.com/m", "v1.0.0")
			rec := pin.Provenance
			if rec.Type != lockfile.ProvenanceGitPinnedKey || rec.ObjectFormat != "sha1" || len(rec.Object) != 40 {
				t.Fatalf("record = %+v", rec)
			}
			if rec.KeyKind != string(fx.key.Kind()) || rec.KeyFingerprint != fx.key.Fingerprint() {
				t.Fatalf("recorded key = %s %s, want %s %s", rec.KeyKind, rec.KeyFingerprint, fx.key.Kind(), fx.key.Fingerprint())
			}
			if rec.SAN != "" || rec.Issuer != "" {
				t.Fatalf("record names an identity: %+v", rec)
			}
			if _, ok, err := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), KindProv, pinDigest(t, c, "example.com/m", "v1.0.0")); err != nil || !ok {
				t.Fatalf("prov not cached: ok=%v err=%v", ok, err)
			}
		})
	}
}

// Naming keys is requiring them, whatever the mode says: a subject
// with no evidence, or with evidence no pinned key made, fails the
// operation under allow-unsigned (REQ-prov-pinned-key-eval).
func TestNamingKeysRequiresThem(t *testing.T) {
	fx := newSSHFixture(t, "v1.0.0")
	other := sigstoretest.NewSSHKey(t)
	otherKey, err := gitprov.ParsePinnedKey(gitprov.SSH, other.Public())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("no evidence", func(t *testing.T) {
		bare := newFixture(t)
		zip, _ := moduleZip(t, declaredFiles())
		bare.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		c := bare.Client("proxy")
		c.Policy = keysRule(trust.AllowUnsigned, nil, fx.key)
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil || !strings.Contains(err.Error(), "requires provenance") {
			t.Fatalf("err = %v, want a require-provenance failure", err)
		}
		if _, ok := c.Lock.Module("example.com/m", "v1.0.0"); ok {
			t.Fatal("a failed evaluation recorded a pin")
		}
	})
	t.Run("a signature by an unpinned key", func(t *testing.T) {
		c := fx.clientUnder(keysRule(trust.AllowUnsigned, nil, otherKey))
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil || !strings.Contains(err.Error(), "by no pinned key") {
			t.Fatalf("err = %v, want a no-pinned-key failure", err)
		}
		if _, ok := c.Lock.Module("example.com/m", "v1.0.0"); ok {
			t.Fatal("a failed evaluation recorded a pin")
		}
	})
	t.Run("a sigstore signature under keys alone", func(t *testing.T) {
		signer := provtest.New(t)
		sfx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
		c := sfx.clientWithPolicy(keysRule(trust.AllowUnsigned, nil, fx.key))
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil || !strings.Contains(err.Error(), "requires provenance") {
			t.Fatalf("err = %v, want a require-provenance failure", err)
		}
	})
}

// An envelope carrying a signature by an unpinned key beside one by a
// pinned key is accepted by the second: an unpinned signer is a
// non-acceptance, skipped, never proof of tampering
// (REQ-prov-pinned-key-eval).
func TestUnpinnedSignerBesideThePinnedOneIsSkipped(t *testing.T) {
	fx := newSSHFixture(t, "v1.0.0")
	other := sigstoretest.NewSSHKey(t)
	rawCommit := fx.Repo.Raw(plumbing.CommitObject, fx.commit)
	stray := envelope(t, "sha1", other.SignedTag(t, tagPayload(fx.commit, "v1.0.0"), sigstoretest.SSHOptions{}), rawCommit, nil)
	fx.Endpoints[proxyHost+"/example.com/m/@v/v1.0.0.prov"] = mergedEnvelopes(t, stray, fx.Endpoints[proxyHost+"/example.com/m/@v/v1.0.0.prov"])
	c := fx.clientUnder(keysRule("", nil, fx.key))
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("Module: %v", err)
	}
	if pin, _ := c.Lock.Module("example.com/m", "v1.0.0"); pin.Provenance.KeyFingerprint != fx.key.Fingerprint() {
		t.Fatalf("record = %+v", pin.Provenance)
	}
}

// mergedEnvelopes is one envelope carrying the evidence of each, in
// order.
func mergedEnvelopes(t *testing.T, docs ...[]byte) []byte {
	t.Helper()
	var evidence []json.RawMessage
	for _, doc := range docs {
		var env struct {
			Evidence []json.RawMessage `json:"evidence"`
		}
		if err := json.Unmarshal(doc, &env); err != nil {
			t.Fatal(err)
		}
		evidence = append(evidence, env.Evidence...)
	}
	out, err := json.Marshal(map[string]any{"formatVersion": 1, "evidence": evidence})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A rule naming both keys and an identity accepts either evidence
// (REQ-prov-pinned-key-eval); a rule naming an identity alone leaves a
// pinned-kind signature absent — unverifiable, recorded none under
// allow-unsigned and a failure under require-provenance
// (REQ-prov-signed-tag).
func TestKeysBesideIdentityAcceptEither(t *testing.T) {
	signer := provtest.New(t)
	id := &trust.IdentityRule{SAN: provtest.Subject, Issuer: provtest.Issuer}
	ssh := newSSHFixture(t, "v1.0.0")

	t.Run("the identity's evidence", func(t *testing.T) {
		sfx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
		c := sfx.clientWithPolicy(keysRule("", id, ssh.key))
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatalf("Module: %v", err)
		}
		if pin, _ := c.Lock.Module("example.com/m", "v1.0.0"); pin.Provenance.Type != lockfile.ProvenanceGitSignedTag {
			t.Fatalf("record = %+v", pin.Provenance)
		}
	})
	t.Run("the key's evidence", func(t *testing.T) {
		c := ssh.clientUnder(keysRule("", id, ssh.key))
		c.TrustedRoot = signer.TrustedRoot()
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatalf("Module: %v", err)
		}
		if pin, _ := c.Lock.Module("example.com/m", "v1.0.0"); pin.Provenance.Type != lockfile.ProvenanceGitPinnedKey {
			t.Fatalf("record = %+v", pin.Provenance)
		}
	})
	t.Run("an identity alone leaves a key's signature absent", func(t *testing.T) {
		c := ssh.clientUnder(keysRule(trust.AllowUnsigned, id))
		c.TrustedRoot = signer.TrustedRoot()
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatalf("Module: %v", err)
		}
		if pin, _ := c.Lock.Module("example.com/m", "v1.0.0"); pin.Provenance != (lockfile.Provenance{}) {
			t.Fatalf("record = %+v, want none", pin.Provenance)
		}
		c2 := ssh.clientUnder(keysRule(trust.RequireProvenance, id))
		c2.TrustedRoot = signer.TrustedRoot()
		if _, err := c2.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil || !strings.Contains(err.Error(), "requires provenance") {
			t.Fatalf("err = %v, want a require-provenance failure", err)
		}
	})
}

// Binding holds under pinned keys as under an identity: a signed tag
// naming another version is rejected, never ignored
// (REQ-prov-tag-binding); so is a signature by a pinned key over other
// bytes than the tag's, an unsigned tag served as evidence, and a
// signature a pinned key made in a form the verifier refuses — each
// tampering or evidence the rule cannot take, never skipped
// (REQ-prov-pinned-key-eval).
func TestPinnedKeyRejectionsAbort(t *testing.T) {
	k := sigstoretest.NewSSHKey(t)
	key, err := gitprov.ParsePinnedKey(gitprov.SSH, k.Public())
	if err != nil {
		t.Fatal(err)
	}
	sign := func(payload []byte) []byte { return k.SignedTag(t, payload, sigstoretest.SSHOptions{}) }
	for name, sign := range map[string]func([]byte) []byte{
		"a tag naming another version": func(payload []byte) []byte {
			return sign(append([]byte(nil), []byte(strings.Replace(string(payload), "tag v1.0.0", "tag v2.0.0", 1))...))
		},
		"a signature over other bytes": func(payload []byte) []byte {
			// Signed as written, then the message altered: the signature
			// no longer covers the bytes in hand.
			raw := sign(payload)
			return []byte(strings.Replace(string(raw), "release v1.0.0", "release v1.0.1", 1))
		},
		"an unsigned tag": func(payload []byte) []byte { return payload },
	} {
		t.Run(name, func(t *testing.T) {
			fx, _ := newSignedFixture(t, "v1.0.0", sign)
			c := fx.Client("proxy")
			c.Policy = keysRule("", nil, key)
			if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil || !strings.Contains(err.Error(), "evidence rejected") {
				t.Fatalf("err = %v, want a rejection", err)
			}
			if _, ok := c.Lock.Module("example.com/m", "v1.0.0"); ok {
				t.Fatal("a rejection recorded a pin")
			}
		})
	}
	t.Run("a rejection beside accepted sigstore evidence still aborts", func(t *testing.T) {
		signer := provtest.New(t)
		fx, commit := newSignedFixture(t, "v1.0.0", func(payload []byte) []byte { return signer.SignedTag(t, payload, sigstoretest.TagOptions{}) })
		raw := []byte(strings.Replace(string(sign(tagPayload(commit, "v1.0.0"))), "release v1.0.0", "release v1.0.1", 1))
		stray := envelope(t, "sha1", raw, fx.Repo.Raw(plumbing.CommitObject, commit), nil)
		fx.Endpoints[proxyHost+"/example.com/m/@v/v1.0.0.prov"] = mergedEnvelopes(t, stray, fx.Endpoints[proxyHost+"/example.com/m/@v/v1.0.0.prov"])
		c := fx.Client("proxy")
		c.Policy = keysRule("", &trust.IdentityRule{SAN: provtest.Subject, Issuer: provtest.Issuer}, key)
		c.TrustedRoot = signer.TrustedRoot()
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil || !strings.Contains(err.Error(), "evidence rejected") {
			t.Fatalf("err = %v, want a rejection", err)
		}
	})
}

// Under keys beside an identity with no trusted root configured, a
// sigstore signature is unverifiable — absent — and nothing else is
// accepted, so the operation fails as a requirement, naming the
// reason, never reading a chain against no root.
func TestSigstoreEvidenceUnderKeysWithoutRootIsAbsent(t *testing.T) {
	signer := provtest.New(t)
	ssh := newSSHFixture(t, "v1.0.0")
	sfx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
	c := sfx.Client("proxy")
	c.Policy = keysRule("", &trust.IdentityRule{SAN: provtest.Subject, Issuer: provtest.Issuer}, ssh.key)
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil || !strings.Contains(err.Error(), "requires provenance") || !strings.Contains(err.Error(), "trusted root") {
		t.Fatalf("err = %v, want a require-provenance failure naming the root", err)
	}
}

// A pinned-key record re-verifies on download against the key it
// names, which the policy of the day must still pin: a policy that no
// longer pins that key fails the download naming it, never rewriting
// the record (REQ-prov-pinned-key-recorded,
// REQ-lock-no-silent-downgrade).
func TestPinnedKeyRecordReverified(t *testing.T) {
	fx := newSSHFixture(t, "v1.0.0")
	other := sigstoretest.NewSSHKey(t)
	otherKey, err := gitprov.ParsePinnedKey(gitprov.SSH, other.Public())
	if err != nil {
		t.Fatal(err)
	}
	c := fx.clientUnder(keysRule("", nil, fx.key))
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}

	t.Run("the policy still pins the key", func(t *testing.T) {
		c2 := fx.clientUnder(keysRule("", nil, otherKey, fx.key))
		c2.Lock = c.Lock
		if err := c2.Download(context.Background(), "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatalf("Download: %v", err)
		}
	})
	t.Run("the policy pins another key", func(t *testing.T) {
		c2 := fx.clientUnder(keysRule("", nil, otherKey))
		c2.Lock = c.Lock
		err := c2.Download(context.Background(), "example.com/m", ver(t, "v1.0.0"))
		if !errors.Is(err, lockfile.ErrProvenanceDowngrade) || !strings.Contains(err.Error(), fx.key.Fingerprint()) {
			t.Fatalf("err = %v, want a downgrade naming the recorded key", err)
		}
	})
	t.Run("the policy pins no key", func(t *testing.T) {
		c2 := fx.clientUnder(&trust.Policy{})
		c2.Lock = c.Lock
		if err := c2.Download(context.Background(), "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, lockfile.ErrProvenanceDowngrade) {
			t.Fatalf("err = %v, want a downgrade", err)
		}
	})
	t.Run("no policy at all", func(t *testing.T) {
		c2 := fx.Client("proxy")
		c2.Lock = c.Lock
		if err := c2.Download(context.Background(), "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, lockfile.ErrProvenanceDowngrade) || !strings.Contains(err.Error(), fx.key.Fingerprint()) {
			t.Fatalf("err = %v, want a downgrade naming the recorded key", err)
		}
	})
	// A later resolution, not a download alone, is held to the policy
	// of the day (REQ-prov-pinned-key-recorded): the module file and
	// the archive of a pin whose key the policy dropped are refused.
	t.Run("a later resolution under another key", func(t *testing.T) {
		c2 := fx.clientUnder(keysRule("", nil, otherKey))
		c2.Lock = c.Lock
		if _, err := c2.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, lockfile.ErrProvenanceDowngrade) || !strings.Contains(err.Error(), fx.key.Fingerprint()) {
			t.Fatalf("Module: err = %v, want a downgrade naming the recorded key", err)
		}
		if _, err := c2.Zip(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, lockfile.ErrProvenanceDowngrade) {
			t.Fatalf("Zip: err = %v, want a downgrade", err)
		}
	})
	t.Run("a later resolution under the key still pinned", func(t *testing.T) {
		c2 := fx.clientUnder(keysRule("", nil, fx.key))
		c2.Lock = c.Lock
		if _, err := c2.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatalf("Module: %v", err)
		}
	})
}

// The origin renders a pinned-key signature's pack as any other's, and
// the direct source verifies it under the rule's keys
// (REQ-proxy-direct-equivalence).
func TestPinnedKeyEvidenceThroughTheOrigin(t *testing.T) {
	k := sigstoretest.NewSSHKey(t)
	key, err := gitprov.ParsePinnedKey(gitprov.SSH, k.Public())
	if err != nil {
		t.Fatal(err)
	}
	fx := newFixture(t)
	tree := fx.TreeFor(fx.Repo, map[string]string{"pb.yaml": "module: example.com/m\n", "a.proto": "syntax = \"proto3\";\n"})
	commit := fx.Repo.CommitTree(tree, "release", gitWhen)
	tag := fx.Repo.TagObject(k.SignedTag(t, tagPayload(commit, "v1.0.0"), sigstoretest.SSHOptions{}))
	fx.Repo.Ref("refs/tags/v1.0.0", tag)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")
	c := fx.Client("direct")
	c.Policy = keysRule("", nil, key)
	if mf, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil || mf.Module != "example.com/m" {
		t.Fatalf("Module: %+v, %v", mf, err)
	}
	if pin, _ := c.Lock.Module("example.com/m", "v1.0.0"); pin.Provenance.KeyFingerprint != key.Fingerprint() {
		t.Fatalf("the pin's provenance: %+v", pin.Provenance)
	}
}
