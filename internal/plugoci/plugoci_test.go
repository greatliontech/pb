package plugoci

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
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
	t.Helper()
	idx := v1.ImageIndex(empty.Index)
	for _, p := range platforms {
		img, err := random.Image(64, 1)
		if err != nil {
			t.Fatal(err)
		}
		img, err = mutate.ConfigFile(img, &v1.ConfigFile{OS: p.OS, Architecture: p.Architecture, Config: v1.Config{Entrypoint: []string{"/plugin"}, Env: []string{"A=1"}}})
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
