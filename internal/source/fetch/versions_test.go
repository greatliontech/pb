package fetch

import (
	"bytes"
	"errors"
	"github.com/greatliontech/pb/internal/module/version"
	"testing"
	"time"

	"github.com/greatliontech/pb/internal/module/archive"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/source/proxy"
)

// Versions parses the proxy's advisory listing and falls through on
// not-here (REQ-proxy-endpoints, REQ-proxy-fallthrough).
func TestVersionsFromProxy(t *testing.T) {
	fx := newFixture(t)
	fx.Endpoints[proxyHost+"/example.com/m/@v/list"] = []byte("v1.0.0\nv1.2.0\nv0.9.0\n")
	c := fx.Client("proxy")
	vs, err := c.Versions(ctx, "example.com/m")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	got := make([]string, len(vs))
	for i, v := range vs {
		got[i] = v.String()
	}
	if len(got) != 3 || got[0] != "v1.0.0" {
		t.Fatalf("versions = %v", got)
	}
}

// The direct leg lists the origin's release tags in the module's tag
// namespace (REQ-resolve-release-tags): repository-level for a root
// module, subtree-prefixed for a declared subtree module, and
// repository-level again for a synthesized subtree
// (REQ-resolve-synthesized-tags).
func TestVersionsFromOrigin(t *testing.T) {
	fx := newFixture(t)
	rootFiles := map[string]string{
		"pb.yaml":         "module: example.com/m\n",
		"sub/pb.yaml":     "module: example.com/m/sub\n",
		"sub/a.proto":     "syntax = \"proto3\";\n",
		"bare/b.proto":    "syntax = \"proto3\";\n",
		"proto/svc.proto": "syntax = \"proto3\";\n",
	}
	commit := fx.CommitFor(rootFiles, gitWhen)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")
	fx.Repo.Ref("refs/tags/v1.0.0", commit)
	fx.Repo.Ref("refs/tags/v1.1.0", commit)
	fx.Repo.Ref("refs/tags/sub/v2.0.0", commit)

	fx.Subtrees["example.com/m/sub"] = "sub"
	fx.Subtrees["example.com/m/bare"] = "bare"
	c := fx.Client("direct")

	list := func(path string) []string {
		vs, err := c.Versions(ctx, path)
		if err != nil {
			t.Fatalf("Versions(%s): %v", path, err)
		}
		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = v.String()
		}
		return out
	}
	if got := list("example.com/m"); len(got) != 2 || got[0] != "v1.0.0" || got[1] != "v1.1.0" {
		t.Fatalf("root versions = %v", got)
	}
	if got := list("example.com/m/sub"); len(got) != 1 || got[0] != "v2.0.0" {
		t.Fatalf("declared subtree versions = %v", got)
	}
	// A subtree with no module file is synthesized: repository-level
	// namespace.
	if got := list("example.com/m/bare"); len(got) != 2 || got[0] != "v1.0.0" {
		t.Fatalf("synthesized subtree versions = %v", got)
	}
}

// Declaredness for the namespace is judged at the origin's
// default-branch head — the only state the origin can answer for — and
// a subtree absent there is no synthesized module: synthesis is the
// judgment over a subtree that exists holding no module file
// (REQ-resolve-synthesis). A subtree module deleted at head keeps its
// own namespace: its listing is its own tags, its release resolves
// through them, and the repository-level tag of a commit where it was
// declared is the root's release, not here for it — never served as a
// version the module never cut.
func TestVersionsSubtreeDeletedAtHeadKeepsItsNamespace(t *testing.T) {
	fx := newFixture(t)
	old := fx.CommitFor(map[string]string{
		"pb.yaml":     "module: example.com/m\n",
		"sub/pb.yaml": "module: example.com/m/sub\n",
	}, gitWhen)
	head := fx.CommitFor(map[string]string{
		"pb.yaml": "module: example.com/m\n",
	}, gitWhen.Add(time.Hour))
	fx.Repo.Ref("refs/heads/main", head)
	fx.Repo.Symref("HEAD", "refs/heads/main")
	fx.Repo.Ref("refs/tags/sub/v2.0.0", old)
	fx.Repo.Ref("refs/tags/v1.0.0", old)

	fx.Subtrees["example.com/m/sub"] = "sub"
	c := fx.Client("direct")
	vs, err := c.Versions(ctx, "example.com/m/sub")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vs) != 1 || vs[0].String() != "v2.0.0" {
		t.Fatalf("versions = %v, want the subtree's own listing", vs)
	}
	if mf, err := c.Module(ctx, "example.com/m/sub", vs[0]); err != nil || mf.Module != "example.com/m/sub" {
		t.Fatalf("the deleted subtree at its own release: %+v, %v", mf, err)
	}
	if _, err := c.Module(ctx, "example.com/m/sub", ver(t, "v1.0.0")); !errors.Is(err, proxy.ErrNotHere) {
		t.Fatalf("the root's release served as the deleted subtree's: %v", err)
	}
}

// Download materializes the full verified artifact set in the cache
// (REQ-dep-download).
func TestDownloadMaterializesArtifacts(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	zip, _ := moduleZip(t, files)
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	fx.Endpoint("example.com/m", "v1.0.0", "mod", files["pb.yaml"])
	fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0","time":"2023-11-14T22:13:20Z"}`)
	c := fx.Client("proxy")

	if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatalf("Download: %v", err)
	}
	for _, kind := range []string{KindZip, KindMod, KindInfo} {
		if _, ok, err := c.Cache.Get("example.com/m", ver(t, "v1.0.0"), kind); err != nil || !ok {
			t.Fatalf("cache %s: ok=%v err=%v", kind, ok, err)
		}
	}
}

// An info object naming a different version than it is addressed by is
// a malformed response, not a variant.
func TestDownloadRejectsMisaddressedInfo(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	zip, _ := moduleZip(t, files)
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	fx.Endpoint("example.com/m", "v1.0.0", "mod", files["pb.yaml"])
	fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.1"}`)
	c := fx.Client("proxy")

	if err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0")); err == nil {
		t.Fatal("Download accepted an info object naming another version")
	}
}

// A fetched standalone module file disagreeing with the archive's copy
// fails Download (REQ-lock-modfile-consistency).
func TestDownloadModfileInconsistencyFails(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	zip, _ := moduleZip(t, files)
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	c := fx.Client("proxy")
	if _, err := c.Module(ctx, "example.com/m", ver(t, "v1.0.0")); err != nil {
		t.Fatal(err)
	}
	// The standalone copy appears afterwards, disagreeing.
	fx.Endpoint("example.com/m", "v1.0.0", "mod", "module: example.com/m\n# drifted\n")
	c2 := fx.Client("proxy")
	c2.Lock = c.Lock
	if err := c2.Download(ctx, "example.com/m", ver(t, "v1.0.0")); !errors.Is(err, lockfile.ErrPinMismatch) {
		t.Fatalf("err = %v, want ErrPinMismatch", err)
	}
}

// A pin recording verified evidence whose envelope no source serves any
// longer fails Download rather than proceeding without the evidence
// (REQ-lock-no-silent-downgrade).
func TestDownloadVanishedEvidenceFailsClosed(t *testing.T) {
	fx := newFixture(t)
	files := declaredFiles()
	zip, _ := moduleZip(t, files)
	fx.Endpoint("example.com/m", "v1.0.0", "zip", string(zip))
	fx.Endpoint("example.com/m", "v1.0.0", "info", `{"version":"v1.0.0"}`)
	c := fx.Client("proxy")
	// Simulate an earlier explicit resolution having verified evidence.
	if err := c.Lock.AddModule(lockfile.ModulePin{
		Path: "example.com/m", Version: "v1.0.0",
		Digest:  mustDigest(t, zip),
		Modfile: ModfileHash([]byte(files["pb.yaml"])),
		Provenance: lockfile.Provenance{
			Type: "git-signed-tag", ObjectFormat: "sha1",
			Object: "0123456789012345678901234567890123456789",
			SAN:    "signer@example.com", Issuer: "https://accounts.example.com",
		},
	}); err != nil {
		t.Fatal(err)
	}
	err := c.Download(ctx, "example.com/m", ver(t, "v1.0.0"))
	if !errors.Is(err, lockfile.ErrProvenanceDowngrade) {
		t.Fatalf("err = %v, want ErrProvenanceDowngrade", err)
	}
}

// Golden layout (REQ-dep-cache-layout): version-addressed entries live
// at <escaped path>/@v/<escaped version>.<kind> with proxy escaping —
// uppercase in a prerelease is storage-safe.
func TestCacheLayoutGolden(t *testing.T) {
	if got := entryPath("example.com/m", ver(t, "v1.0.0-RC1"), KindZip); got != "example.com/m/@v/v1.0.0-!r!c1.zip" {
		t.Fatalf("entryPath = %q", got)
	}
	if got := entryPath("example.com/m", ver(t, "v1.0.0"), KindProv); got != "example.com/m/@v/v1.0.0.prov" {
		t.Fatalf("entryPath = %q", got)
	}
}

// Put is atomic and whole; Get reports absence without error and never
// serves a partial entry.
func TestCachePutGet(t *testing.T) {
	fx := newFixture(t)
	c := fx.Client("proxy")
	v := ver(t, "v1.0.0")
	if _, ok, err := c.Cache.Get("example.com/m", v, KindZip); ok || err != nil {
		t.Fatalf("Get on empty cache: ok=%v err=%v", ok, err)
	}
	if err := c.Cache.Put("example.com/m", v, KindZip, []byte("abc")); err != nil {
		t.Fatal(err)
	}
	b, ok, err := c.Cache.Get("example.com/m", v, KindZip)
	if err != nil || !ok || string(b) != "abc" {
		t.Fatalf("Get = %q, %v, %v", b, ok, err)
	}
}

func mustDigest(t *testing.T, zipBytes []byte) string {
	t.Helper()
	d, _, err := archive.DigestZip(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// Latest through a proxy is its @latest answer; through the origin the
// highest release tag in the module's namespace, else the head's
// pseudo-version with no precedent (REQ-resolve-synthesized-tags).
func TestLatest(t *testing.T) {
	fx := newFixture(t)
	fx.Endpoints[proxyHost+"/example.com/m/@latest"] = []byte(`{"version":"v1.2.0","time":"2024-01-02T03:04:05Z"}`)
	c := fx.Client("proxy")
	v, err := c.Latest(ctx, "example.com/m")
	if err != nil || v.String() != "v1.2.0" {
		t.Fatalf("proxy latest = %v, %v", v, err)
	}

	fx = newFixture(t)
	commit := fx.CommitFor(map[string]string{
		"pb.yaml":      "module: example.com/m\n",
		"sub/pb.yaml":  "module: example.com/m/sub\n",
		"bare/b.proto": "syntax = \"proto3\";\n",
	}, gitWhen)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")
	fx.Repo.Ref("refs/tags/v1.0.0", commit)
	fx.Repo.Ref("refs/tags/v1.1.0", commit)
	fx.Subtrees["example.com/m/sub"] = "sub"
	c = fx.Client("direct")
	if v, err := c.Latest(ctx, "example.com/m"); err != nil || v.String() != "v1.1.0" {
		t.Fatalf("origin latest = %v, %v", v, err)
	}
	// A declared subtree with no tag in its namespace: the head's
	// pseudo-version, no tagged release preceding it.
	want := "v0.0.0-" + gitWhen.UTC().Format(version.PseudoTimeLayout) + "-" + commit.String()[:12]
	if v, err := c.Latest(ctx, "example.com/m/sub"); err != nil || v.String() != want {
		t.Fatalf("origin head = %v, %v (want %s)", v, err, want)
	}
	// One release alone is the latest, no pseudo-version for it.
	fx = newFixture(t)
	commit = fx.CommitFor(map[string]string{"pb.yaml": "module: example.com/m\n"}, gitWhen)
	fx.Repo.Ref("refs/heads/main", commit)
	fx.Repo.Symref("HEAD", "refs/heads/main")
	fx.Repo.Ref("refs/tags/v3.0.0", commit)
	if v, err := fx.Client("direct").Latest(ctx, "example.com/m"); err != nil || v.String() != "v3.0.0" {
		t.Fatalf("one-tag latest = %v, %v", v, err)
	}
}
