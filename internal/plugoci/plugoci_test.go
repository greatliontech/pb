package plugoci

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/greatliontech/ocifs"
	"github.com/greatliontech/pb/internal/lockfile"
	"github.com/greatliontech/pb/internal/trust"
)

var ctx = context.Background()

// fixture starts an in-memory registry and pushes a multi-platform
// index (host platform + one foreign) at :v1.
type fixture struct {
	host   string
	digest string // the index digest as pushed
}

func hostPlatform() v1.Platform {
	p := HostPlatform()
	return v1.Platform{OS: p.OS, Architecture: p.Arch}
}

func pushIndex(t *testing.T, ref string, platforms ...v1.Platform) string {
	return pushIndexEnv(t, ref, []string{"A=1"}, platforms...)
}

func pushIndexEnv(t *testing.T, ref string, env []string, platforms ...v1.Platform) string {
	t.Helper()
	idx := v1.ImageIndex(empty.Index)
	for _, p := range platforms {
		img, err := random.Image(64, 1)
		if err != nil {
			t.Fatal(err)
		}
		img, err = mutate.ConfigFile(img, &v1.ConfigFile{OS: p.OS, Architecture: p.Architecture, Config: v1.Config{Entrypoint: []string{"/plugin"}, Env: env}})
		if err != nil {
			t.Fatal(err)
		}
		// Each child carries its platform's spelling in a file, so an
		// export can be told apart by the child it came from.
		spelled := p.OS + "/" + p.Architecture
		if p.Variant != "" {
			spelled += "/" + p.Variant
		}
		marker, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			if err := tw.WriteHeader(&tar.Header{Name: "platform", Mode: 0o644, Size: int64(len(spelled))}); err != nil {
				return nil, err
			}
			if _, err := tw.Write([]byte(spelled)); err != nil {
				return nil, err
			}
			if err := tw.Close(); err != nil {
				return nil, err
			}
			return io.NopCloser(&buf), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		img, err = mutate.AppendLayers(img, marker)
		if err != nil {
			t.Fatal(err)
		}
		pl := p
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &pl}})
	}
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(r, idx); err != nil {
		t.Fatal(err)
	}
	h, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return h.String()
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	digest := pushIndex(t, host+"/org/plugin:v1", hostPlatform(), v1.Platform{OS: "plan9", Architecture: "mips"})
	return &fixture{host: host, digest: digest}
}

func newAcquirer(t *testing.T, fx *fixture, lock *lockfile.File, policy *trust.Policy, verifier ImageVerifier) *Acquirer {
	t.Helper()
	a, err := New(Config{
		WorkDir:  t.TempDir(),
		Lock:     lock,
		Policy:   policy,
		Verifier: verifier,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// First use resolves the tag once, records the pin with provenance
// none under allow-unsigned, and exports a root filesystem
// (REQ-lock-first-use, REQ-prov-unsigned-recorded).
func TestAcquireFirstUse(t *testing.T) {
	fx := newFixture(t)
	lock := &lockfile.File{}
	a := newAcquirer(t, fx, lock, &trust.Policy{}, nil)
	got, err := a.Acquire(ctx, fx.host+"/org/plugin:v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Pin.Digest != fx.digest || got.Pin.Scheme != lockfile.SchemeOCI || got.Pin.Provenance != (lockfile.Provenance{}) {
		t.Fatalf("pin = %+v, want digest %s provenance none", got.Pin, fx.digest)
	}
	if len(got.Process.Argv) != 1 || got.Process.Argv[0] != "/plugin" || len(got.Process.Env) != 1 {
		t.Fatalf("image config not surfaced: %+v", got)
	}
	if _, ok := lock.Plugin(fx.host+"/org/plugin:v1", lockfile.SchemeOCI); !ok {
		t.Fatal("pin not recorded")
	}
	fi, err := os.Stat(got.Rootfs)
	if err != nil || !fi.IsDir() {
		t.Fatalf("rootfs %q: %v", got.Rootfs, err)
	}
	entries, err := os.ReadDir(got.Rootfs)
	if err != nil || len(entries) == 0 {
		t.Fatalf("rootfs empty: %v %v", entries, err)
	}
	_ = filepath.Join
}

// A pinned reference never re-resolves the tag: content moved under
// the tag is invisible, the pinned digest is what materializes
// (REQ-plugin-digest-pin).
func TestAcquirePinnedIgnoresMovedTag(t *testing.T) {
	fx := newFixture(t)
	ref := fx.host + "/org/plugin:v1"
	lock := &lockfile.File{}
	a := newAcquirer(t, fx, lock, &trust.Policy{}, nil)
	if _, err := a.Acquire(ctx, ref); err != nil {
		t.Fatal(err)
	}
	// Move the tag to different content.
	moved := pushIndex(t, ref, hostPlatform())
	if moved == fx.digest {
		t.Fatal("fixture: tag did not move")
	}
	got, err := a.Acquire(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pin.Digest != fx.digest {
		t.Fatalf("pinned acquire served %s, want the pinned %s", got.Pin.Digest, fx.digest)
	}
}

// A pin whose digest the registry no longer serves fails; nothing
// rewrites the pin (REQ-lock-digest-enforcement's plugin analog).
func TestAcquirePinMismatchFailsClosed(t *testing.T) {
	fx := newFixture(t)
	ref := fx.host + "/org/plugin:v1"
	lock := &lockfile.File{}
	bogus := "sha256:" + strings.Repeat("ab", 32)
	if err := lock.AddPlugin(lockfile.PluginPin{Ref: ref, Scheme: lockfile.SchemeOCI, Digest: bogus}); err != nil {
		t.Fatal(err)
	}
	a := newAcquirer(t, fx, lock, &trust.Policy{}, nil)
	_, err := a.Acquire(ctx, ref)
	if err == nil {
		t.Fatal("bogus pin acquired")
	}
	pin, _ := lock.Plugin(ref, lockfile.SchemeOCI)
	if pin.Digest != bogus {
		t.Fatal("pin rewritten on failure")
	}
}

// No host-platform entry in the manifest list refuses, naming the
// platforms found; a bare manifest (no list) refuses as the same
// class (REQ-plugin-platform-strict).
func TestAcquirePlatformStrict(t *testing.T) {
	fx := newFixture(t)
	foreign := fx.host + "/org/foreign:v1"
	pushIndex(t, foreign, v1.Platform{OS: "plan9", Architecture: "mips"})
	a := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{}, nil)
	_, err := a.Acquire(ctx, foreign)
	if err == nil || !strings.Contains(err.Error(), "does not support this platform") || !strings.Contains(err.Error(), "plan9/mips") {
		t.Fatalf("foreign platform: %v", err)
	}

	bare := fx.host + "/org/bare:v1"
	img, _ := random.Image(64, 1)
	r, _ := name.ParseReference(bare)
	if err := remote.Write(r, img); err != nil {
		t.Fatal(err)
	}
	_, err = a.Acquire(ctx, bare)
	if err == nil || !strings.Contains(err.Error(), "not a manifest list") {
		t.Fatalf("bare manifest: %v", err)
	}

	// Several entries for the host — variants of one architecture —
	// are refused: choosing among them would be a fallback.
	host := hostPlatform()
	several := fx.host + "/org/several:v1"
	pushIndex(t, several, v1.Platform{OS: host.OS, Architecture: host.Architecture, Variant: "v1"}, v1.Platform{OS: host.OS, Architecture: host.Architecture, Variant: "v2"})
	_, err = a.Acquire(ctx, several)
	if err == nil || !strings.Contains(err.Error(), "2 entries for "+host.OS+"/"+host.Architecture+" in its manifest list (["+host.OS+"/"+host.Architecture+"/v1 "+host.OS+"/"+host.Architecture+"/v2])") {
		t.Fatalf("several entries: %v", err)
	}
}

// The seam records the one entry it admitted, its variant included,
// and the daemon byte path hands it on as the platform to pull: the
// child the run uses is pb's choice, never the daemon's
// (REQ-plugin-platform-strict, REQ-plugin-core-verifies).
func TestAcquireAdmittedPlatform(t *testing.T) {
	fx := newFixture(t)
	host := hostPlatform()
	ref := fx.host + "/org/variant:v1"
	// The host's entry sits second: the admitted entry is the one
	// that matched, not the first listed.
	pushIndex(t, ref, v1.Platform{OS: "plan9", Architecture: "mips"}, v1.Platform{OS: host.OS, Architecture: host.Architecture, Variant: "v9"})
	a, err := New(Config{WorkDir: t.TempDir(), Lock: &lockfile.File{}, Policy: &trust.Policy{}, Pull: PullDaemon})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	got, err := a.Acquire(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if want := host.OS + "/" + host.Architecture + "/v9"; got.Platform != want {
		t.Fatalf("admitted platform = %q, want %q", got.Platform, want)
	}
	plain := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{}, nil)
	if got, err := plain.Acquire(ctx, fx.host+"/org/plugin:v1"); err != nil || got.Platform != host.OS+"/"+host.Architecture {
		t.Fatalf("an entry without a variant: %+v %v", got, err)
	}
	// The store path exports that same child: the one entry that
	// matched, whatever its variant — its own marker is in the export.
	got, err = plain.Acquire(ctx, ref)
	if err != nil || got.Platform != host.OS+"/"+host.Architecture+"/v9" {
		t.Fatalf("the store path's admitted child: %+v %v", got, err)
	}
	if marker, err := os.ReadFile(filepath.Join(got.Rootfs, "platform")); err != nil || string(marker) != host.OS+"/"+host.Architecture+"/v9" {
		t.Fatalf("the export is not the admitted child's: %q %v", marker, err)
	}
}

// require-provenance with no verifier fails closed naming the gap; a
// verifier's record lands in the pin (REQ-plugin-verify-before-run,
// REQ-prov-plugin-signature's seam position).
func TestAcquireProvenancePolicy(t *testing.T) {
	fx := newFixture(t)
	ref := fx.host + "/org/plugin:v1"
	requireAll := &trust.Policy{Default: trust.RequireProvenance}
	a := newAcquirer(t, fx, &lockfile.File{}, requireAll, nil)
	_, err := a.Acquire(ctx, ref)
	if !errors.Is(err, ErrNoImageVerifier) {
		t.Fatalf("err = %v, want ErrNoImageVerifier", err)
	}

	lock := &lockfile.File{}
	ver := &stubVerifier{prov: lockfile.Provenance{Type: "git-signed-tag", ObjectFormat: "sha256", Object: strings.Repeat("ab", 32), SAN: "https://ci.example/wf", Issuer: "https://issuer.example"}}
	a2 := newAcquirer(t, fx, lock, requireAll, ver)
	got, err := a2.Acquire(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pin.Provenance != ver.prov {
		t.Fatalf("pin provenance = %+v", got.Pin.Provenance)
	}
	if ver.gotRef != ref || ver.gotDigest != fx.digest {
		t.Fatalf("verifier saw (%s, %s)", ver.gotRef, ver.gotDigest)
	}

	// A failing verifier aborts the acquisition; nothing is pinned.
	lock3 := &lockfile.File{}
	a3 := newAcquirer(t, fx, lock3, requireAll, &stubVerifier{err: fmt.Errorf("evidence rotten")})
	if _, err := a3.Acquire(ctx, ref); err == nil || !strings.Contains(err.Error(), "evidence rotten") {
		t.Fatalf("verifier failure: %v", err)
	}
	if len(lock3.Plugins) != 0 {
		t.Fatal("failed verification left a pin")
	}
}

// Under allow-unsigned an available verifier is still consulted: what
// verifies is recorded; tolerable non-acceptance (absence, identity)
// records none; rejected evidence aborts even here — the module
// pipeline's classification at the plugin surface.
func TestAcquireOpportunisticVerification(t *testing.T) {
	fx := newFixture(t)
	ref := fx.host + "/org/plugin:v1"
	allowAll := &trust.Policy{}

	prov := lockfile.Provenance{Type: "git-signed-tag", ObjectFormat: "sha256", Object: strings.Repeat("cd", 32), SAN: "https://ci.example/wf", Issuer: "https://issuer.example"}
	lock := &lockfile.File{}
	a := newAcquirer(t, fx, lock, allowAll, &stubVerifier{prov: prov})
	got, err := a.Acquire(ctx, ref)
	if err != nil || got.Pin.Provenance != prov {
		t.Fatalf("verified evidence not recorded under allow-unsigned: %+v %v", got, err)
	}

	lock2 := &lockfile.File{}
	a2 := newAcquirer(t, fx, lock2, allowAll, &stubVerifier{err: fmt.Errorf("nothing here: %w", ErrNoEvidence)})
	got2, err := a2.Acquire(ctx, ref)
	if err != nil || got2.Pin.Provenance != (lockfile.Provenance{}) {
		t.Fatalf("absence not tolerated as none: %+v %v", got2, err)
	}

	a3 := newAcquirer(t, fx, &lockfile.File{}, allowAll, &stubVerifier{err: fmt.Errorf("odd identity: %w", ErrIdentityNotAccepted)})
	if got3, err := a3.Acquire(ctx, ref); err != nil || got3.Pin.Provenance != (lockfile.Provenance{}) {
		t.Fatalf("identity non-acceptance not tolerated: %v", err)
	}

	a4 := newAcquirer(t, fx, &lockfile.File{}, allowAll, &stubVerifier{err: errors.New("signature does not verify")})
	if _, err := a4.Acquire(ctx, ref); err == nil || !strings.Contains(err.Error(), "signature does not verify") {
		t.Fatalf("rejected evidence tolerated under allow-unsigned: %v", err)
	}

	// Under require-provenance, tolerable absence is still a failure.
	requireAll := &trust.Policy{Default: trust.RequireProvenance}
	a5 := newAcquirer(t, fx, &lockfile.File{}, requireAll, &stubVerifier{err: fmt.Errorf("bare: %w", ErrNoEvidence)})
	if _, err := a5.Acquire(ctx, ref); !errors.Is(err, ErrNoEvidence) {
		t.Fatalf("require + absence: %v", err)
	}
}

type stubVerifier struct {
	prov      lockfile.Provenance
	err       error
	gotRef    string
	gotDigest string
}

func (s *stubVerifier) VerifyImage(_ context.Context, ref, digest string, _ *trust.IdentityRule) (lockfile.Provenance, error) {
	s.gotRef, s.gotDigest = ref, digest
	return s.prov, s.err
}

// The identity rule from the longest-prefix plugins rule reaches the
// verifier (REQ-prov-policy-eval at the plugin surface).
func TestAcquireIdentityRuleFlows(t *testing.T) {
	fx := newFixture(t)
	ref := fx.host + "/org/plugin:v1"
	rule := &trust.IdentityRule{SAN: "https://ci.example/**", Issuer: "https://issuer.example"}
	policy := &trust.Policy{Plugins: []trust.Rule{{Prefix: fx.host + "/org", Require: trust.RequireProvenance, Identity: rule}}}
	ver := &idCapture{}
	a := newAcquirer(t, fx, &lockfile.File{}, policy, ver)
	if _, err := a.Acquire(ctx, ref); err == nil || !strings.Contains(err.Error(), "no evidence") {
		t.Fatalf("err = %v", err)
	}
	if ver.got == nil || ver.got.SAN != rule.SAN {
		t.Fatalf("identity rule did not reach the verifier: %+v", ver.got)
	}
}

type idCapture struct{ got *trust.IdentityRule }

func (c *idCapture) VerifyImage(_ context.Context, _, _ string, id *trust.IdentityRule) (lockfile.Provenance, error) {
	c.got = id
	return lockfile.Provenance{}, errors.New("no evidence")
}

// An export that cannot materialize fails the acquisition — no
// Acquired with an empty root filesystem, no pin recorded for it.
func TestAcquireExportFailureFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through permission bits")
	}
	fx := newFixture(t)
	lock := &lockfile.File{}
	work := t.TempDir()
	a, err := New(Config{WorkDir: work, Lock: lock, Policy: &trust.Policy{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	exports := filepath.Join(work, "exports")
	if err := os.MkdirAll(exports, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(exports, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(exports, 0o755) })
	got, err := a.Acquire(ctx, fx.host+"/org/plugin:v1")
	if err == nil || got != nil {
		t.Fatalf("acquisition with an unwritable export cache returned %+v, %v", got, err)
	}
	if _, pinned := lock.Plugin(fx.host+"/org/plugin:v1", lockfile.SchemeOCI); pinned {
		t.Fatal("a pin was recorded for an acquisition whose export failed")
	}
}

// An image whose configuration states an environment entry that is
// not KEY=VALUE is refused at acquisition, naming the entry: no
// runner is handed an environment substrates read differently.
func TestAcquireRefusesBareEnv(t *testing.T) {
	fx := newFixture(t)
	pushIndexEnv(t, fx.host+"/org/bare:v1", []string{"A=1", "PB_SECRET"}, hostPlatform())
	a := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{}, nil)
	_, err := a.Acquire(ctx, fx.host+"/org/bare:v1")
	if err == nil || !strings.Contains(err.Error(), `"PB_SECRET" is not KEY=VALUE`) {
		t.Fatalf("bare env: %v", err)
	}
}

// An override from an OCI layout, a layout archive, or a docker-save
// tarball is materialized through the verifying seam with the
// lockfile untouched: no pin is written, and an entry's pin — even
// one naming another digest — is not consulted; the platform check
// and the policy still hold.
func TestAcquireOverride(t *testing.T) {
	fx := newFixture(t)
	lock := &lockfile.File{}
	if err := lock.AddPlugin(lockfile.PluginPin{Ref: fx.host + "/org/plugin:v1", Scheme: lockfile.SchemeOCI, Digest: "sha256:" + strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	a := newAcquirer(t, fx, lock, &trust.Policy{}, nil)
	ref := fx.host + "/org/plugin:v1"
	idx := indexFor(t, hostPlatform())
	layoutDir := t.TempDir()
	if _, err := layout.Write(layoutDir, idx); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "layout.tar")
	tarDir(t, layoutDir, archive)
	img, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	first, err := idx.Image(img.Manifests[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(t.TempDir(), "saved.tar")
	if err := tarball.WriteToFile(saved, name.MustParseReference("plugin:dev"), first); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{layoutDir, archive, saved} {
		got, err := a.AcquireOverride(ctx, ref, source)
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		if got.Process.Argv[0] != "/plugin" || got.Rootfs == "" || got.Pin.Ref != "" || got.Pin.Digest != "" {
			t.Fatalf("%s: %+v", source, got)
		}
	}
	if pin, _ := lock.Plugin(ref, lockfile.SchemeOCI); pin.Digest != "sha256:"+strings.Repeat("0", 64) || len(lock.Plugins) != 1 {
		t.Fatalf("the lockfile was touched: %+v", lock.Plugins)
	}
	// The platform check holds: a layout for another platform only.
	foreign := t.TempDir()
	if _, err := layout.Write(foreign, indexFor(t, v1.Platform{OS: "plan9", Architecture: "mips"})); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AcquireOverride(ctx, ref, foreign); err == nil || !strings.Contains(err.Error(), "has no "+HostPlatform().OS+"/"+HostPlatform().Arch+" entry in its manifest list") {
		t.Fatalf("foreign platform: %v", err)
	}
	// The policy holds: require-provenance without a verifier fails closed.
	strict := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{Default: trust.RequireProvenance}, nil)
	if _, err := strict.AcquireOverride(ctx, ref, layoutDir); !errors.Is(err, ErrNoImageVerifier) {
		t.Fatalf("require-provenance: %v", err)
	}
	if _, err := a.AcquireOverride(ctx, ref, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing source acquired")
	}
}

// indexFor builds a manifest list with one plugin image per platform.
func indexFor(t *testing.T, platforms ...v1.Platform) v1.ImageIndex {
	t.Helper()
	idx := v1.ImageIndex(empty.Index)
	for _, p := range platforms {
		img, err := random.Image(64, 1)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := img.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		cfg = cfg.DeepCopy()
		cfg.OS, cfg.Architecture = p.OS, p.Architecture
		cfg.Config = v1.Config{Entrypoint: []string{"/plugin"}, Env: []string{"A=1"}}
		img, err = mutate.ConfigFile(img, cfg)
		if err != nil {
			t.Fatal(err)
		}
		pl := p
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &pl}})
	}
	return idx
}

// tarDir writes dir as a tar archive of files and directories.
func tarDir(t *testing.T, dir, out string) {
	t.Helper()
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// An archive entry escaping the layout is refused before anything is
// written past it.
func TestUntarRefusesEscape(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "evil.tar")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for _, name := range []string{"oci-layout", "../evil"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte("x"))
	}
	tw.Close()
	f.Close()
	dir := t.TempDir()
	if err := untar(ctx, archive, dir); err == nil || !strings.Contains(err.Error(), "escapes the layout") {
		t.Fatalf("escape: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the escaping entry was written")
	}
}

// A layout whose descriptors carry no platform — a docker-save archive
// since Docker 25, a single-image layout — lists each image under the
// platform its configuration names and passes the platform check; an
// image naming none is refused.
func TestAcquireOverridePlatformless(t *testing.T) {
	fx := newFixture(t)
	a := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{}, nil)
	ref := fx.host + "/org/plugin:v1"
	bare := v1.ImageIndex(empty.Index)
	for _, p := range []v1.Platform{hostPlatform(), {OS: "plan9", Architecture: "mips"}} {
		img, err := random.Image(64, 1)
		if err != nil {
			t.Fatal(err)
		}
		cfg, _ := img.ConfigFile()
		cfg = cfg.DeepCopy()
		cfg.OS, cfg.Architecture = p.OS, p.Architecture
		cfg.Config = v1.Config{Entrypoint: []string{"/plugin"}}
		img, _ = mutate.ConfigFile(img, cfg)
		bare = mutate.AppendManifests(bare, mutate.IndexAddendum{Add: img})
	}
	dir := t.TempDir()
	if _, err := layout.Write(dir, bare); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AcquireOverride(ctx, ref, dir); err != nil {
		t.Fatalf("platform-less layout: %v", err)
	}
	archive := filepath.Join(t.TempDir(), "saved.tar")
	tarDir(t, dir, archive)
	if _, err := a.AcquireOverride(ctx, ref, archive); err != nil {
		t.Fatalf("platform-less archive: %v", err)
	}
	// No platform anywhere: refused naming the image.
	img, _ := random.Image(64, 1)
	cfg, _ := img.ConfigFile()
	cfg = cfg.DeepCopy()
	cfg.OS, cfg.Architecture = "", ""
	cfg.Config = v1.Config{Entrypoint: []string{"/plugin"}}
	img, _ = mutate.ConfigFile(img, cfg)
	none := t.TempDir()
	if _, err := layout.Write(none, mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: img})); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AcquireOverride(ctx, ref, none); err == nil || !strings.Contains(err.Error(), "names no platform") {
		t.Fatalf("no platform: %v", err)
	}
	saved := filepath.Join(t.TempDir(), "legacy.tar")
	if err := tarball.WriteToFile(saved, name.MustParseReference("plugin:dev"), img); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AcquireOverride(ctx, ref, saved); err == nil || !strings.Contains(err.Error(), "names no platform") {
		t.Fatalf("legacy tarball without a platform: %v", err)
	}
}

// An override is a plugin image like any: no entrypoint and a bare
// environment entry are refused; an archive holding anything but
// files and directories is refused.
func TestAcquireOverrideRefusals(t *testing.T) {
	fx := newFixture(t)
	a := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{}, nil)
	ref := fx.host + "/org/plugin:v1"
	build := func(cfgc v1.Config) string {
		img, _ := random.Image(64, 1)
		cfg, _ := img.ConfigFile()
		cfg = cfg.DeepCopy()
		cfg.OS, cfg.Architecture = hostPlatform().OS, hostPlatform().Architecture
		cfg.Config = cfgc
		img, _ = mutate.ConfigFile(img, cfg)
		dir := t.TempDir()
		if _, err := layout.Write(dir, mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: img})); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if _, err := a.AcquireOverride(ctx, ref, build(v1.Config{})); err == nil || !strings.Contains(err.Error(), "declares no entrypoint") {
		t.Fatalf("no entrypoint: %v", err)
	}
	if _, err := a.AcquireOverride(ctx, ref, build(v1.Config{Entrypoint: []string{"/plugin"}, Env: []string{"SECRET"}})); err == nil || !strings.Contains(err.Error(), "not KEY=VALUE") {
		t.Fatalf("bare env: %v", err)
	}
	archive := filepath.Join(t.TempDir(), "links.tar")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	tw.WriteHeader(&tar.Header{Name: "oci-layout", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	tw.Close()
	f.Close()
	if err := untar(ctx, archive, t.TempDir()); err == nil || !strings.Contains(err.Error(), "neither a file nor a directory") {
		t.Fatalf("symlink entry: %v", err)
	}
}

// The staging registry answers the store alone: a request without
// the acquirer's token is refused, the ping excepted.
func TestStagingRequiresToken(t *testing.T) {
	fx := newFixture(t)
	a := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{}, nil)
	for _, c := range []struct {
		path   string
		token  string
		status int
	}{
		{"/v2/", "", http.StatusOK},
		{"/v2/override/tags/list", "", http.StatusUnauthorized},
		{"/v2/override/tags/list", "wrong", http.StatusUnauthorized},
		{"/v2/override/tags/list", a.staging.token, http.StatusNotFound},
	} {
		req, _ := http.NewRequest("GET", "http://"+a.staging.host+c.path, nil)
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.status {
			t.Errorf("%s with token %q: %d, want %d", c.path, c.token, resp.StatusCode, c.status)
		}
	}
}

// A host refusing the loopback bind refuses overrides alone: ordinary
// acquisition runs, and an override names the staging failure.
func TestStagingBindFailureIsOverridesOnly(t *testing.T) {
	prev := listenStaging
	listenStaging = func() (net.Listener, error) { return nil, errors.New("loopback bind denied by policy") }
	t.Cleanup(func() { listenStaging = prev })
	fx := newFixture(t)
	a := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{}, nil)
	if _, err := a.Acquire(ctx, fx.host+"/org/plugin:v1"); err != nil {
		t.Fatalf("acquisition without staging: %v", err)
	}
	if _, err := a.AcquireOverride(ctx, fx.host+"/org/plugin:v1", t.TempDir()); err == nil || !strings.Contains(err.Error(), "override staging: loopback bind denied by policy") {
		t.Fatalf("override without staging: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

// A manifest list nesting a list without a platform is refused.
func TestWithPlatformsRefusesNestedList(t *testing.T) {
	inner := indexFor(t, hostPlatform())
	outer := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: inner})
	if _, err := withPlatforms(outer); err == nil || !strings.Contains(err.Error(), "nests a list without a platform") {
		t.Fatalf("nested list: %v", err)
	}
	platformed := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: inner, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}})
	if _, err := withPlatforms(platformed); err != nil {
		t.Fatalf("nested list with a platform: %v", err)
	}
}

// Under the daemon byte path an acquisition runs the seam and records
// the pin exactly as the store path does, but materializes nothing:
// it yields the repository at the verified digest for the daemon to
// pull; a pinned reference resolves at its pin and a moved tag is
// invisible; a mismatch fails closed (REQ-plugin-core-verifies).
func TestAcquireDaemonPull(t *testing.T) {
	fx := newFixture(t)
	lock := &lockfile.File{}
	work := t.TempDir()
	a, err := New(Config{WorkDir: work, Lock: lock, Policy: &trust.Policy{}, Pull: PullDaemon})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	ref := fx.host + "/org/plugin:v1"
	got, err := a.Acquire(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.Image != fx.host+"/org/plugin@"+fx.digest || got.Rootfs != "" || len(got.Process.Argv) != 0 {
		t.Fatalf("acquired %+v, want the repository at %s and nothing else", got, fx.digest)
	}
	if pin, ok := lock.Plugin(ref, lockfile.SchemeOCI); !ok || pin.Digest != fx.digest || got.Pin.Digest != pin.Digest {
		t.Fatalf("pin = %+v (%v), want %s", pin, ok, fx.digest)
	}
	// Nothing materialized: no layer content in the store's tiers, no
	// export, and no reference recorded — a store that may not reach
	// the network finds no image under the reference.
	filepath.WalkDir(work, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(work, p)
		top := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if top == "blobs" || top == "exports" || top == "layers" {
			t.Fatalf("a daemon-path acquisition materialized %s", rel)
		}
		return nil
	})
	never, err := ocifs.New(ocifs.WithWorkDir(work), ocifs.WithPullPolicy(ocifs.PullNever))
	if err != nil {
		t.Fatal(err)
	}
	defer never.Close()
	if _, err := never.Pull(ctx, ref); err == nil || !strings.Contains(err.Error(), "pull policy is 'Never'") {
		t.Fatalf("a daemon-path acquisition recorded the reference as acquired: %v", err)
	}
	// The tag moves; the pin holds and the daemon is handed the
	// pinned digest.
	pushIndex(t, ref, hostPlatform())
	again, err := a.Acquire(ctx, ref)
	if err != nil || again.Image != got.Image {
		t.Fatalf("pinned acquisition: %+v %v", again, err)
	}
	// A pin the registry contradicts fails closed.
	wrong := &lockfile.File{}
	if err := wrong.AddPlugin(lockfile.PluginPin{Ref: ref, Scheme: lockfile.SchemeOCI, Digest: "sha256:" + strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{WorkDir: t.TempDir(), Lock: wrong, Policy: &trust.Policy{}, Pull: PullDaemon})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	if _, err := b.Acquire(ctx, ref); err == nil || !strings.Contains(err.Error(), strings.Repeat("0", 64)) {
		t.Fatalf("a pin the registry does not hold: %v", err)
	}
}
