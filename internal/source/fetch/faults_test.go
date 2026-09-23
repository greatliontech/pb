package fetch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/greatliontech/gitprov/sigstoretest"
	"github.com/greatliontech/pb/internal/module/archive"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/provenance"
	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/pb/internal/source/direct"
	"github.com/greatliontech/pb/internal/source/origin"
	"github.com/greatliontech/pb/internal/source/proxy"
	"github.com/greatliontech/pb/internal/testing/fetchtest"
	"github.com/greatliontech/pb/internal/testing/provtest"
)

// The fault-injection filesystem lives in internal/testing/fetchtest; local
// aliases keep this suite reading naturally.
type errFS = fetchtest.ErrFS

var errInjected = fetchtest.ErrInjected

// Every cache storage fault surfaces as the operation's error — no
// arm of the pipeline swallows a failed read or write.
func TestCacheStorageFaultsSurface(t *testing.T) {
	files := declaredFiles()

	// First-use writes: each write-path fault fails Module.
	for _, tc := range []struct {
		name string
		fs   errFS
	}{
		{"mkdirall", errFS{FailMkdirAll: true, PutFailAfter: -1}},
		{"create", errFS{FailCreate: true, PutFailAfter: -1}},
		{"write", errFS{FailWrite: true, PutFailAfter: -1}},
		{"close", errFS{FailClose: true, PutFailAfter: -1}},
		{"rename", errFS{FailRename: true, PutFailAfter: -1}},
		{"second put (module file)", errFS{PutFailAfter: 1}},
	} {
		t.Run("first-use "+tc.name, func(t *testing.T) {
			fx := newFixture(t)
			zip, _ := moduleZip(t, files)
			fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
			c := fx.Client("proxy")
			fs := tc.fs
			fs.Filesystem = memfs.New()
			c.Cache = &Cache{FS: &fs}
			if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
				t.Fatalf("err = %v, want the injected fault", err)
			}
		})
	}

	// Pinned reads: a failing Open is a cache error, not absence.
	for _, synthesized := range []bool{false, true} {
		name := "pinned read declared"
		mod := files
		if synthesized {
			name = "pinned read synthesized"
			mod = map[string]string{"a.proto": "syntax = \"proto3\";\n"}
		}
		t.Run(name, func(t *testing.T) {
			fx := newFixture(t)
			zip, _ := moduleZip(t, mod)
			fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
			if !synthesized {
				fx.Endpoints[proxyHost+"/example.com/m/@v/v1.0.0.zip"], _ = moduleZip(t, map[string]string{
					"pb.yaml":       "module: example.com/m\ndeps:\n  example.com/dep: v1.2.0\n",
					"proto/a.proto": "syntax = \"proto3\";\n",
				})
			}
			seed := fx.Client("proxy")
			if _, err := seed.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
				t.Fatal(err)
			}
			c := fx.Client("proxy")
			c.Lock = seed.Lock
			c.Cache = &Cache{FS: &errFS{Filesystem: memfs.New(), FailOpen: true, PutFailAfter: -1}}
			if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
				t.Fatalf("err = %v, want the injected fault", err)
			}
		})
	}

	// Download's later writes: info and provenance cache writes fail
	// the operation too.
	t.Run("download info put", func(t *testing.T) {
		fx := newFixture(t)
		zip, _ := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		fx.Endpoint("example.com/m", "v1.0.0", "mod", files["pb.yaml"])
		fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0"}`)
		c := fx.Client("proxy")
		// zip + mod cache writes succeed; the info write is the third.
		c.Cache = &Cache{FS: &errFS{Filesystem: memfs.New(), PutFailAfter: 2}}
		if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v, want the injected fault", err)
		}
	})
}

// A source serving bytes that are not a zip container at all fails
// first use before anything is recorded.
func TestFirstUseUnparsableArchiveFails(t *testing.T) {
	fx := newFixture(t)
	fx.Endpoint("example.com/m", "v1.0.0", "zip", "not a zip container")
	c := fx.Client("proxy")
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil {
		t.Fatal("Module accepted unparsable archive bytes")
	}
	if _, ok := c.Lock.Module("example.com/m", "v1.0.0"); ok {
		t.Fatal("pin recorded from unparsable bytes")
	}
}

// A pin recording no module file must reject a digest-matching archive
// that does carry one: the two answers for the same file set disagree.
func TestPinnedSynthesizedRejectsArchiveWithModfile(t *testing.T) {
	fx := newFixture(t)
	zip, digest := moduleZip(t, map[string]string{"pb.yaml": "module: example.com/m\n"})
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	c := fx.Client("proxy")
	if err := c.Lock.AddModule(lockfile.ModulePin{Path: "example.com/m", Version: "v1.0.0", Digest: digest}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
		!strings.Contains(err.Error(), "records none") {
		t.Fatalf("err = %v, want the modfile/pin disagreement", err)
	}
}

// Provenance error arms: each in-spec failure shape surfaces, and no
// tolerated shape aborts.
func TestProvenanceEvaluationErrorArms(t *testing.T) {
	files := declaredFiles()
	serve := func(t *testing.T, prov string, provStatus int) *fixture {
		t.Helper()
		fx := newFixture(t)
		zip, _ := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		if provStatus != 0 {
			fx.Status[proxyHost+"/example.com/m/@v/v1.0.0.prov"] = provStatus
		} else if prov != "" {
			fx.Endpoint("example.com/m", "v1.0.0", "prov", prov)
		}
		return fx
	}

	t.Run("malformed envelope aborts even under allow-unsigned", func(t *testing.T) {
		fx := serve(t, "not json", 0)
		c := fx.Client("proxy")
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil {
			t.Fatal("malformed envelope tolerated")
		}
	})

	t.Run("prov transport failure aborts the fetch", func(t *testing.T) {
		fx := serve(t, "", 500)
		c := fx.Client("proxy")
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
			!strings.Contains(err.Error(), "unexpected status") {
			t.Fatalf("err = %v, want the transport failure itself", err)
		}
	})

	t.Run("envelope with only unrecognized evidence is unsigned", func(t *testing.T) {
		fx := serve(t, `{"formatVersion":1,"evidence":[{"type":"future-kind"}]}`, 0)
		c := fx.Client("proxy")
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatalf("Module under allow-unsigned: %v", err)
		}
		if pin, _ := c.Lock.Module("example.com/m", "v1.0.0"); pin.Provenance != (lockfile.Provenance{}) {
			t.Fatalf("provenance = %+v, want none", pin.Provenance)
		}
		c2 := fx.Client("proxy")
		c2.Policy = &trust.Policy{Default: trust.RequireProvenance}
		if _, err := c2.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
			!strings.Contains(err.Error(), "no recognized evidence") {
			t.Fatalf("err = %v, want the no-recognized-evidence failure", err)
		}
	})

	signer := provtest.New(t)

	t.Run("evidence with no trusted root configured", func(t *testing.T) {
		fx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
		c := fx.Client("proxy") // no TrustedRoot, no Policy: allow-unsigned
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatalf("Module: %v", err)
		}
		if pin, _ := c.Lock.Module("example.com/m", "v1.0.0"); pin.Provenance != (lockfile.Provenance{}) {
			t.Fatalf("provenance = %+v, want none", pin.Provenance)
		}
		c2 := fx.Client("proxy")
		c2.Policy = &trust.Policy{Default: trust.RequireProvenance}
		if _, err := c2.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
			!strings.Contains(err.Error(), "trusted root") {
			t.Fatalf("err = %v, want the no-trusted-root failure", err)
		}
	})

	t.Run("invalid explicit identity rule surfaces", func(t *testing.T) {
		fx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
		c := fx.clientWithPolicy(explicitRule("[", provtest.Issuer, trust.AllowUnsigned))
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
			!strings.Contains(err.Error(), "trust: identity rule") {
			t.Fatalf("err = %v, want the rule-construction failure itself", err)
		}
	})

	t.Run("underivable default identity beyond no-forge surfaces", func(t *testing.T) {
		fx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
		originOverride(fx.Fixture, "https://github.com/"+strings.Repeat("a", 8000))
		c := fx.clientWithPolicy(nil)
		c.TrustedRoot = signer.TrustedRoot()
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
			!strings.Contains(err.Error(), "trust: default identity") {
			t.Fatalf("err = %v, want the derivation failure itself", err)
		}
	})
}

// Origin resolution is memoized per module path: many resolutions of
// one module consult the resolver once.
func TestOriginResolutionMemoized(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	commit := fx.CommitFor(files, gitWhen)
	fx.Repo.Ref("refs/tags/v1.0.0", commit)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")

	c := fx.Client("direct")
	calls := 0
	inner := c.ResolveOrigin
	c.ResolveOrigin = func(ctx context.Context, modPath string) (origin.Origin, error) {
		calls++
		return inner(ctx, modPath)
	}
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("origin resolved %d times, want 1 (memoized)", calls)
	}
	// A second path interleaved must not evict the first memo entry:
	// the third resolution goes through Versions, which always needs
	// the origin — a pinned Module would ride the cache and see
	// nothing.
	fx.Subtrees["example.com/m2"] = ""
	if _, err := c.Versions(ctx, "example.com/m2"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Versions(ctx, "example.com/m"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("origin resolved %d times across two paths, want 2", calls)
	}
}

// Entry-selective storage faults: an arm masked by a same-sentinel
// fallback (a failing mod read falling back to a failing zip read)
// distinguishes itself when only its own entry faults.
func TestSelectiveCacheFaults(t *testing.T) {
	files := declaredFiles()
	seedPinned := func(t *testing.T, fx *fixture) *Client {
		t.Helper()
		seed := fx.Client("proxy")
		if _, err := seed.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatal(err)
		}
		return seed
	}

	t.Run("mod entry read fault is fatal despite the archive fallback", func(t *testing.T) {
		fx := newFixture(t)
		zip, _ := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		seed := seedPinned(t, fx)
		c := fx.Client("proxy")
		c.Lock = seed.Lock
		c.Cache = &Cache{FS: &errFS{Filesystem: memfs.New(), FailOpenSuffix: ".mod", PutFailAfter: -1}}
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v, want the injected fault", err)
		}
	})

	t.Run("zip entry read fault in the fallback is fatal", func(t *testing.T) {
		fx := newFixture(t)
		zip, _ := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		seed := seedPinned(t, fx)
		c := fx.Client("proxy")
		c.Lock = seed.Lock
		c.Cache = &Cache{FS: &errFS{Filesystem: memfs.New(), FailOpenSuffix: ".zip", PutFailAfter: -1}}
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v, want the injected fault", err)
		}
	})

	t.Run("zip write fault on the pinned refetch is fatal", func(t *testing.T) {
		fx := newFixture(t)
		zip, _ := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		seed := seedPinned(t, fx)
		c := fx.Client("proxy")
		c.Lock = seed.Lock
		c.Cache = &Cache{FS: &errFS{Filesystem: memfs.New(), FailRenameSfx: ".zip", PutFailAfter: -1}}
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v, want the injected fault", err)
		}
	})

	t.Run("mod write fault on the pinned refetch is fatal", func(t *testing.T) {
		fx := newFixture(t)
		zip, _ := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		fx.Endpoint("example.com/m", "v1.0.0", "mod", files["pb.yaml"])
		seed := seedPinned(t, fx)
		c := fx.Client("proxy")
		c.Lock = seed.Lock
		c.Cache = &Cache{FS: &errFS{Filesystem: memfs.New(), FailRenameSfx: ".mod", PutFailAfter: -1}}
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v, want the injected fault", err)
		}
	})

	t.Run("prov write fault at first use is fatal", func(t *testing.T) {
		fx := newProvFixture(t, provtest.New(t), sigstoretest.TagOptions{}, "v1.0.0")
		c := fx.clientWithPolicy(explicitRule(provtest.Subject, provtest.Issuer, trust.RequireProvenance))
		c.Cache = &Cache{FS: &errFS{Filesystem: memfs.New(), FailRenameSfx: ".prov", PutFailAfter: -1}}
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v, want the injected fault", err)
		}
	})
}

// Hand-crafted pins exercise the wire format's degenerate corners: the
// pipeline never produces them, but the lockfile is user-editable state
// and every disagreement fails closed.
func TestHandCraftedPinCorners(t *testing.T) {
	files := declaredFiles()
	fxFor := func(t *testing.T) (*fixture, []byte, string) {
		t.Helper()
		fx := newFixture(t)
		zip, digest := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		return fx, zip, digest
	}
	pin := func(t *testing.T, c *Client, p lockfile.ModulePin) {
		t.Helper()
		if err := c.Lock.AddModule(p); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("digestless pin cannot serve the archive", func(t *testing.T) {
		fx, _, _ := fxFor(t)
		c := fx.Client("proxy")
		pin(t, c, lockfile.ModulePin{Path: "example.com/m", Version: "v1.0.0"})
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
			!strings.Contains(err.Error(), "no digest") {
			t.Fatalf("err = %v, want the digestless-pin failure", err)
		}
	})

	t.Run("pin modfile hash disagreeing with the archive copy", func(t *testing.T) {
		fx, _, digest := fxFor(t)
		c := fx.Client("proxy")
		pin(t, c, lockfile.ModulePin{Path: "example.com/m", Version: "v1.0.0", Digest: digest,
			Modfile: "sha256:" + strings.Repeat("0", 64)})
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, lockfile.ErrPinMismatch) {
			t.Fatalf("err = %v, want ErrPinMismatch", err)
		}
	})

	t.Run("pinned module file that does not parse", func(t *testing.T) {
		fx := newFixture(t)
		bad := "not: [valid: modfile"
		zip, digest := moduleZip(t, map[string]string{"pb.yaml": bad})
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		c := fx.Client("proxy")
		pin(t, c, lockfile.ModulePin{Path: "example.com/m", Version: "v1.0.0", Digest: digest,
			Modfile: ModfileHash([]byte(bad))})
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil {
			t.Fatal("an unparsable pinned module file was accepted")
		}
	})

	t.Run("pinned module file declaring another identity", func(t *testing.T) {
		fx := newFixture(t)
		other := "module: example.com/other\n"
		zip, digest := moduleZip(t, map[string]string{"pb.yaml": other})
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		c := fx.Client("proxy")
		pin(t, c, lockfile.ModulePin{Path: "example.com/m", Version: "v1.0.0", Digest: digest,
			Modfile: ModfileHash([]byte(other))})
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil {
			t.Fatal("a pinned module file declaring another identity was accepted")
		}
	})
}

// Pinned-fetch failure classification: a transport failure is neither a
// pin mismatch nor a digest mismatch, and a served digest-mismatching
// archive is exactly a digest mismatch.
func TestPinnedFetchFailureClasses(t *testing.T) {
	files := declaredFiles()

	t.Run("zip transport failure surfaces as such", func(t *testing.T) {
		fx := newFixture(t)
		zip, _ := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		seed := fx.Client("proxy")
		if _, err := seed.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatal(err)
		}
		fx.Status[proxyHost+"/example.com/m/@v/v1.0.0.zip"] = 500
		c := fx.Client("proxy")
		c.Lock = seed.Lock
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
			!strings.Contains(err.Error(), "unexpected status") {
			t.Fatalf("err = %v, want the transport failure", err)
		}
	})

	t.Run("mod transport failure surfaces without falling back", func(t *testing.T) {
		fx := newFixture(t)
		zip, _ := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		seed := fx.Client("proxy")
		if _, err := seed.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatal(err)
		}
		fx.Status[proxyHost+"/example.com/m/@v/v1.0.0.mod"] = 500
		c := fx.Client("proxy")
		c.Lock = seed.Lock
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
			!strings.Contains(err.Error(), "unexpected status") {
			t.Fatalf("err = %v, want the transport failure", err)
		}
	})

	t.Run("served wrong-content archive is a digest mismatch", func(t *testing.T) {
		fx := newFixture(t)
		zip, _ := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		seed := fx.Client("proxy")
		if _, err := seed.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatal(err)
		}
		wrong, _ := moduleZip(t, map[string]string{"pb.yaml": "module: example.com/m\n"})
		fx.Endpoints[proxyHost+"/example.com/m/@v/v1.0.0.zip"] = wrong
		c := fx.Client("proxy")
		c.Lock = seed.Lock
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, archive.ErrDigestMismatch) {
			t.Fatalf("err = %v, want ErrDigestMismatch", err)
		}
	})
}

// Direct-source failure arms: seam failures abort, and resolution
// integrity failures abort as integrity failures — never as not-here.
func TestDirectSourceFailureArms(t *testing.T) {
	t.Run("origin resolution failure aborts", func(t *testing.T) {
		fx := newFixture(t)
		fx.ResolveOverride = func(context.Context, string) (origin.Origin, error) {
			return origin.Origin{}, errInjected
		}
		c := fx.Client("direct")
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v, want the injected fault", err)
		}
	})

	t.Run("repository fetch failure aborts", func(t *testing.T) {
		fx := newFixture(t)
		fx.ResolveOverride = func(context.Context, string) (origin.Origin, error) {
			return origin.Origin{Repo: ""}, nil // no URL: the clone cannot start
		}
		c := fx.Client("direct")
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil {
			t.Fatal("a failing repository fetch was tolerated")
		}
	})

	t.Run("base-inconsistent pseudo-version aborts, not not-here", func(t *testing.T) {
		fx := newFixture(t)
		files := declaredFiles()
		commit := fx.CommitFor(files, gitWhen)
		fx.Repo.Ref("refs/tags/v1.0.0", commit)
		fx.Repo.Ref("refs/heads/main", commit)
		fx.Repo.Symref("HEAD", "refs/heads/main")
		c := fx.Client("direct")
		// v1.0.0 is tagged on the commit itself, so a zero-base
		// pseudo-version over the same commit is base-inconsistent
		// (REQ-resolve-pseudo-base).
		pseudo := "v0.0.0-" + gitWhen.UTC().Format("20060102150405") + "-" + commit.String()[:12]
		_, err := c.Module(ctx, "example.com/m", ver(t, pseudo))
		if err == nil || errors.Is(err, proxy.ErrNotHere) {
			t.Fatalf("err = %v, want an integrity abort that is not not-here", err)
		}
		if !errors.Is(err, direct.ErrBaseInconsistent) {
			t.Fatalf("err = %v, want ErrBaseInconsistent", err)
		}
	})

	t.Run("subtree module root missing at the resolved commit aborts", func(t *testing.T) {
		fx := newFixture(t)
		commit := fx.CommitFor(map[string]string{"pb.yaml": "module: example.com/m\n"}, gitWhen)
		fx.Repo.Ref("refs/tags/sub/v1.0.0", commit) // tag namespace without the subtree
		fx.Repo.Ref("refs/heads/main", commit)
		fx.Repo.Symref("HEAD", "refs/heads/main")
		fx.Subtrees["example.com/m/sub"] = "sub"
		c := fx.Client("direct")
		if _, err := c.Module(ctx, "example.com/m/sub", ver(t, "v1.0.0")); !errors.Is(err, direct.ErrNoModuleRoot) {
			t.Fatalf("err = %v, want ErrNoModuleRoot", err)
		}
	})
}

// Versions failure arms: exhausted sources error, and a headless origin
// cannot answer the namespace question for a subtree module.
func TestVersionsFailureArms(t *testing.T) {
	t.Run("no source knows the module", func(t *testing.T) {
		fx := newFixture(t)
		c := fx.Client("proxy")
		if _, err := c.Versions(ctx, "example.com/unknown"); !errors.Is(err, proxy.ErrNotHere) {
			t.Fatalf("err = %v, want ErrNotHere", err)
		}
	})

	t.Run("headless origin fails a subtree listing", func(t *testing.T) {
		fx := newFixture(t)
		commit := fx.CommitFor(map[string]string{"sub/pb.yaml": "module: example.com/m/sub\n"}, gitWhen)
		fx.Repo.Ref("refs/tags/sub/v1.0.0", commit)
		fx.Repo.Ref("refs/heads/main", commit)
		// No HEAD symref: go-git refuses the clone itself, so the
		// failure surfaces at the repository seam — the later
		// Head-resolution arm is unreachable through a healthy fetch.
		fx.Subtrees["example.com/m/sub"] = "sub"
		c := fx.Client("direct")
		if _, err := c.Versions(ctx, "example.com/m/sub"); err == nil {
			t.Fatalf("a headless origin was tolerated")
		}
	})
}

// Download's re-verification accepts a reproducing evidence object even
// behind a non-reproducing one: verified-but-different evidence (a
// re-signed tag) is skipped, never fatal and never a downgrade.
func TestReverifySkipsNonReproducingEvidence(t *testing.T) {
	signer := provtest.New(t)
	fx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
	fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	c := fx.clientWithPolicy(explicitRule(provtest.Subject, provtest.Issuer, trust.RequireProvenance))
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}

	// A second signed tag over a different payload spelling: verifies,
	// binds, but hashes to a different signed object than the pin. The
	// reproducing object is the exact tag first use recorded — CMS
	// signing is randomized, so only the original bytes reproduce.
	other := signer.SignedTag(t, append(tagPayload(fx.commit, "v1.0.0"), '\n'), sigstoretest.TagOptions{})
	pinned := servedTag(t, fx.Endpoints[proxyHost+"/example.com/m/@v/v1.0.0.prov"])
	rawCommit := fx.Repo.Raw(plumbing.CommitObject, fx.commit)
	env := twoEvidenceEnvelope(t, other, pinned, rawCommit)
	fx.Endpoints[proxyHost+"/example.com/m/@v/v1.0.0.prov"] = env

	c2 := fx.clientWithPolicy(explicitRule(provtest.Subject, provtest.Issuer, trust.RequireProvenance))
	c2.Lock = c.Lock
	if err := c2.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("Download with a non-reproducing leading evidence object: %v", err)
	}
}

// Download-level arms: propagation from the first-use pipeline and the
// pinned-archive fetch, the synthesized shape, and cached-entry reuse.
func TestDownloadArms(t *testing.T) {
	t.Run("first-use failure propagates", func(t *testing.T) {
		fx := newFixture(t)
		c := fx.Client("proxy")
		if err := c.Download(ctx, "example.com/absent", ver(t, "v1.0.0")); !errors.Is(err, proxy.ErrNotHere) {
			t.Fatalf("err = %v, want ErrNotHere", err)
		}
	})

	t.Run("pinned archive transport failure propagates", func(t *testing.T) {
		fx := newFixture(t)
		files := declaredFiles()
		zip, _ := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		seed := fx.Client("proxy")
		if _, err := seed.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatal(err)
		}
		fx.Status[proxyHost+"/example.com/m/@v/v1.0.0.zip"] = 500
		c := fx.Client("proxy")
		c.Lock = seed.Lock
		if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
			!strings.Contains(err.Error(), "unexpected status") {
			t.Fatalf("err = %v, want the transport failure", err)
		}
	})

	t.Run("synthesized module downloads without a module-file entry", func(t *testing.T) {
		fx := newFixture(t)
		zip, _ := moduleZip(t, map[string]string{"a.proto": "syntax = \"proto3\";\n"})
		fx.Endpoint("example.com/syn", "v2.1.0", "zip", string(zip))
		fx.Endpoint("example.com/syn", "v2.1.0", "info", `{"version":"v2.1.0"}`)
		c := fx.Client("proxy")
		if err := c.Download(ctx, "example.com/syn", ver(t, "v2.1.0")); err != nil {
			t.Fatalf("Download: %v", err)
		}
		for _, kind := range []string{KindMod, KindProv} {
			if _, ok, _ := c.Cache.Get("example.com/syn", ver(t, "v2.1.0"), kind); ok {
				t.Fatalf("synthesized download cached a %s entry", kind)
			}
		}
	})

	t.Run("second download refetches nothing", func(t *testing.T) {
		fx := newFixture(t)
		files := declaredFiles()
		zip, _ := moduleZip(t, files)
		zipKey := fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		modKey := fx.Endpoint("example.com/m", "v1.0.0", "mod", files["pb.yaml"])
		infoKey := fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0"}`)
		c := fx.Client("proxy")
		if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatal(err)
		}
		before := map[string]int{zipKey: fx.Hits[zipKey], modKey: fx.Hits[modKey], infoKey: fx.Hits[infoKey]}
		if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatal(err)
		}
		for k, n := range before {
			if fx.Hits[k] != n {
				t.Fatalf("second download refetched %s (%d -> %d)", k, n, fx.Hits[k])
			}
		}
	})

	t.Run("mod cache write fault fails download", func(t *testing.T) {
		fx := newFixture(t)
		files := declaredFiles()
		zip, _ := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		fx.Endpoint("example.com/m", "v1.0.0", "mod", files["pb.yaml"])
		seed := fx.Client("proxy")
		if _, err := seed.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatal(err)
		}
		c := fx.Client("proxy")
		c.Lock = seed.Lock
		c.Cache = &Cache{FS: &errFS{Filesystem: memfs.New(), FailRenameSfx: ".mod", PutFailAfter: -1}}
		if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v, want the injected fault", err)
		}
	})

	t.Run("zip cache write fault at first use is fatal despite later successes", func(t *testing.T) {
		fx := newFixture(t)
		files := declaredFiles()
		zip, _ := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		fx.Endpoint("example.com/m", "v1.0.0", "mod", files["pb.yaml"])
		c := fx.Client("proxy")
		c.Cache = &Cache{FS: &errFS{Filesystem: memfs.New(), FailRenameSfx: ".zip", PutFailAfter: -1}}
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v, want the injected fault", err)
		}
	})

	t.Run("cache entry read-body fault surfaces", func(t *testing.T) {
		fx := newFixture(t)
		files := declaredFiles()
		zip, _ := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		seed := fx.Client("proxy")
		if _, err := seed.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatal(err)
		}
		c := fx.Client("proxy")
		c.Lock = seed.Lock
		fs := &errFS{Filesystem: memfs.New(), FailReadBody: true, PutFailAfter: -1}
		c.Cache = &Cache{FS: fs}
		// Seed an entry so the read is attempted.
		fs.FailReadBody = false
		if err := c.Cache.Put("example.com/m", ver(t, "v1.0.0"), KindZip, zip); err != nil {
			t.Fatal(err)
		}
		fs.FailReadBody = true
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v, want the injected fault", err)
		}
	})
}

// Versions' direct-leg seam failures surface through Versions itself.
func TestVersionsSeamFailures(t *testing.T) {
	t.Run("origin resolution failure", func(t *testing.T) {
		fx := newFixture(t)
		fx.ResolveOverride = func(context.Context, string) (origin.Origin, error) {
			return origin.Origin{}, errInjected
		}
		c := fx.Client("direct")
		if _, err := c.Versions(ctx, "example.com/m"); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v, want the injected fault", err)
		}
	})

	t.Run("repository fetch failure", func(t *testing.T) {
		fx := newFixture(t)
		fx.ResolveOverride = func(context.Context, string) (origin.Origin, error) {
			return origin.Origin{Repo: ""}, nil
		}
		c := fx.Client("direct")
		if _, err := c.Versions(ctx, "example.com/m"); err == nil {
			t.Fatal("a failing repository fetch was tolerated")
		}
	})
}

// Re-verification fails when the only verifying evidence yields a
// record other than the pinned one — acceptance is record identity,
// never mere verification.
func TestReverifyRequiresExactRecord(t *testing.T) {
	signer := provtest.New(t)
	fx := newProvFixture(t, signer, sigstoretest.TagOptions{}, "v1.0.0")
	fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	c := fx.clientWithPolicy(explicitRule(provtest.Subject, provtest.Issuer, trust.RequireProvenance))
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	// Serve only a re-signed tag: verifies, but is a different signed
	// object than the pin records.
	other := signer.SignedTag(t, append(tagPayload(fx.commit, "v1.0.0"), '\n'), sigstoretest.TagOptions{})
	rawCommit := fx.Repo.Raw(plumbing.CommitObject, fx.commit)
	fx.Endpoints[proxyHost+"/example.com/m/@v/v1.0.0.prov"] = envelope(t, "sha1", other, rawCommit, nil)

	c2 := fx.clientWithPolicy(explicitRule(provtest.Subject, provtest.Issuer, trust.RequireProvenance))
	c2.Lock = c.Lock
	if err := c2.Download(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, lockfile.ErrProvenanceDowngrade) {
		t.Fatalf("err = %v, want ErrProvenanceDowngrade", err)
	}
}

// Evidence present but the origin unresolvable: the evaluation cannot
// bind a subject and fails.
func TestEvidenceWithUnresolvableOriginFails(t *testing.T) {
	fx := newProvFixture(t, provtest.New(t), sigstoretest.TagOptions{}, "v1.0.0")
	fx.ResolveOverride = func(context.Context, string) (origin.Origin, error) {
		return origin.Origin{}, errInjected
	}
	c := fx.clientWithPolicy(nil)
	c.TrustedRoot = provtest.New(t).TrustedRoot()
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
		t.Fatalf("err = %v, want the injected fault", err)
	}
}

// Download-path cache read faults and transport/parse failure classes,
// entry-selective so no earlier arm masks the one under test.
func TestDownloadEntryFaultsAndClasses(t *testing.T) {
	files := declaredFiles()
	seed := func(t *testing.T, fx *fixture) *Client {
		t.Helper()
		c := fx.Client("proxy")
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatal(err)
		}
		return c
	}
	base := func(t *testing.T) *fixture {
		t.Helper()
		fx := newFixture(t)
		zip, _ := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		fx.Endpoint("example.com/m", "v1.0.0", "mod", files["pb.yaml"])
		fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0"}`)
		return fx
	}

	for _, sfx := range []string{".info", ".mod"} {
		t.Run("cache read fault on "+sfx, func(t *testing.T) {
			fx := base(t)
			s := seed(t, fx)
			c := fx.Client("proxy")
			c.Lock = s.Lock
			c.Cache = &Cache{FS: &errFS{Filesystem: memfs.New(), FailOpenSuffix: sfx, PutFailAfter: -1}}
			if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
				t.Fatalf("%s: err = %v, want the injected fault", sfx, err)
			}
		})
	}

	t.Run("info transport failure", func(t *testing.T) {
		fx := base(t)
		s := seed(t, fx)
		fx.Status[proxyHost+"/example.com/m/@v/v1.0.0.info"] = 500
		c := fx.Client("proxy")
		c.Lock = s.Lock
		if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
			!strings.Contains(err.Error(), "unexpected status") {
			t.Fatalf("err = %v, want the transport failure", err)
		}
	})

	t.Run("served junk info is a malformed response", func(t *testing.T) {
		fx := base(t)
		s := seed(t, fx)
		fx.Endpoints[proxyHost+"/example.com/m/@v/v1.0.0.info"] = []byte("junk")
		c := fx.Client("proxy")
		c.Lock = s.Lock
		if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, proxy.ErrMalformed) {
			t.Fatalf("err = %v, want ErrMalformed", err)
		}
	})

	t.Run("archive-copy write fault with no standalone source", func(t *testing.T) {
		fx := newFixture(t)
		zip, _ := moduleZip(t, files)
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		s := seed(t, fx)
		c := fx.Client("proxy")
		c.Lock = s.Lock
		c.Cache = &Cache{FS: &errFS{Filesystem: memfs.New(), FailRenameSfx: ".mod", PutFailAfter: -1}}
		if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v, want the injected fault", err)
		}
	})
}

// downloadProv-side classes behind a verified pin: cache read fault,
// transport failure, junk envelope, and a missing trusted root each
// surface as themselves.
func TestDownloadProvFailureClasses(t *testing.T) {
	seedVerified := func(t *testing.T) (*provFixture, *Client) {
		t.Helper()
		fx := newProvFixture(t, provtest.New(t), sigstoretest.TagOptions{}, "v1.0.0")
		fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0"}`)
		c := fx.clientWithPolicy(explicitRule(provtest.Subject, provtest.Issuer, trust.RequireProvenance))
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatal(err)
		}
		return fx, c
	}

	t.Run("cache read fault on .prov", func(t *testing.T) {
		fx, s := seedVerified(t)
		c := fx.clientWithPolicy(s.Policy)
		c.Lock = s.Lock
		c.Cache = &Cache{FS: &errFS{Filesystem: memfs.New(), FailOpenSuffix: ".prov", PutFailAfter: -1}}
		if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v, want the injected fault", err)
		}
	})

	t.Run("prov transport failure is not a downgrade", func(t *testing.T) {
		fx, s := seedVerified(t)
		fx.Status[proxyHost+"/example.com/m/@v/v1.0.0.prov"] = 500
		c := fx.clientWithPolicy(s.Policy)
		c.Lock = s.Lock
		err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0"))
		if err == nil || !strings.Contains(err.Error(), "unexpected status") || errors.Is(err, lockfile.ErrProvenanceDowngrade) {
			t.Fatalf("err = %v, want the transport failure itself", err)
		}
	})

	t.Run("served junk envelope is malformed, not a downgrade", func(t *testing.T) {
		fx, s := seedVerified(t)
		fx.Endpoints[proxyHost+"/example.com/m/@v/v1.0.0.prov"] = []byte("junk")
		c := fx.clientWithPolicy(s.Policy)
		c.Lock = s.Lock
		if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, provenance.ErrEnvelopeMalformed) {
			t.Fatalf("err = %v, want ErrEnvelopeMalformed", err)
		}
	})

	t.Run("missing trusted root fails re-verification", func(t *testing.T) {
		fx, s := seedVerified(t)
		c := fx.Client("proxy")
		c.Policy = s.Policy
		c.TrustedRoot = nil
		c.Lock = s.Lock
		if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
			!strings.Contains(err.Error(), "no trusted root") {
			t.Fatalf("err = %v, want the missing-root failure", err)
		}
	})
}

// A pinned declared module whose origin commit carries the module root
// but no module file: the direct .mod fetch answers not-here, the
// archive fallback then convicts the pin/archive disagreement.
func TestDirectDeclaredPinWithModfileAbsentAtOrigin(t *testing.T) {
	fx := newFixture(t)
	files := map[string]string{"a.proto": "syntax = \"proto3\";\n"}
	commit := fx.CommitFor(files, gitWhen)
	fx.Repo.Ref("refs/tags/v1.0.0", commit)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")
	_, digest := moduleZip(t, files)

	c := fx.Client("direct")
	if err := c.Lock.AddModule(lockfile.ModulePin{
		Path: "example.com/m", Version: "v1.0.0", Digest: digest,
		Modfile: "sha256:" + strings.Repeat("0", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, lockfile.ErrPinMismatch) ||
		!strings.Contains(err.Error(), "archive has none") {
		t.Fatalf("err = %v, want the pin-records-a-module-file conviction", err)
	}
}

// A client wired without an origin resolver fails loudly at the first
// use that needs one, never with a nil-dereference.
func TestNilOriginResolverGuard(t *testing.T) {
	fx := newFixture(t)
	c := fx.Client("direct")
	c.ResolveOrigin = nil
	fx.ResolveOverride = nil
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
		!strings.Contains(err.Error(), "no origin resolver") {
		t.Fatalf("err = %v, want the miswiring guard", err)
	}
}

// Download repairs a poisoned cached module-file entry: the mutant
// space where the cached-entry guard degrades must never leave wrong
// bytes behind a successful Download.
func TestDownloadRepairsPoisonedModEntry(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	zip, _ := moduleZip(t, files)
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	fx.Endpoint("example.com/m", "v1.0.0", "mod", files["pb.yaml"])
	fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	c := fx.Client("proxy")
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	if err := c.Cache.Put("example.com/m", ver(t, "v1.0.0"), KindMod, []byte("module: example.com/m\n# poison\n")); err != nil {
		t.Fatal(err)
	}
	if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("Download: %v", err)
	}
	b, ok, _ := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), KindMod)
	if !ok || string(b) != files["pb.yaml"] {
		t.Fatalf("cached mod after Download = %q, want the pinned bytes", b)
	}
}

// Download's standalone module-file fetch aborts on a transport
// failure — it does not silently fall back to the archive copy.
func TestDownloadModfileTransportFailureAborts(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	zip, _ := moduleZip(t, files)
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	seed := fx.Client("proxy")
	if _, err := seed.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	fx.Status[proxyHost+"/example.com/m/@v/v1.0.0.mod"] = 500
	// Fresh cache: the module-file entry must come from the sources.
	c := fx.Client("proxy")
	c.Lock = seed.Lock
	if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil ||
		!strings.Contains(err.Error(), "unexpected status") {
		t.Fatalf("err = %v, want the transport failure", err)
	}
}

// A second Download with verified provenance reuses the cached
// envelope: re-verification runs against local bytes, no refetch.
func TestSecondDownloadReusesCachedEnvelope(t *testing.T) {
	fx := newProvFixture(t, provtest.New(t), sigstoretest.TagOptions{}, "v1.0.0")
	fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	fx.Endpoint("example.com/m", "v1.0.0", "mod", declaredFiles()["pb.yaml"])
	c := fx.clientWithPolicy(explicitRule(provtest.Subject, provtest.Issuer, trust.RequireProvenance))
	if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	provKey := proxyHost + "/example.com/m/@v/v1.0.0.prov"
	before := fx.Hits[provKey]
	if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	if fx.Hits[provKey] != before {
		t.Fatalf("second download refetched the envelope (%d -> %d)", before, fx.Hits[provKey])
	}
}

// A pinned declared subtree module whose subtree vanished at the
// resolved commit: the direct module-file fetch surfaces the missing
// module root rather than treating it as a healthy not-here.
func TestDirectPinnedSubtreeMissingModuleRoot(t *testing.T) {
	fx := newFixture(t)
	commit := fx.CommitFor(map[string]string{"pb.yaml": "module: example.com/m\n"}, gitWhen)
	fx.Repo.Ref("refs/tags/sub/v1.0.0", commit)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")
	fx.Subtrees["example.com/m/sub"] = "sub"

	c := fx.Client("direct")
	if err := c.Lock.AddModule(lockfile.ModulePin{
		Path: "example.com/m/sub", Version: "v1.0.0",
		Digest:  "pb1:" + strings.Repeat("0", 64),
		Modfile: "sha256:" + strings.Repeat("0", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Module(ctx, "example.com/m/sub", ver(t, "v1.0.0")); !errors.Is(err, direct.ErrNoModuleRoot) {
		t.Fatalf("err = %v, want ErrNoModuleRoot", err)
	}
}
