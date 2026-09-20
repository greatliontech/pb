package fetch

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/greatliontech/gitprov/sigstoretest"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/pb/internal/testing/provtest"
)

// Cached info entries are validated like fetched ones: junk at the
// entry is discarded as absent and healed from sources, never kept
// (REQ-dep-cache-transparent).
func TestDownloadHealsCorruptCachedInfo(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	zip, _ := moduleZip(t, files)
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	fx.Endpoint("example.com/m", "v1.0.0", "mod", files["pb.yaml"])
	fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	c := fx.Client("proxy")
	if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	if err := c.Cache.Put("example.com/m", ver(t, "v1.0.0"), KindInfo, []byte("not json at all")); err != nil {
		t.Fatal(err)
	}
	if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("Download over corrupt cached info: %v", err)
	}
	b, ok, err := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), KindInfo)
	if err != nil || !ok || string(b) != `{"version":"v1.0.0"}` {
		t.Fatalf("cached info after heal = %q, %v, %v", b, ok, err)
	}
}

// Download's re-verification classifies evidence exactly as first use:
// a cached envelope failing it is local state, discarded and refetched
// (REQ-dep-cache-transparent); a served envelope carrying tampered
// evidence aborts (REQ-prov-tag-binding: rejected, not ignored) rather
// than being ignored because the pin already exists.
func TestDownloadReverifiesEvidence(t *testing.T) {
	signer := provtest.New(t)
	fx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
	fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	fx.Endpoint("example.com/m", "v1.0.0", "mod", declaredFiles()["pb.yaml"])
	c := fx.clientWithPolicy(explicitRule(provtest.Subject, provtest.Issuer, trust.RequireProvenance))
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}

	t.Run("corrupt cached envelope heals from sources", func(t *testing.T) {
		if err := c.Cache.Put("example.com/m", ver(t, "v1.0.0"), KindProv, []byte("junk")); err != nil {
			t.Fatal(err)
		}
		if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatalf("Download over corrupt cached prov: %v", err)
		}
		b, ok, _ := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), KindProv)
		if !ok || string(b) == "junk" {
			t.Fatal("corrupt cached envelope survived Download")
		}
	})

	t.Run("served tampered evidence aborts", func(t *testing.T) {
		misbound := signer.SignedTag(t, tagPayload(fx.commit, "v9.9.9"), sigstoretest.TagOptions{})
		env := envelope(t, "sha1", misbound, fx.Repo.Raw(plumbing.CommitObject, fx.commit), nil)
		fx.Endpoints[proxyHost+"/example.com/m/@v/v1.0.0.prov"] = env
		c2 := fx.clientWithPolicy(explicitRule(provtest.Subject, provtest.Issuer, trust.RequireProvenance))
		c2.Lock = c.Lock
		err := c2.Download(ctx, "example.com/m", ver(t, "v1.0.0"))
		if err == nil || !strings.Contains(err.Error(), "rejected") {
			t.Fatalf("err = %v, want a rejection", err)
		}
	})
}

// A pin/archive disagreement convicts through Download's module-file
// arm even when no source serves a standalone copy: the archive
// extraction error propagates rather than an empty entry being cached.
func TestDownloadConvictsBogusModfilePin(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	zip, digest := moduleZip(t, files)
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	c := fx.Client("proxy")
	if err := c.Lock.AddModule(lockfile.ModulePin{
		Path: "example.com/m", Version: "v1.0.0", Digest: digest,
		Modfile: "sha256:" + strings.Repeat("0", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, lockfile.ErrPinMismatch) {
		t.Fatalf("err = %v, want ErrPinMismatch", err)
	}
	if _, ok, _ := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), KindMod); ok {
		t.Fatal("a failed conviction cached a module-file entry")
	}
}
