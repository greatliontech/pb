package modfetch

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/greatliontech/pb/internal/archive"
	"github.com/greatliontech/pb/internal/direct"
	"github.com/greatliontech/pb/internal/gittest"
	"github.com/greatliontech/pb/internal/lockfile"
	"github.com/greatliontech/pb/internal/modfetchtest"
	"github.com/greatliontech/pb/internal/origin"
	"github.com/greatliontech/pb/internal/proxy"
	"pgregory.net/rapid"
)

var ctx = context.Background()

// declaredFiles is a module file set declaring example.com/m with one
// dependency.
func declaredFiles() map[string]string {
	return map[string]string{
		"pb.yaml":       "module: example.com/m\ndeps:\n  example.com/dep: v1.2.0\n",
		"proto/a.proto": "syntax = \"proto3\";\n",
	}
}

// First use of an unpinned pair fetches from sources, verifies, and
// records the complete pin in one step (REQ-lock-first-use): digest,
// module-file hash, and evaluated provenance — none here, tolerated but
// recorded (REQ-prov-unsigned-recorded). The verified artifacts land in
// the cache; a second resolution is served from it.
func TestFirstUseRecordsCompletePin(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	zip, digest := moduleZip(t, files)
	zipPath := fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	modPath := fx.Endpoint("example.com/m", "v1.0.0", "mod", files["pb.yaml"])
	c := fx.Client("proxy")

	mf, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0"))
	if err != nil {
		t.Fatalf("Module: %v", err)
	}
	if mf.Module != "example.com/m" || mf.Deps["example.com/dep"] != "v1.2.0" {
		t.Fatalf("module file = %+v", mf)
	}
	pin, ok := c.Lock.Module("example.com/m", "v1.0.0")
	if !ok {
		t.Fatal("no pin recorded")
	}
	if pin.Digest != digest {
		t.Fatalf("pin digest = %s, want %s", pin.Digest, digest)
	}
	if pin.Modfile != ModfileHash([]byte(files["pb.yaml"])) {
		t.Fatalf("pin modfile = %s", pin.Modfile)
	}
	if pin.Provenance != (lockfile.Provenance{}) {
		t.Fatalf("pin provenance = %+v, want none", pin.Provenance)
	}
	for _, kind := range []string{KindZip, KindMod} {
		if _, ok, err := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), kind); err != nil || !ok {
			t.Fatalf("cache %s: ok=%v err=%v", kind, ok, err)
		}
	}
	// No provenance was served, so nothing may sit at its cache entry.
	if _, ok, _ := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), KindProv); ok {
		t.Fatal("an absent provenance envelope was cached")
	}

	// Pinned resolution is served from the cache: no fetch at all.
	before, modBefore := fx.Hits[zipPath], fx.Hits[modPath]
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("pinned Module: %v", err)
	}
	if fx.Hits[zipPath] != before || fx.Hits[modPath] != modBefore {
		t.Fatalf("pinned resolution refetched (%d -> %d zip, %d -> %d mod hits)", before, fx.Hits[zipPath], modBefore, fx.Hits[modPath])
	}
}

// A subtree with no module file is consumable as a synthesized module
// (REQ-resolve-synthesis): identity is the required path, no
// dependencies, and the pin records no module-file hash.
func TestFirstUseSynthesizesModule(t *testing.T) {
	fx := newFixture(t)
	zip, digest := moduleZip(t, map[string]string{"a.proto": "syntax = \"proto3\";\n"})
	fx.Endpoint("example.com/syn", "v2.1.0", "zip", string(zip))
	c := fx.Client("proxy")

	mf, err := c.Module(ctx, "example.com/syn", ver(t, "v2.1.0"))
	if err != nil {
		t.Fatalf("Module: %v", err)
	}
	if mf.Module != "example.com/syn" || len(mf.Deps) != 0 {
		t.Fatalf("synthesized module file = %+v", mf)
	}
	pin, _ := c.Lock.Module("example.com/syn", "v2.1.0")
	if pin.Modfile != "" || pin.Digest != digest {
		t.Fatalf("pin = %+v", pin)
	}
	// A synthesized module writes no module-file or provenance entry.
	for _, kind := range []string{KindMod, KindProv} {
		if _, ok, _ := c.Cache.Get("example.com/syn", ver(t, "v2.1.0"), kind); ok {
			t.Fatalf("synthesized module cached a %s entry", kind)
		}
	}

	// The pinned pair keeps synthesizing from the digest-verified
	// archive — served from the cache, no refetch.
	before := fx.Hits[proxyHost+"/example.com/syn/@v/v2.1.0.zip"]
	mf, err = c.Module(ctx, "example.com/syn", ver(t, "v2.1.0"))
	if err != nil || mf.Module != "example.com/syn" {
		t.Fatalf("pinned synthesized Module = %+v, %v", mf, err)
	}
	if fx.Hits[proxyHost+"/example.com/syn/@v/v2.1.0.zip"] != before {
		t.Fatal("pinned synthesized resolution refetched the archive")
	}
}

// A fetched module whose module file declares a different path fails
// verification (REQ-modfile-identity) and leaves no pin and no cache
// entry behind.
func TestFirstUseIdentityMismatchLeavesNoState(t *testing.T) {
	fx := newFixture(t)
	zip, _ := moduleZip(t, map[string]string{"pb.yaml": "module: example.com/other\n"})
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	c := fx.Client("proxy")

	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil {
		t.Fatal("Module accepted a module declaring a different identity")
	}
	if _, ok := c.Lock.Module("example.com/m", "v1.0.0"); ok {
		t.Fatal("a failed verification recorded a pin")
	}
	if _, ok, _ := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), KindZip); ok {
		t.Fatal("a failed verification cached the artifact")
	}
}

// Property (REQ-proxy-client-verification, REQ-proxy-immutable's client
// half): once a pair is pinned, no source can change what is accepted
// for its version-addressed archive. Any byte flip either leaves the
// verified file set — and thus the digest, a pure function of the file
// set (REQ-archive-digest-purity) — identical, or fails the operation;
// tampering that alters any file's path, mode, or content is never
// accepted.
func TestPinnedArchiveTamperRejectedProperty(t *testing.T) {
	fx := newFixture(t)
	zip, digest := moduleZip(t, declaredFiles())
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	pinned := fx.Client("proxy")
	if _, err := pinned.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("seed Module: %v", err)
	}
	// .mod is deliberately not served: the pinned path must reach the
	// tampered archive.
	rapid.Check(t, func(rt *rapid.T) {
		pos := rapid.IntRange(0, len(zip)-1).Draw(rt, "pos")
		bit := rapid.IntRange(0, 7).Draw(rt, "bit")
		tampered := bytes.Clone(zip)
		tampered[pos] ^= 1 << bit
		fx.Endpoints[proxyHost+"/example.com/m/@v/v1.0.0.zip"] = tampered

		c := fx.Client("proxy")
		c.Lock = pinned.Lock // the pin store carries the trust
		_, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0"))
		if err == nil {
			// Accepted: the flip must not have changed the file set.
			d, _, derr := archive.DigestZip(bytes.NewReader(tampered), int64(len(tampered)))
			if derr != nil || d != digest {
				rt.Fatalf("content-changing tamper accepted (pos %d bit %d): digest %s err %v", pos, bit, d, derr)
			}
			return
		}
		if _, ok, _ := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), KindZip); ok {
			rt.Fatal("rejected archive cached")
		}
	})
	fx.Endpoints[proxyHost+"/example.com/m/@v/v1.0.0.zip"] = zip
}

// Property (REQ-dep-cache-transparent): whatever state the cache is in
// — empty, correct, or corrupted arbitrarily — a pinned resolution
// yields the identical result, and a corrupted entry is healed from
// sources rather than served or fatal.
func TestCacheTransparencyProperty(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	zip, _ := moduleZip(t, files)
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	fx.Endpoint("example.com/m", "v1.0.0", "mod", files["pb.yaml"])
	seed := fx.Client("proxy")
	want, err := seed.Module(ctx, "example.com/m", ver(t, "v1.0.0"))
	if err != nil {
		t.Fatalf("seed Module: %v", err)
	}
	poisonZip, _ := moduleZip(t, map[string]string{"pb.yaml": "module: example.com/m\n"})
	rapid.Check(t, func(rt *rapid.T) {
		c := fx.Client("proxy")
		c.Lock = seed.Lock
		for _, kind := range []string{KindZip, KindMod} {
			switch rapid.IntRange(0, 3).Draw(rt, kind) {
			case 0: // absent
			case 1: // correct
				b := zip
				if kind == KindMod {
					b = []byte(files["pb.yaml"])
				}
				if err := c.Cache.Put("example.com/m", ver(t, "v1.0.0"), kind, b); err != nil {
					rt.Fatal(err)
				}
			case 2: // corrupt
				junk := rapid.SliceOfN(rapid.Byte(), 0, 64).Draw(rt, kind+"-junk")
				if err := c.Cache.Put("example.com/m", ver(t, "v1.0.0"), kind, junk); err != nil {
					rt.Fatal(err)
				}
			case 3: // well-formed, wrong content — the hostile-cache shape
				b := poisonZip
				if kind == KindMod {
					b = []byte("module: example.com/m\n# not the pinned bytes\n")
				}
				if err := c.Cache.Put("example.com/m", ver(t, "v1.0.0"), kind, b); err != nil {
					rt.Fatal(err)
				}
			}
		}
		got, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0"))
		if err != nil {
			rt.Fatalf("Module with arbitrary cache state: %v", err)
		}
		if got.Module != want.Module || len(got.Deps) != len(want.Deps) || got.Deps["example.com/dep"] != want.Deps["example.com/dep"] {
			rt.Fatalf("cache state changed the outcome: %+v vs %+v", got, want)
		}
	})
}

// An unpinned resolution never reads the cache: a pin is what makes
// cache bytes verifiable, so first use pins what the sources serve,
// and the poisoned entry is overwritten by the verified fetch
// (REQ-dep-cache-transparent).
func TestFirstUseIgnoresCache(t *testing.T) {
	fx := newFixture(t)
	sourceZip, sourceDigest := moduleZip(t, declaredFiles())
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(sourceZip))
	c := fx.Client("proxy")

	poisonZip, poisonDigest := moduleZip(t, map[string]string{
		"pb.yaml": "module: example.com/m\n", // valid, differing content
	})
	if err := c.Cache.Put("example.com/m", ver(t, "v1.0.0"), KindZip, poisonZip); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("Module: %v", err)
	}
	pin, _ := c.Lock.Module("example.com/m", "v1.0.0")
	if pin.Digest == poisonDigest {
		t.Fatal("first use pinned the poisoned cache content")
	}
	if pin.Digest != sourceDigest {
		t.Fatalf("pin digest = %s, want the source's %s", pin.Digest, sourceDigest)
	}
	if b, ok, _ := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), KindZip); !ok || !bytes.Equal(b, sourceZip) {
		t.Fatal("cache not replaced by the verified fetch")
	}
}

// With no source serving the standalone module file, a pinned declared
// module's file comes from the digest-verified archive — the same bytes
// by REQ-lock-modfile-consistency.
func TestPinnedModfileFallsBackToArchive(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	zip, _ := moduleZip(t, files)
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	c := fx.Client("proxy")
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	// Fresh cache, no .mod endpoint: the pinned path goes through the
	// archive.
	c2 := fx.Client("proxy")
	c2.Lock = c.Lock
	mf, err := c2.Module(ctx, "example.com/m", ver(t, "v1.0.0"))
	if err != nil {
		t.Fatalf("Module: %v", err)
	}
	if mf.Module != "example.com/m" {
		t.Fatalf("module = %q", mf.Module)
	}
}

// A fetched standalone module file that does not hash to the pin fails
// the operation (REQ-lock-digest-enforcement) — served bytes, unlike
// cache bytes, are somebody's answer for the immutable artifact.
func TestPinnedModfileMismatchFailsClosed(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	zip, _ := moduleZip(t, files)
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	c := fx.Client("proxy")
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	fx.Endpoint("example.com/m", "v1.0.0", "mod", "module: example.com/m\n# rewritten\n")
	c2 := fx.Client("proxy")
	c2.Lock = c.Lock
	if _, err := c2.Module(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, lockfile.ErrPinMismatch) {
		t.Fatalf("err = %v, want ErrPinMismatch", err)
	}
}

// Fall-through consults sources in order and only on not-here
// (REQ-proxy-fallthrough): a second proxy serves what the first lacks;
// any other failure aborts without consulting later sources; off fails
// when reached.
func TestSourceFallthrough(t *testing.T) {
	zipKey := proxyHost + "/example.com/m/@v/v1.0.0.zip"

	t.Run("second source serves after a 404", func(t *testing.T) {
		fx := newFixture(t)
		zip, _ := moduleZip(t, declaredFiles())
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		// alt has nothing: its 404 moves the fetch to proxy.
		c := fx.Client("alt,proxy")
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
			t.Fatalf("Module: %v", err)
		}
		if fx.Hits[zipKey] == 0 {
			t.Fatal("the second source was never consulted")
		}
	})

	t.Run("a 500 aborts without consulting later sources", func(t *testing.T) {
		fx := newFixture(t)
		zip, _ := moduleZip(t, declaredFiles())
		fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
		fx.Status[altHost+"/example.com/m/@v/v1.0.0.zip"] = 500
		c := fx.Client("alt,proxy")
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil {
			t.Fatal("a 5xx source did not abort the fetch")
		}
		if fx.Hits[zipKey] != 0 {
			t.Fatal("a later source was consulted after an aborting failure")
		}
	})

	t.Run("off fails when reached", func(t *testing.T) {
		fx := newFixture(t)
		c := fx.Client("off")
		if _, err := c.Module(ctx, "example.com/m", ver(t, "v9.9.9")); !errors.Is(err, proxy.ErrOff) {
			t.Fatalf("err = %v, want ErrOff", err)
		}
	})
}

// The direct source constructs artifacts from the origin repository
// itself (REQ-proxy-direct-equivalence): a release tag resolves,
// the archive digests identically to the proxy's for the same file
// set, and the module file comes from the tree.
func TestDirectSourceServesArtifacts(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	commit := fx.CommitFor(files, gitWhen)
	fx.Repo.Ref("refs/tags/v1.0.0", commit)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")
	_, wantDigest := moduleZip(t, files)

	c := fx.Client("direct")
	mf, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0"))
	if err != nil {
		t.Fatalf("Module: %v", err)
	}
	if mf.Module != "example.com/m" {
		t.Fatalf("module = %q", mf.Module)
	}
	pin, _ := c.Lock.Module("example.com/m", "v1.0.0")
	if pin.Digest != wantDigest {
		t.Fatalf("direct digest %s != proxy-equivalent %s", pin.Digest, wantDigest)
	}

	// A version with no tag is not-here at the origin: with direct as
	// the only source, the fetch exhausts the list.
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v3.0.0")); !errors.Is(err, proxy.ErrNotHere) {
		t.Fatalf("unknown version err = %v, want ErrNotHere", err)
	}
}

// The module cache never decides between sources: with the archive
// pinned from a proxy, a later direct-only client verifies the same
// digest from the origin (one artifact identity across sources).
func TestPinnedPairVerifiesAcrossSources(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	zip, _ := moduleZip(t, files)
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	commit := fx.CommitFor(files, gitWhen)
	fx.Repo.Ref("refs/tags/v1.0.0", commit)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")

	viaProxy := fx.Client("proxy")
	if _, err := viaProxy.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	viaDirect := fx.Client("direct")
	viaDirect.Lock = viaProxy.Lock
	if _, err := viaDirect.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("direct against proxy-recorded pin: %v", err)
	}
}

// The direct source constructs the full artifact set (zip, module
// file, info, provenance absence) for Download — proxy-equivalent end
// to end (REQ-proxy-direct-equivalence).
func TestDirectSourceDownload(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	commit := fx.CommitFor(files, gitWhen)
	fx.Repo.Ref("refs/tags/v1.0.0", commit)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")

	c := fx.Client("direct")
	if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("Download: %v", err)
	}
	for _, kind := range []string{KindZip, KindMod, KindInfo} {
		if _, ok, err := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), kind); err != nil || !ok {
			t.Fatalf("cache %s: ok=%v err=%v", kind, ok, err)
		}
	}
	// The lightweight tag carries no signature: provenance is absent,
	// recorded none, and nothing sits at the prov entry.
	pin, _ := c.Lock.Module("example.com/m", "v1.0.0")
	if pin.Provenance != (lockfile.Provenance{}) {
		t.Fatalf("provenance = %+v, want none", pin.Provenance)
	}
	if _, ok, _ := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), KindProv); ok {
		t.Fatal("absent provenance cached")
	}
	// The cached info is the canonical direct construction: it names
	// the version and the commit's time.
	b, _, _ := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), KindInfo)
	info, err := proxy.ParseInfo(b)
	if err != nil || info.Version.String() != "v1.0.0" || !info.Time.Equal(gitWhen) {
		t.Fatalf("direct info = %+v, %v", info, err)
	}
}

// An artifact kind outside the endpoint set is an internal contract
// violation both arms refuse rather than fetch something else.
func TestUnknownArtifactKindRejected(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	commit := fx.CommitFor(files, gitWhen)
	fx.Repo.Ref("refs/tags/v1.0.0", commit)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")

	c := fx.Client("proxy")
	if _, err := c.fetch(ctx, "example.com/m", ver(t, "v1.0.0"), "bogus"); err == nil ||
		!strings.Contains(err.Error(), "unknown artifact kind") {
		t.Fatalf("proxy arm err = %v, want unknown-kind", err)
	}
	c2 := fx.Client("direct")
	if _, err := c2.fetch(ctx, "example.com/m", ver(t, "v1.0.0"), "bogus"); err == nil ||
		!strings.Contains(err.Error(), "unknown artifact kind") {
		t.Fatalf("direct arm err = %v, want unknown-kind", err)
	}
}

// One run resolves against one fetched origin state: the repository
// memo is snapshot consistency, not a cost optimization. A tag that
// appears at the origin after the first fetch is invisible to this
// client — and visible to a fresh one — so no operation can mix two
// origin states in one artifact set.
func TestRepoFetchIsSnapshotConsistent(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	commit := fx.CommitFor(files, gitWhen)
	fx.Repo.Ref("refs/tags/v1.0.0", commit)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")

	c := fx.Client("direct")
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}

	// The origin moves after the fetch.
	later := fx.CommitFor(files, gitWhen.Add(time.Hour))
	fx.Repo.Ref("refs/tags/v2.0.0", later)

	if _, err := c.Module(ctx, "example.com/m", ver(t, "v2.0.0")); !errors.Is(err, proxy.ErrNotHere) {
		t.Fatalf("post-fetch tag through the same client: err = %v, want ErrNotHere (snapshot)", err)
	}
	fresh := fx.Client("direct")
	if _, err := fresh.Module(ctx, "example.com/m", ver(t, "v2.0.0")); err != nil {
		t.Fatalf("post-fetch tag through a fresh client: %v", err)
	}
}

// Two origins through one client: each keeps its own fetched snapshot,
// and interleaving them never evicts the other's — the repository memo
// is per-URL snapshot consistency, not a single-slot cache.
func TestTwoOriginSnapshotsIndependent(t *testing.T) {
	sharedFS := memfs.New()
	repoA := gittest.NewAt(t, sharedFS, "a")
	repoB := gittest.NewAt(t, sharedFS, "b")
	build := func(r *gittest.Repo, module string) {
		files := map[string]string{"pb.yaml": "module: " + module + "\n"}
		fx := &fixture{&modfetchtest.Fixture{T: t, Repo: r}}
		commit := fx.CommitFor(files, gitWhen)
		r.Ref("refs/tags/v1.0.0", commit)
		r.Ref("refs/heads/main", commit)
		r.Symref("HEAD", "refs/heads/main")
	}
	build(repoA, "example.com/a")
	build(repoB, "example.com/b")

	fx := newFixture(t)
	fx.ResolveOverride = func(_ context.Context, modPath string) (origin.Origin, error) {
		if modPath == "example.com/b" {
			return origin.Origin{Repo: "file:///b"}, nil
		}
		return origin.Origin{Repo: "file:///a"}, nil
	}
	c := fx.Client("direct")
	c.Fetcher = direct.Fetcher{ClientOptions: repoA.ClientOptions()} // shared FS serves both

	if _, err := c.Module(ctx, "example.com/a", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Module(ctx, "example.com/b", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	// Both origins move after their fetches; both snapshots must hold.
	for _, r := range []*gittest.Repo{repoA, repoB} {
		commit := (&fixture{&modfetchtest.Fixture{T: t, Repo: r}}).CommitFor(map[string]string{"x.proto": "syntax = \"proto3\";\n"}, gitWhen.Add(time.Hour))
		r.Ref("refs/tags/v2.0.0", commit)
	}
	if _, err := c.Versions(ctx, "example.com/a"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"example.com/a", "example.com/b"} {
		vs, err := c.Versions(ctx, path)
		if err != nil {
			t.Fatalf("Versions(%s): %v", path, err)
		}
		if len(vs) != 1 || vs[0].String() != "v1.0.0" {
			t.Fatalf("%s versions = %v, want the fetch-time snapshot", path, vs)
		}
	}
}
