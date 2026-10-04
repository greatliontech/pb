package oci

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/gitprov/sigstoretest"
	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/provenance/image"
	"github.com/greatliontech/pb/internal/provenance/image/evidence"
	"github.com/greatliontech/pb/internal/testing/imagetest"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/greatliontech/ocifs"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/provenance/trust"
)

var ctx = context.Background()

// fixture is an in-process registry, served by fixtures at a
// reserved host, with a multi-platform index (host platform + one
// foreign) pushed at :v1. No socket is bound: the suite's registry
// round trips are calls, which is what lets a mutation oracle over
// the acquirer be attributed.
type fixture struct {
	host    string
	digest  string       // the index digest as pushed
	handler http.Handler // the registry, for a test to serve wrapped
}

// fixtures serves every fixture registry in this process, at a
// reserved host each, and has no transport beyond them: the suite
// dials nothing.
var fixtures = newInProcessTransport(nil)

var fixtureSerial atomic.Int64

func hostPlatform() v1.Platform { return v1Platform(plugin.HostPlatform()) }

// Daemon is the host's platform served by a daemon's pull.
func Daemon() []Candidate { return []Candidate{{Platform: plugin.HostPlatform(), Daemon: true}} }

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
	if err := remote.WriteIndex(r, idx, remote.WithTransport(fixtures)); err != nil {
		t.Fatal(err)
	}
	h, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return h.String()
}

// TestMain keeps the suite off the developer's own container
// configuration: the ambient credential store is an empty one unless
// a test writes its own.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pb-oci-docker-config-")
	if err != nil {
		panic(err)
	}
	// An empty configuration file: a directory holding none would
	// let the store fall through to the host's podman logins.
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o600); err != nil {
		panic(err)
	}
	os.Setenv("DOCKER_CONFIG", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// authenticated wraps a registry handler behind HTTP basic
// authentication: a request without the credential is answered 401
// with the challenge, as a private registry answers.
func authenticated(h http.Handler, user, pass string) http.Handler {
	return authenticatedWhere(h, user, pass, func(*http.Request) bool { return true })
}

// authenticatedWhere is authenticated for the requests guarded
// admits, the rest served openly.
func authenticatedWhere(h http.Handler, user, pass string, guarded func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); guarded(r) && (!ok || u != user || p != pass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// dockerConfig points the ambient credential store at a directory
// holding the configuration given, for the test's duration.
func dockerConfig(t *testing.T, config string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", dir)
}

// login is a configuration holding one basic login for host.
func login(host, user, pass string) string {
	return fmt.Sprintf(`{"auths":{"%s":{"auth":"%s"}}}`, host, base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
}

// A registry demanding a login is read under the ambient credential
// store (REQ-plugin-registry-credentials): with the host's login in
// the container tooling's configuration the acquisition succeeds and
// pins as any; with no login for the registry it fails naming the
// registry and the refusal.
func TestAcquireReadsTheAmbientCredentialStore(t *testing.T) {
	fx := newFixture(t)
	fixtures.serve(fx.host, authenticated(fx.handler, "reader", "s3cret"))
	ref := fx.host + "/org/plugin:v1"

	// No login: refused, the registry named.
	dockerConfig(t, `{}`)
	lock := &lockfile.File{}
	a := newAcquirer(t, fx, lock, &trust.Policy{}, nil)
	if _, err := a.Acquire(ctx, ref, Host()); err == nil || !strings.Contains(err.Error(), fx.host) || !strings.Contains(err.Error(), "401") {
		t.Fatalf("no login: %v", err)
	}
	if _, ok := lock.Plugin(ref, lockfile.SchemeOCI); ok {
		t.Fatal("a refused acquisition pinned")
	}
	a.Close()

	// The host's login in the ambient store: served and pinned.
	dockerConfig(t, login(fx.host, "reader", "s3cret"))
	lock = &lockfile.File{}
	a = newAcquirer(t, fx, lock, &trust.Policy{}, nil)
	if _, err := a.Acquire(ctx, ref, Host()); err != nil {
		t.Fatalf("with the login: %v", err)
	}
	if pin := pinOf(t, a, ref); pin.Digest != fx.digest {
		t.Fatalf("pin = %+v, want digest %s", pin, fx.digest)
	}
}

// A private repository serves its evidence under the same login
// (REQ-plugin-registry-credentials): with the image itself served
// openly and the evidence routes alone behind the login, the
// signature carriers are fetched with it and the record is made;
// without it the acquisition fails naming the registry and the
// refusal — never as an unsigned image, which allow-unsigned would
// pin with no record.
func TestAcquireEvidenceReadsTheAmbientCredentialStore(t *testing.T) {
	fx := newSignedFixture(t, true)
	bundle := fx.signBundle(t, signerSAN, signerIssuer, sigstoretest.BundleOptions{})
	evidence := func(r *http.Request) bool {
		return strings.Contains(r.URL.Path, "/referrers/") || strings.Contains(r.URL.Path, bundle.String())
	}
	fixtures.serve(fx.host, authenticatedWhere(fx.handler, "reader", "s3cret", evidence))
	ref := fx.host + "/org/plugin:v1"
	for _, posture := range []trust.Mode{trust.RequireProvenance, trust.AllowUnsigned} {
		governed := &trust.Policy{Plugins: []trust.Rule{rule(fx.host+"/org", posture, signerSAN)}}

		dockerConfig(t, login(fx.host, "reader", "s3cret"))
		lock := &lockfile.File{}
		a := newAcquirer(t, fx.fixture, lock, governed, fx.sig.TrustedRoot())
		if _, err := a.Acquire(ctx, ref, Host()); err != nil {
			t.Fatalf("%v with the login: %v", posture, err)
		}
		if pin, ok := lock.Plugin(ref, lockfile.SchemeOCI); !ok || pin.Provenance != imageRecord(signerSAN) {
			t.Fatalf("%v: recorded pin = %+v %v", posture, pin, ok)
		}
		a.Close()

		dockerConfig(t, `{}`)
		lock = &lockfile.File{}
		a = newAcquirer(t, fx.fixture, lock, governed, fx.sig.TrustedRoot())
		if _, err := a.Acquire(ctx, ref, Host()); err == nil || !strings.Contains(err.Error(), fx.host) || !strings.Contains(err.Error(), "401") {
			t.Fatalf("%v with no login: %v", posture, err)
		}
		if _, ok := lock.Plugin(ref, lockfile.SchemeOCI); ok {
			t.Fatalf("%v with no login pinned", posture)
		}
		a.Close()
	}
}

// The override staging is read anonymously: a credential helper the
// ambient store names but the host cannot run stops no override
// (REQ-plugin-registry-credentials names registries, and the staging
// is none the user named).
func TestAcquireOverrideReadsNoCredentialStore(t *testing.T) {
	dockerConfig(t, `{"credsStore":"pbnonexistenthelper"}`)
	fx := newFixture(t)
	a := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{}, nil)
	layoutDir := t.TempDir()
	if _, err := layout.Write(layoutDir, indexFor(t, hostPlatform())); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AcquireOverride(ctx, fx.host+"/org/plugin:v1", layoutDir, Host()); err != nil {
		t.Fatalf("an override under a broken credential helper: %v", err)
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	host := fmt.Sprintf("fixture%d%s", fixtureSerial.Add(1), reservedDomain)
	h := imagetest.Handler(false)
	fixtures.serve(host, h)
	t.Cleanup(func() { fixtures.serve(host, nil) })
	digest := pushIndex(t, host+"/org/plugin:v1", hostPlatform(), v1.Platform{OS: "plan9", Architecture: "mips"})
	return &fixture{host: host, digest: digest, handler: h}
}

// pinOf reads the pin an acquisition recorded from the acquirer's own
// lockfile, the record every consumer reads.
func pinOf(t *testing.T, a *Acquirer, ref string) lockfile.PluginPin {
	t.Helper()
	pin, ok := a.lock.Plugin(ref, lockfile.SchemeOCI)
	if !ok {
		t.Fatalf("no pin recorded for %s", ref)
	}
	return pin
}

func newAcquirer(t *testing.T, fx *fixture, lock *lockfile.File, policy *trust.Policy, root *gitprov.TrustedRoot) *Acquirer {
	t.Helper()
	return newAcquirerKeeping(t, fx, lock, policy, root, "")
}

// newAcquirerKeeping is newAcquirer with evidence kept under dir.
func newAcquirerKeeping(t *testing.T, fx *fixture, lock *lockfile.File, policy *trust.Policy, root *gitprov.TrustedRoot, dir string) *Acquirer {
	t.Helper()
	return newAcquirerAt(t, fx, lock, policy, root, t.TempDir(), dir)
}

// newAcquirerAt is newAcquirerKeeping over the store at workDir, so a
// later acquirer can find the store warm; the caller closes the
// earlier one first.
func newAcquirerAt(t *testing.T, fx *fixture, lock *lockfile.File, policy *trust.Policy, root *gitprov.TrustedRoot, workDir, dir string) *Acquirer {
	t.Helper()
	a, err := New(Config{
		WorkDir:     workDir,
		EvidenceDir: dir,
		Lock:        lock,
		Policy:      policy,
		TrustedRoot: root,
		Transport:   fixtures,
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
	got, err := a.Acquire(ctx, fx.host+"/org/plugin:v1", Host())
	if err != nil {
		t.Fatal(err)
	}
	if pin := pinOf(t, a, fx.host+"/org/plugin:v1"); pin.Digest != fx.digest || pin.Scheme != lockfile.SchemeOCI || pin.Provenance != (lockfile.Provenance{}) {
		t.Fatalf("pin = %+v, want digest %s provenance none", pin, fx.digest)
	}
	if len(got.Process.Argv) != 1 || got.Process.Argv[0] != "/plugin" || len(got.Process.Env) != 1 {
		t.Fatalf("image config not surfaced: %+v", got)
	}
	if _, ok := lock.Plugin(fx.host+"/org/plugin:v1", lockfile.SchemeOCI); !ok {
		t.Fatal("pin not recorded")
	}
	// The store path exports: nothing for a daemon to pull.
	if _, ok := got.Image.(*plugin.Export); !ok {
		t.Fatalf("a store acquisition's image = %+v, want an export", got.Image)
	}
	fi, err := os.Stat(rootfsOf(t, got))
	if err != nil || !fi.IsDir() {
		t.Fatalf("rootfs %q: %v", rootfsOf(t, got), err)
	}
	entries, err := os.ReadDir(rootfsOf(t, got))
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
	first, err := a.Acquire(ctx, ref, Host())
	if err != nil {
		t.Fatal(err)
	}
	// Move the tag to different content.
	moved := pushIndex(t, ref, hostPlatform())
	if moved == fx.digest {
		t.Fatal("fixture: tag did not move")
	}
	// Over a cold store, so the pin and not the store's own memory of
	// the tag holds the digest.
	cold := newAcquirerAt(t, fx, lock, &trust.Policy{}, nil, t.TempDir(), "")
	again, err := cold.Acquire(ctx, ref, Host())
	if err != nil {
		t.Fatal(err)
	}
	if pinOf(t, cold, ref).Digest != fx.digest {
		t.Fatalf("pinned acquire served %s, want the pinned %s", pinOf(t, cold, ref).Digest, fx.digest)
	}
	// What materializes is the pinned image: the export is content-
	// addressed, so the same child the first acquisition yielded, not
	// the content the tag moved to.
	if filepath.Base(rootfsOf(t, again)) != filepath.Base(rootfsOf(t, first)) {
		t.Fatalf("the pinned acquisition materialized %s, the first %s", rootfsOf(t, again), rootfsOf(t, first))
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
	_, err := a.Acquire(ctx, ref, Host())
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
	_, err := a.Acquire(ctx, foreign, Host())
	if err == nil || !strings.Contains(err.Error(), "does not support this platform") || !strings.Contains(err.Error(), "plan9/mips") {
		t.Fatalf("foreign platform: %v", err)
	}

	bare := fx.host + "/org/bare:v1"
	img, _ := random.Image(64, 1)
	r, _ := name.ParseReference(bare)
	if err := remote.Write(r, img, remote.WithTransport(fixtures)); err != nil {
		t.Fatal(err)
	}
	_, err = a.Acquire(ctx, bare, Host())
	if err == nil || !strings.Contains(err.Error(), "not a manifest list") {
		t.Fatalf("bare manifest: %v", err)
	}

	// Several entries for the host — variants of one architecture —
	// are refused: choosing among them would be a fallback.
	host := hostPlatform()
	several := fx.host + "/org/several:v1"
	pushIndex(t, several, v1.Platform{OS: host.OS, Architecture: host.Architecture, Variant: "v1"}, v1.Platform{OS: host.OS, Architecture: host.Architecture, Variant: "v2"})
	_, err = a.Acquire(ctx, several, Host())
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
	a, err := New(Config{WorkDir: t.TempDir(), Lock: &lockfile.File{}, Policy: &trust.Policy{}, Transport: fixtures})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	got, err := a.Acquire(ctx, ref, Daemon())
	if err != nil {
		t.Fatal(err)
	}
	if want := host.OS + "/" + host.Architecture + "/v9"; entryOf(t, got) != want {
		t.Fatalf("admitted entry = %q, want %q", entryOf(t, got), want)
	}
	plain := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{}, nil)
	if got, err := plain.Acquire(ctx, fx.host+"/org/plugin:v1", Host()); err != nil || entryOf(t, got) != host.OS+"/"+host.Architecture {
		t.Fatalf("an entry without a variant: %+v %v", got, err)
	}
	// The store path exports that same child: the one entry that
	// matched, whatever its variant — its own marker is in the export.
	got, err = plain.Acquire(ctx, ref, Host())
	if err != nil || entryOf(t, got) != host.OS+"/"+host.Architecture+"/v9" {
		t.Fatalf("the store path's admitted child: %+v %v", got, err)
	}
	if marker, err := os.ReadFile(filepath.Join(rootfsOf(t, got), "platform")); err != nil || string(marker) != host.OS+"/"+host.Architecture+"/v9" {
		t.Fatalf("the export is not the admitted child's: %q %v", marker, err)
	}
}

// signedFixture is a fixture whose registry answers the referrers API
// or leaves the client to the fallback tag, with a synthetic sigstore
// to sign its image.
type signedFixture struct {
	*fixture
	sig  *sigstoretest.Sigstore
	repo name.Repository
}

func newSignedFixture(t *testing.T, referrersAPI bool) *signedFixture {
	t.Helper()
	host := fmt.Sprintf("fixture%d%s", fixtureSerial.Add(1), reservedDomain)
	h := imagetest.Handler(referrersAPI)
	fixtures.serve(host, h)
	t.Cleanup(func() { fixtures.serve(host, nil) })
	digest := pushIndex(t, host+"/org/plugin:v1", hostPlatform())
	repo, err := name.NewRepository(host + "/org/plugin")
	if err != nil {
		t.Fatal(err)
	}
	return &signedFixture{fixture: &fixture{host: host, digest: digest, handler: h}, sig: sigstoretest.New(t), repo: repo}
}

func (fx *signedFixture) hash(t *testing.T) v1.Hash {
	t.Helper()
	h, err := v1.NewHash(fx.digest)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// signBundle attaches a bundle over the image's digest, signed by
// subject and issuer, as cosign's default sign would.
func (fx *signedFixture) signBundle(t *testing.T, subject, issuer string, o sigstoretest.BundleOptions) v1.Hash {
	t.Helper()
	return fx.signBundleOrdered(t, subject, issuer, o, imagetest.Anywhere, v1.Hash{})
}

// signBundleOrdered is signBundle with the referrer's digest placed
// before or after pivot's.
func (fx *signedFixture) signBundleOrdered(t *testing.T, subject, issuer string, o sigstoretest.BundleOptions, order imagetest.Order, pivot v1.Hash) v1.Hash {
	t.Helper()
	b := fx.sig.Bundle(t, fx.digest, subject, issuer, o)
	return imagetest.AttachOrdered(t, fx.repo, fx.hash(t), imagetest.BundleArtifact(b, imagetest.CosignSignPredicate), order, pivot, remote.WithTransport(fixtures))
}

const (
	signerSAN    = "https://github.com/acme/plugin/.github/workflows/release.yml@refs/tags/v1"
	signerIssuer = "https://token.actions.githubusercontent.com"
)

func rule(prefix string, require trust.Mode, san string) trust.Rule {
	return trust.Rule{Prefix: prefix, Require: require, Identity: &trust.IdentityRule{SAN: san, Issuer: signerIssuer}}
}

func imageRecord(san string) lockfile.Provenance {
	return lockfile.Provenance{Type: lockfile.ProvenanceImageSignature, SAN: san, Issuer: signerIssuer}
}

// Under require-provenance the image's evidence decides
// (REQ-plugin-verify-before-run, REQ-prov-plugin-signature): with no
// identity rule nothing is judged and the acquisition fails naming it
// (REQ-prov-plugin-identity); with no trusted root an unsigned image
// fails as absent and a signed one as a judgement with no root to
// make it; an unsigned image fails as absent; a signature by the rule's identity
// is recorded as an image-signature record; one by another signer is
// not accepted; rejected evidence aborts. Nothing failed is pinned.
func TestAcquireProvenancePolicy(t *testing.T) {
	for _, api := range []bool{true, false} {
		t.Run(map[bool]string{true: "referrers API", false: "fallback tag"}[api], func(t *testing.T) {
			fx := newSignedFixture(t, api)
			ref := fx.host + "/org/plugin:v1"
			governed := &trust.Policy{Plugins: []trust.Rule{rule(fx.host+"/org", trust.RequireProvenance, signerSAN)}}

			noRule := newAcquirer(t, fx.fixture, &lockfile.File{}, &trust.Policy{Default: trust.RequireProvenance}, fx.sig.TrustedRoot())
			if _, err := noRule.Acquire(ctx, ref, Host()); !errors.Is(err, ErrNoIdentityRule) {
				t.Fatalf("require without an identity rule: %v", err)
			}
			noRoot := newAcquirer(t, fx.fixture, &lockfile.File{}, governed, nil)
			if _, err := noRoot.Acquire(ctx, ref, Host()); !errors.Is(err, image.ErrNoEvidence) {
				t.Fatalf("require without a trusted root, the image unsigned: %v", err)
			}
			unsigned := newAcquirer(t, fx.fixture, &lockfile.File{}, governed, fx.sig.TrustedRoot())
			if _, err := unsigned.Acquire(ctx, ref, Host()); !errors.Is(err, image.ErrNoEvidence) {
				t.Fatalf("require on an unsigned image: %v", err)
			}

			fx.signBundle(t, "someone@example.com", signerIssuer, sigstoretest.BundleOptions{})
			noRootSigned := newAcquirer(t, fx.fixture, &lockfile.File{}, governed, nil)
			if _, err := noRootSigned.Acquire(ctx, ref, Host()); !errors.Is(err, ErrNoTrustedRoot) {
				t.Fatalf("evidence found with no trusted root: %v", err)
			}
			lock := &lockfile.File{}
			other := newAcquirer(t, fx.fixture, lock, governed, fx.sig.TrustedRoot())
			if _, err := other.Acquire(ctx, ref, Host()); !errors.Is(err, image.ErrIdentityNotAccepted) {
				t.Fatalf("another signer: %v", err)
			}
			if len(lock.Plugins) != 0 {
				t.Fatal("a refused acquisition left a pin")
			}

			fx.signBundle(t, signerSAN, signerIssuer, sigstoretest.BundleOptions{})
			signed := newAcquirer(t, fx.fixture, lock, governed, fx.sig.TrustedRoot())
			if _, err := signed.Acquire(ctx, ref, Host()); err != nil {
				t.Fatal(err)
			}
			if pin, ok := lock.Plugin(ref, lockfile.SchemeOCI); !ok || pin.Provenance != imageRecord(signerSAN) {
				t.Fatalf("recorded pin = %+v %v", pin, ok)
			}

			// Rejected evidence reached before an accepted carrier aborts;
			// carriers past the accepted one are never fetched.
			corrupt := newSignedFixture(t, api)
			accepted := corrupt.signBundle(t, signerSAN, signerIssuer, sigstoretest.BundleOptions{})
			corrupt.signBundleOrdered(t, signerSAN, signerIssuer, sigstoretest.BundleOptions{TwoSignatures: true}, imagetest.After, accepted)
			corruptPolicy := &trust.Policy{Plugins: []trust.Rule{rule(corrupt.host+"/org", trust.RequireProvenance, signerSAN)}}
			a3 := newAcquirer(t, corrupt.fixture, &lockfile.File{}, corruptPolicy, corrupt.sig.TrustedRoot())
			if got, err := a3.Acquire(ctx, corrupt.host+"/org/plugin:v1", Host()); err != nil || pinOf(t, a3, corrupt.host+"/org/plugin:v1").Provenance != imageRecord(signerSAN) {
				t.Fatalf("rejected evidence past the accepted carrier: %+v %v", got, err)
			}
			corrupt.signBundleOrdered(t, signerSAN, signerIssuer, sigstoretest.BundleOptions{TwoSignatures: true}, imagetest.Before, accepted)
			lock3 := &lockfile.File{}
			a4 := newAcquirer(t, corrupt.fixture, lock3, corruptPolicy, corrupt.sig.TrustedRoot())
			if _, err := a4.Acquire(ctx, corrupt.host+"/org/plugin:v1", Host()); err == nil || !strings.Contains(err.Error(), "evidence rejected") {
				t.Fatalf("rejected evidence before the accepted carrier: %v", err)
			}
			if len(lock3.Plugins) != 0 {
				t.Fatal("failed verification left a pin")
			}
		})
	}
}

// Under allow-unsigned the evidence is still judged: what verifies is
// recorded; a tolerable non-acceptance — no identity rule, another
// signer, no evidence — records none; rejected evidence aborts even
// here (REQ-prov-plugin-classification, REQ-prov-unsigned-recorded).
func TestAcquireOpportunisticVerification(t *testing.T) {
	fx := newSignedFixture(t, true)
	ref := fx.host + "/org/plugin:v1"
	fx.signBundle(t, signerSAN, signerIssuer, sigstoretest.BundleOptions{})
	tolerant := &trust.Policy{Plugins: []trust.Rule{rule(fx.host+"/org", trust.AllowUnsigned, signerSAN)}}

	a := newAcquirer(t, fx.fixture, &lockfile.File{}, tolerant, fx.sig.TrustedRoot())
	if got, err := a.Acquire(ctx, ref, Host()); err != nil || pinOf(t, a, ref).Provenance != imageRecord(signerSAN) {
		t.Fatalf("verified evidence not recorded under allow-unsigned: %+v %v", got, err)
	}
	noRule := newAcquirer(t, fx.fixture, &lockfile.File{}, &trust.Policy{}, fx.sig.TrustedRoot())
	if got, err := noRule.Acquire(ctx, ref, Host()); err != nil || pinOf(t, noRule, ref).Provenance != (lockfile.Provenance{}) {
		t.Fatalf("a signed image with no identity rule: %+v %v", got, err)
	}
	otherRule := &trust.Policy{Plugins: []trust.Rule{rule(fx.host+"/org", trust.AllowUnsigned, "https://github.com/other/**")}}
	other := newAcquirer(t, fx.fixture, &lockfile.File{}, otherRule, fx.sig.TrustedRoot())
	if got, err := other.Acquire(ctx, ref, Host()); err != nil || pinOf(t, other, ref).Provenance != (lockfile.Provenance{}) {
		t.Fatalf("identity non-acceptance not tolerated as none: %+v %v", got, err)
	}
	// Evidence found with no trusted root to judge it fails under
	// allow-unsigned too; an unsigned image with no root is none.
	noRoot := newAcquirer(t, fx.fixture, &lockfile.File{}, tolerant, nil)
	if _, err := noRoot.Acquire(ctx, ref, Host()); !errors.Is(err, ErrNoTrustedRoot) || len(noRoot.lock.Plugins) != 0 {
		t.Fatalf("a signed image with no trusted root: %v, pins %+v", err, noRoot.lock.Plugins)
	}
	unsignedFx := newSignedFixture(t, true)
	unsignedNoRoot := newAcquirer(t, unsignedFx.fixture, &lockfile.File{}, &trust.Policy{Plugins: []trust.Rule{rule(unsignedFx.host+"/org", trust.AllowUnsigned, signerSAN)}}, nil)
	if got, err := unsignedNoRoot.Acquire(ctx, unsignedFx.host+"/org/plugin:v1", Host()); err != nil || pinOf(t, unsignedNoRoot, unsignedFx.host+"/org/plugin:v1").Provenance != (lockfile.Provenance{}) {
		t.Fatalf("an unsigned image with no trusted root: %+v %v", got, err)
	}

	bare := newSignedFixture(t, true)
	a2 := newAcquirer(t, bare.fixture, &lockfile.File{}, &trust.Policy{Plugins: []trust.Rule{rule(bare.host+"/org", trust.AllowUnsigned, signerSAN)}}, bare.sig.TrustedRoot())
	if got, err := a2.Acquire(ctx, bare.host+"/org/plugin:v1", Host()); err != nil || pinOf(t, a2, bare.host+"/org/plugin:v1").Provenance != (lockfile.Provenance{}) {
		t.Fatalf("absence not tolerated as none: %+v %v", got, err)
	}

	corrupt := newSignedFixture(t, true)
	corrupt.signBundle(t, signerSAN, signerIssuer, sigstoretest.BundleOptions{TwoSignatures: true})
	a3 := newAcquirer(t, corrupt.fixture, &lockfile.File{}, &trust.Policy{Plugins: []trust.Rule{rule(corrupt.host+"/org", trust.AllowUnsigned, signerSAN)}}, corrupt.sig.TrustedRoot())
	if _, err := a3.Acquire(ctx, corrupt.host+"/org/plugin:v1", Host()); err == nil || !strings.Contains(err.Error(), "evidence rejected") {
		t.Fatalf("rejected evidence tolerated under allow-unsigned: %v", err)
	}
}

// The longest-prefix plugins rule's identity is the one evidence is
// held to (REQ-prov-policy-eval at the plugin surface).
func TestAcquireIdentityRuleFlows(t *testing.T) {
	fx := newSignedFixture(t, true)
	ref := fx.host + "/org/plugin:v1"
	fx.signBundle(t, signerSAN, signerIssuer, sigstoretest.BundleOptions{})
	policy := &trust.Policy{Plugins: []trust.Rule{
		rule(fx.host+"/org", trust.RequireProvenance, "https://github.com/other/**"),
		rule(fx.host+"/org/plugin", trust.RequireProvenance, "https://github.com/acme/plugin/**"),
	}}
	a := newAcquirer(t, fx.fixture, &lockfile.File{}, policy, fx.sig.TrustedRoot())
	if got, err := a.Acquire(ctx, ref, Host()); err != nil || pinOf(t, a, ref).Provenance != imageRecord(signerSAN) {
		t.Fatalf("the longest-prefix rule's identity did not govern: %+v %v", got, err)
	}
	reversed := &trust.Policy{Plugins: []trust.Rule{
		rule(fx.host+"/org", trust.RequireProvenance, "https://github.com/acme/plugin/**"),
		rule(fx.host+"/org/plugin", trust.RequireProvenance, "https://github.com/other/**"),
	}}
	b := newAcquirer(t, fx.fixture, &lockfile.File{}, reversed, fx.sig.TrustedRoot())
	if _, err := b.Acquire(ctx, ref, Host()); !errors.Is(err, image.ErrIdentityNotAccepted) {
		t.Fatalf("the shorter rule's identity governed: %v", err)
	}
}

// cosign's legacy location — the signature tag's simple-signing
// layers — is evidence too, and the whole acquisition makes no round
// trip beyond the acquirer's own transport: the judgement is offline
// (REQ-prov-plugin-carriers, REQ-prov-offline).
func TestAcquireLegacySignatureTag(t *testing.T) {
	fx := newSignedFixture(t, false)
	ref := fx.host + "/org/plugin:v1"
	e := fx.sig.Envelope(t, fx.digest, signerSAN, signerIssuer, sigstoretest.EnvelopeOptions{})
	imagetest.Tag(t, fx.repo, imagetest.SignatureTag(fx.hash(t)), []imagetest.Layer{imagetest.EnvelopeLayer(e)}, remote.WithTransport(fixtures))
	sigstoretest.RefuseNetwork(t)
	governed := &trust.Policy{Plugins: []trust.Rule{rule(fx.host+"/org", trust.RequireProvenance, signerSAN)}}
	a := newAcquirer(t, fx.fixture, &lockfile.File{}, governed, fx.sig.TrustedRoot())
	if got, err := a.Acquire(ctx, ref, Host()); err != nil || pinOf(t, a, ref).Provenance != imageRecord(signerSAN) {
		t.Fatalf("the legacy tag's envelope: %+v %v", got, err)
	}
}

// unreachable makes the fixture's registry refuse every round trip —
// with a status the client does not retry, so the refusal is prompt.
func (fx *signedFixture) unreachable(t *testing.T) {
	t.Helper()
	fixtures.serve(fx.host, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "the registry is unreachable", http.StatusForbidden)
	}))
}

// Evidence fetched for an image is kept with its content and judged
// from there: an acquisition whose kept evidence the policy accepts
// makes no round trip; kept evidence the policy no longer accepts is
// refetched, what the fetch takes replacing it; a fetch that takes
// nothing keeps nothing (REQ-prov-plugin-evidence-kept).
func TestAcquireKeepsEvidence(t *testing.T) {
	fx := newSignedFixture(t, true)
	ref := fx.host + "/org/plugin:v1"
	kept := t.TempDir()
	governed := &trust.Policy{Plugins: []trust.Rule{rule(fx.host+"/org", trust.RequireProvenance, signerSAN)}}

	// Unsigned: nothing found, nothing kept, asked again each time.
	if _, err := newAcquirerKeeping(t, fx.fixture, &lockfile.File{}, governed, fx.sig.TrustedRoot(), kept).Acquire(ctx, ref, Host()); !errors.Is(err, image.ErrNoEvidence) {
		t.Fatalf("unsigned: %v", err)
	}
	if entries, _ := os.ReadDir(kept); len(entries) != 0 {
		t.Fatalf("an unsigned image left kept evidence: %v", entries)
	}

	fx.signBundle(t, signerSAN, signerIssuer, sigstoretest.BundleOptions{})
	lock := &lockfile.File{}
	workDir := t.TempDir()
	first := newAcquirerAt(t, fx.fixture, lock, governed, fx.sig.TrustedRoot(), workDir, kept)
	if got, err := first.Acquire(ctx, ref, Host()); err != nil || pinOf(t, first, ref).Provenance != imageRecord(signerSAN) {
		t.Fatalf("first use: %+v %v", got, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(kept, "sha256")); len(entries) != 1 {
		t.Fatalf("the fetched evidence was not kept: %v", entries)
	}
	first.Close()

	// The registry gone, the store warm: the kept evidence carries the
	// acquisition.
	fx.unreachable(t)
	offline := newAcquirerAt(t, fx.fixture, lock, governed, fx.sig.TrustedRoot(), workDir, kept)
	if got, err := offline.Acquire(ctx, ref, Host()); err != nil || pinOf(t, offline, ref).Provenance != imageRecord(signerSAN) {
		t.Fatalf("offline with kept evidence: %+v %v", got, err)
	}
	offline.Close()
	// Without kept evidence the same acquisition needs the registry.
	unkept := newAcquirerAt(t, fx.fixture, lock, governed, fx.sig.TrustedRoot(), workDir, "")
	if _, err := unkept.Acquire(ctx, ref, Host()); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("offline without kept evidence: %v", err)
	}
	unkept.Close()
	// A policy the kept evidence does not satisfy refetches, and the
	// registry gone fails the acquisition.
	other := &trust.Policy{Plugins: []trust.Rule{rule(fx.host+"/org", trust.RequireProvenance, "https://github.com/other/**")}}
	tightened := newAcquirerAt(t, fx.fixture, &lockfile.File{}, other, fx.sig.TrustedRoot(), workDir, kept)
	if _, err := tightened.Acquire(ctx, ref, Host()); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("a tightened policy offline: %v", err)
	}
	tightened.Close()
	// A pinned record the kept evidence no longer bears refetches too:
	// with the registry gone, the run fails rather than passing.
	if _, err := newAcquirerAt(t, fx.fixture, lock, other, fx.sig.TrustedRoot(), workDir, kept).Acquire(ctx, ref, Host()); err == nil {
		t.Fatal("a pinned record judged from kept evidence the policy refuses passed")
	}
}

// What a fetch takes replaces what was kept, and a fetch that rejects
// keeps nothing, so kept evidence is never a rejected fetch's residue
// (REQ-prov-plugin-evidence-kept).
func TestAcquireKeptEvidenceReplaced(t *testing.T) {
	fx := newSignedFixture(t, true)
	ref := fx.host + "/org/plugin:v1"
	kept := t.TempDir()
	const otherSAN = "https://github.com/acme/plugin/.github/workflows/release.yml@refs/tags/v2"
	accepting := func(san string) *trust.Policy {
		return &trust.Policy{Plugins: []trust.Rule{rule(fx.host+"/org", trust.RequireProvenance, san)}}
	}
	firstReferrer := fx.signBundle(t, signerSAN, signerIssuer, sigstoretest.BundleOptions{})
	if _, err := newAcquirerKeeping(t, fx.fixture, &lockfile.File{}, accepting(signerSAN), fx.sig.TrustedRoot(), kept).Acquire(ctx, ref, Host()); err != nil {
		t.Fatal(err)
	}
	store := evidence.Store{Dir: kept}
	h, err := v1.NewHash(fx.digest)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := store.Load(h)

	// A second signer's bundle appears, its referrer sorting after the
	// first's; a policy accepting only it refuses the kept evidence,
	// fetches, passes the first over, accepts the second, and keeps
	// both in that order.
	fx.signBundleOrdered(t, otherSAN, signerIssuer, sigstoretest.BundleOptions{}, imagetest.After, firstReferrer)
	workDir := t.TempDir()
	pinnedToSecond := &lockfile.File{}
	second := newAcquirerAt(t, fx.fixture, pinnedToSecond, accepting(otherSAN), fx.sig.TrustedRoot(), workDir, kept)
	if got, err := second.Acquire(ctx, ref, Host()); err != nil || pinOf(t, second, ref).Provenance != imageRecord(otherSAN) {
		t.Fatalf("the second signer: %+v %v", got, err)
	}
	second.Close()
	after, _ := store.Load(h)
	// What the fetch took is kept, up to the accepted carrier: the
	// passed-over first signer's, then the accepted second signer's.
	if len(before) != 1 || len(after) != 2 {
		t.Fatalf("kept evidence not replaced by what the fetch took: before %v, after %v", before, after)
	}
	if _, err := gitprov.VerifyImage(ctx, fx.digest, after[0].Value, gitprov.Identity{Subject: signerSAN, Issuer: signerIssuer}, fx.sig.TrustedRoot()); err != nil {
		t.Fatalf("the passed-over carrier is not the first signer's: %v", err)
	}
	if _, err := gitprov.VerifyImage(ctx, fx.digest, after[1].Value, gitprov.Identity{Subject: otherSAN, Issuer: signerIssuer}, fx.sig.TrustedRoot()); err != nil {
		t.Fatalf("the last kept carrier is not the accepted signer's: %v", err)
	}
	// An unwritable store changes no outcome.
	sealed := filepath.Join(t.TempDir(), "sealed")
	if err := os.Mkdir(sealed, 0o500); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		unwritable := newAcquirerKeeping(t, fx.fixture, &lockfile.File{}, accepting(otherSAN), fx.sig.TrustedRoot(), sealed)
		if got, err := unwritable.Acquire(ctx, ref, Host()); err != nil || pinOf(t, unwritable, ref).Provenance != imageRecord(otherSAN) {
			t.Fatalf("an unwritable evidence store changed the outcome: %+v %v", got, err)
		}
	}
	// The kept judgement holds the pin's record too: under a policy
	// accepting both signers, the kept first signer's carrier is
	// passed over for the one reproducing the pin, with no round trip.
	fx.unreachable(t)
	broad := accepting("https://github.com/acme/plugin/**")
	offline := newAcquirerAt(t, fx.fixture, pinnedToSecond, broad, fx.sig.TrustedRoot(), workDir, kept)
	if got, err := offline.Acquire(ctx, ref, Host()); err != nil || pinOf(t, offline, ref).Provenance != imageRecord(otherSAN) {
		t.Fatalf("the pinned record over kept evidence offline: %+v %v", got, err)
	}
	offline.Close()
	// A rejected fetch keeps nothing new: the kept evidence stays.
	corrupt := newSignedFixture(t, true)
	corruptRef := corrupt.host + "/org/plugin:v1"
	corrupt.signBundle(t, signerSAN, signerIssuer, sigstoretest.BundleOptions{TwoSignatures: true})
	corruptKept := t.TempDir()
	if _, err := newAcquirerKeeping(t, corrupt.fixture, &lockfile.File{}, &trust.Policy{Plugins: []trust.Rule{rule(corrupt.host+"/org", trust.RequireProvenance, signerSAN)}}, corrupt.sig.TrustedRoot(), corruptKept).Acquire(ctx, corruptRef, Host()); err == nil || !strings.Contains(err.Error(), "evidence rejected") {
		t.Fatalf("rejected: %v", err)
	}
	if entries, _ := os.ReadDir(corruptKept); len(entries) != 0 {
		t.Fatalf("a rejected fetch kept evidence: %v", entries)
	}
}

// An explicit update re-resolves the tag and rewrites the pin: the
// digest to what the tag names now, the record to what a fresh fetch
// judged, kept evidence replaced; a reference with no pin fails; an
// image the policy refuses fails and leaves the pin
// (REQ-dep-update, REQ-lock-no-silent-downgrade).
func TestUpdatePlugin(t *testing.T) {
	fx := newSignedFixture(t, true)
	ref := fx.host + "/org/plugin:v1"
	kept := t.TempDir()
	const otherSAN = "https://github.com/acme/plugin/.github/workflows/release.yml@refs/tags/v2"
	both := &trust.Policy{Plugins: []trust.Rule{rule(fx.host+"/org", trust.RequireProvenance, "https://github.com/acme/plugin/**")}}
	fx.signBundle(t, signerSAN, signerIssuer, sigstoretest.BundleOptions{})
	lock := &lockfile.File{}
	workDir := t.TempDir()
	first := newAcquirerAt(t, fx.fixture, lock, both, fx.sig.TrustedRoot(), workDir, kept)
	if _, _, err := first.UpdatePlugin(ctx, ref, Host()); err == nil || !strings.Contains(err.Error(), "no pin to update") {
		t.Fatalf("an unpinned reference updated: %v", err)
	}
	if _, err := first.Acquire(ctx, ref, Host()); err != nil {
		t.Fatal(err)
	}
	firstPin := pinOf(t, first, ref)
	first.Close()
	oldDigest := fx.digest

	// The tag moves to a new image, signed by another signer.
	fx.digest = pushIndex(t, ref, hostPlatform())
	otherReferrer := fx.signBundle(t, otherSAN, signerIssuer, sigstoretest.BundleOptions{})
	// An acquisition keeps the pin: the tag is never re-resolved
	// implicitly (REQ-plugin-digest-pin) — over a cold store, so the
	// pin and not the cache holds it.
	still := newAcquirerAt(t, fx.fixture, lock, both, fx.sig.TrustedRoot(), t.TempDir(), kept)
	if again, err := still.Acquire(ctx, ref, Host()); err != nil || pinOf(t, still, ref).Digest != oldDigest {
		t.Fatalf("an acquisition re-resolved the tag: %+v %v", again, err)
	}
	still.Close()
	updater := newAcquirerAt(t, fx.fixture, lock, both, fx.sig.TrustedRoot(), workDir, kept)
	before, after, err := updater.UpdatePlugin(ctx, ref, Host())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, firstPin) || after.Digest != fx.digest || after.Provenance != imageRecord(otherSAN) || after.Ref != ref || after.Scheme != lockfile.SchemeOCI {
		t.Fatalf("update = %+v -> %+v", before, after)
	}
	if pin, ok := lock.Plugin(ref, lockfile.SchemeOCI); !ok || !reflect.DeepEqual(pin, after) {
		t.Fatalf("the pin was not rewritten: %+v", pin)
	}
	h, err := v1.NewHash(fx.digest)
	if err != nil {
		t.Fatal(err)
	}
	if carriers, ok := (evidence.Store{Dir: kept}).Load(h); !ok || len(carriers) != 1 {
		t.Fatalf("the update kept nothing for the new digest: %v %v", carriers, ok)
	}
	updater.Close()
	// An acquisition at the moved pin runs the new image under its
	// new record, the kept evidence reproducing it.
	moved := newAcquirerAt(t, fx.fixture, lock, both, fx.sig.TrustedRoot(), workDir, kept)
	if got, err := moved.Acquire(ctx, ref, Host()); err != nil || pinOf(t, moved, ref).Digest != fx.digest || pinOf(t, moved, ref).Provenance != imageRecord(otherSAN) {
		t.Fatalf("an acquisition at the moved pin: %+v %v", got, err)
	}
	moved.Close()

	// The tag unmoved, a signature the judgement prefers added: the
	// update fetches anew rather than judging the kept evidence, so
	// the record follows the registry.
	const thirdSAN = "https://github.com/acme/plugin/.github/workflows/release.yml@refs/tags/v3"
	fx.signBundleOrdered(t, thirdSAN, signerIssuer, sigstoretest.BundleOptions{}, imagetest.Before, otherReferrer)
	refreshing := newAcquirerAt(t, fx.fixture, lock, both, fx.sig.TrustedRoot(), workDir, kept)
	if _, after3, err := refreshing.UpdatePlugin(ctx, ref, Host()); err != nil || after3.Digest != fx.digest || after3.Provenance != imageRecord(thirdSAN) {
		t.Fatalf("the update judged kept evidence instead of fetching: %+v %v", after3, err)
	}
	refreshing.Close()

	// The tag moves again, to an unsigned image: under
	// require-provenance the update fails and the pin stands.
	fx.digest = pushIndex(t, ref, hostPlatform())
	refusing := newAcquirerAt(t, fx.fixture, lock, both, fx.sig.TrustedRoot(), workDir, kept)
	if _, _, err := refusing.UpdatePlugin(ctx, ref, Host()); !errors.Is(err, image.ErrNoEvidence) {
		t.Fatalf("an unsigned image under require-provenance updated: %v", err)
	}
	if pin, _ := lock.Plugin(ref, lockfile.SchemeOCI); pin.Digest != after.Digest || pin.Provenance != imageRecord(thirdSAN) {
		t.Fatalf("a refused update moved the pin: %+v", pin)
	}
	refusing.Close()
	// Under allow-unsigned the same update rewrites the record to
	// none: the explicit update, not a silent downgrade.
	tolerant := &trust.Policy{Plugins: []trust.Rule{rule(fx.host+"/org", trust.AllowUnsigned, "https://github.com/acme/plugin/**")}}
	downgrading := newAcquirerAt(t, fx.fixture, lock, tolerant, fx.sig.TrustedRoot(), workDir, kept)
	if _, after2, err := downgrading.UpdatePlugin(ctx, ref, Host()); err != nil || after2.Digest != fx.digest || after2.Provenance != (lockfile.Provenance{}) {
		t.Fatalf("the explicit update to an unsigned image: %+v %v", after2, err)
	}
}

// A pinned image is judged on every acquisition, and its record must
// still hold: evidence that no longer bears the recorded identity
// fails the acquisition rather than passing under a stale record
// (REQ-plugin-verify-before-run, REQ-lock-no-silent-downgrade).
func TestAcquireReverifiesPinnedRecord(t *testing.T) {
	fx := newSignedFixture(t, true)
	ref := fx.host + "/org/plugin:v1"
	fx.signBundle(t, signerSAN, signerIssuer, sigstoretest.BundleOptions{})
	lock := &lockfile.File{}
	governed := &trust.Policy{Plugins: []trust.Rule{rule(fx.host+"/org", trust.AllowUnsigned, signerSAN)}}
	first := newAcquirer(t, fx.fixture, lock, governed, fx.sig.TrustedRoot())
	if got, err := first.Acquire(ctx, ref, Host()); err != nil || pinOf(t, first, ref).Provenance != imageRecord(signerSAN) {
		t.Fatalf("first use: %+v %v", got, err)
	}
	// The same pin, the same image, the evidence the same: verified again.
	again := newAcquirer(t, fx.fixture, lock, governed, fx.sig.TrustedRoot())
	if got, err := again.Acquire(ctx, ref, Host()); err != nil || pinOf(t, again, ref).Provenance != imageRecord(signerSAN) {
		t.Fatalf("re-acquisition: %+v %v", got, err)
	}
	// A second signature the policy also accepts, sorting before the
	// recorded one, does not displace the record: the carrier
	// reproducing it is the one taken.
	recorded := fx.signBundleOrdered(t, signerSAN, signerIssuer, sigstoretest.BundleOptions{}, imagetest.Anywhere, v1.Hash{})
	const otherSAN = "https://github.com/acme/plugin/.github/workflows/release.yml@refs/tags/v2"
	fx.signBundleOrdered(t, otherSAN, signerIssuer, sigstoretest.BundleOptions{}, imagetest.Before, recorded)
	broad := &trust.Policy{Plugins: []trust.Rule{rule(fx.host+"/org", trust.AllowUnsigned, "https://github.com/acme/plugin/**")}}
	both := newAcquirer(t, fx.fixture, lock, broad, fx.sig.TrustedRoot())
	if got, err := both.Acquire(ctx, ref, Host()); err != nil || pinOf(t, both, ref).Provenance != imageRecord(signerSAN) {
		t.Fatalf("a second accepted signer displaced the record: %+v %v", got, err)
	}
	// A policy under which only the other signer is accepted leaves
	// the pinned record unsupported: refused naming the identity, the
	// record kept.
	otherRule := &trust.Policy{Plugins: []trust.Rule{rule(fx.host+"/org", trust.AllowUnsigned, otherSAN)}}
	stale := newAcquirer(t, fx.fixture, lock, otherRule, fx.sig.TrustedRoot())
	if _, err := stale.Acquire(ctx, ref, Host()); !errors.Is(err, lockfile.ErrProvenanceDowngrade) || !strings.Contains(err.Error(), otherSAN) {
		t.Fatalf("a pinned record no longer borne: %v", err)
	}
	// A policy accepting neither: refused as absent, the record kept.
	none := &trust.Policy{Plugins: []trust.Rule{rule(fx.host+"/org", trust.AllowUnsigned, "https://github.com/other/**")}}
	gone := newAcquirer(t, fx.fixture, lock, none, fx.sig.TrustedRoot())
	if _, err := gone.Acquire(ctx, ref, Host()); !errors.Is(err, lockfile.ErrProvenanceDowngrade) {
		t.Fatalf("a pinned record with no accepted evidence: %v", err)
	}
	if pin, ok := lock.Plugin(ref, lockfile.SchemeOCI); !ok || pin.Provenance != imageRecord(signerSAN) {
		t.Fatalf("the record was rewritten: %+v", pin)
	}
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
	a, err := New(Config{WorkDir: work, Lock: lock, Policy: &trust.Policy{}, Transport: fixtures})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	exports := filepath.Join(work, "exports")
	if err := os.MkdirAll(exports, 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("a directory cannot be made unwritable by its mode on windows")
	}
	if err := os.Chmod(exports, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(exports, 0o755) })
	got, err := a.Acquire(ctx, fx.host+"/org/plugin:v1", Host())
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
	_, err := a.Acquire(ctx, fx.host+"/org/bare:v1", Host())
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
	seeded, _ := lock.Plugin(ref, lockfile.SchemeOCI)
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
		got, err := a.AcquireOverride(ctx, ref, source, Host())
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		// An override materializes without pinning: the stale pin the
		// lockfile holds for the reference is exactly as it was.
		if pin, _ := a.lock.Plugin(ref, lockfile.SchemeOCI); got.Process.Argv[0] != "/plugin" || rootfsOf(t, got) == "" || !reflect.DeepEqual(pin, seeded) {
			t.Fatalf("%s: %+v (pin now %+v)", source, got, pin)
		}
	}
	if len(lock.Plugins) != 1 {
		t.Fatalf("the lockfile gained a pin: %+v", lock.Plugins)
	}
	// The platform check holds: a layout for another platform only.
	foreign := t.TempDir()
	if _, err := layout.Write(foreign, indexFor(t, v1.Platform{OS: "plan9", Architecture: "mips"})); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AcquireOverride(ctx, ref, foreign, Host()); err == nil || !strings.Contains(err.Error(), "has no "+plugin.HostPlatform().OS+"/"+plugin.HostPlatform().Arch+" entry in its manifest list") {
		t.Fatalf("foreign platform: %v", err)
	}
	// The policy holds: require-provenance with no identity rule fails
	// closed, and with one the override's evidence is sought where it
	// was staged, which holds none.
	strict := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{Default: trust.RequireProvenance}, nil)
	if _, err := strict.AcquireOverride(ctx, ref, layoutDir, Host()); !errors.Is(err, ErrNoIdentityRule) {
		t.Fatalf("require-provenance without an identity rule: %v", err)
	}
	// Evidence for the override's own digest attached at the declared
	// reference's repository is not the override's: the override was
	// staged elsewhere, and there is nothing.
	sfx := newSignedFixture(t, true)
	overrideDigest, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	// The override's own index sits in the declared repository too,
	// with a signature attached, so only the repository decides.
	if err := remote.WriteIndex(sfx.repo.Tag("elsewhere"), idx, remote.WithTransport(fixtures)); err != nil {
		t.Fatal(err)
	}
	imagetest.Attach(t, sfx.repo, overrideDigest, imagetest.BundleArtifact(sfx.sig.Bundle(t, overrideDigest.String(), signerSAN, signerIssuer, sigstoretest.BundleOptions{}), imagetest.CosignSignPredicate), remote.WithTransport(fixtures))
	governed := &trust.Policy{Plugins: []trust.Rule{rule(sfx.host+"/org", trust.RequireProvenance, signerSAN)}}
	judged := newAcquirer(t, sfx.fixture, &lockfile.File{}, governed, sfx.sig.TrustedRoot())
	if _, err := judged.AcquireOverride(ctx, sfx.host+"/org/plugin:v1", layoutDir, Host()); !errors.Is(err, image.ErrNoEvidence) {
		t.Fatalf("require-provenance on an override: %v", err)
	}
	if _, err := a.AcquireOverride(ctx, ref, filepath.Join(t.TempDir(), "missing"), Host()); err == nil {
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
	if _, err := a.AcquireOverride(ctx, ref, dir, Host()); err != nil {
		t.Fatalf("platform-less layout: %v", err)
	}
	archive := filepath.Join(t.TempDir(), "saved.tar")
	tarDir(t, dir, archive)
	if _, err := a.AcquireOverride(ctx, ref, archive, Host()); err != nil {
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
	if _, err := a.AcquireOverride(ctx, ref, none, Host()); err == nil || !strings.Contains(err.Error(), "names no platform") {
		t.Fatalf("no platform: %v", err)
	}
	saved := filepath.Join(t.TempDir(), "legacy.tar")
	if err := tarball.WriteToFile(saved, name.MustParseReference("plugin:dev"), img); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AcquireOverride(ctx, ref, saved, Host()); err == nil || !strings.Contains(err.Error(), "names no platform") {
		t.Fatalf("legacy tarball without a platform: %v", err)
	}
}

// An override is a plugin image like any: no process and a bare
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
	if _, err := a.AcquireOverride(ctx, ref, build(v1.Config{}), Host()); err == nil || !strings.Contains(err.Error(), "declares no process") {
		t.Fatalf("no process: %v", err)
	}
	if _, err := a.AcquireOverride(ctx, ref, build(v1.Config{Entrypoint: []string{"/plugin"}, Env: []string{"SECRET"}}), Host()); err == nil || !strings.Contains(err.Error(), "not KEY=VALUE") {
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

// closeRecorder is a request body that records its close.
type closeRecorder struct {
	io.Reader
	closed bool
}

func (c *closeRecorder) Close() error { c.closed = true; return nil }

// recordingBase is a transport beyond the in-process hosts that
// records what reaches it and answers nothing.
type recordingBase struct{ hosts []string }

func (b *recordingBase) RoundTrip(req *http.Request) (*http.Response, error) {
	b.hosts = append(b.hosts, req.URL.Host)
	if req.Body != nil {
		req.Body.Close()
	}
	return nil, errors.New("beyond this process")
}

// The in-process transport keeps the round tripper's contract: the
// caller's request is served through a copy — a handler mutating
// what it is served leaves the caller's headers and URL as they
// were — its body left in place and closed after the trip, and a
// request without a body is served one, so a handler reading it
// sees the wire's non-nil body.
func TestInProcessTransportKeepsTheCallersRequest(t *testing.T) {
	seen := make(chan *http.Request, 1)
	mutating := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Served", "mutated")
		r.URL.Path = "/mutated"
		seen <- r
		w.WriteHeader(http.StatusNoContent)
	})
	tr := newInProcessTransport(nil)
	host := "contract" + reservedDomain
	tr.serve(host, mutating)
	body := &closeRecorder{Reader: strings.NewReader("payload")}
	req, err := http.NewRequest(http.MethodPost, "https://"+host+"/v2/", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Body = body
	req.Header.Set("X-Served", "caller")
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	served := <-seen
	if served == req {
		t.Fatal("the handler was served the caller's own request")
	}
	if got := req.Header.Get("X-Served"); got != "caller" {
		t.Fatalf("the caller's header reads %q after the handler mutated its copy", got)
	}
	if req.URL.Path != "/v2/" {
		t.Fatalf("the caller's path reads %q after the handler mutated its copy", req.URL.Path)
	}
	if req.Body != body {
		t.Fatal("the caller's body was replaced")
	}
	if !body.closed {
		t.Fatal("the caller's body was not closed")
	}
	if resp.Request != req {
		t.Fatal("the response does not name the caller's request")
	}
	bare, err := http.NewRequest(http.MethodGet, "https://"+host+"/v2/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.RoundTrip(bare); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got.Body == nil {
		t.Fatal("a request without a body served without one")
	}
}

// A host the transport does not hold goes to the transport beyond
// it, and with none is refused with its body closed — a suite built
// on the transport alone dials nothing; a released host is refused
// the same way.
func TestInProcessTransportRefusesUnknownHosts(t *testing.T) {
	host := "held" + reservedDomain
	held := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	get := func(tr http.RoundTripper, host string) error {
		body := &closeRecorder{Reader: strings.NewReader("")}
		req, err := http.NewRequest(http.MethodGet, "https://"+host+"/v2/", body)
		if err != nil {
			t.Fatal(err)
		}
		req.Body = body
		resp, err := tr.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
		if !body.closed {
			t.Fatalf("%s: the body left open", host)
		}
		return err
	}

	alone := newInProcessTransport(nil)
	alone.serve(host, held)
	if err := get(alone, host); err != nil {
		t.Fatalf("a held host: %v", err)
	}
	if err := get(alone, "unknown"+reservedDomain); err == nil || !strings.Contains(err.Error(), "no transport beyond it") {
		t.Fatalf("an unknown host with no transport beyond: %v", err)
	}
	alone.serve(host, nil)
	if err := get(alone, host); err == nil || !strings.Contains(err.Error(), "no transport beyond it") {
		t.Fatalf("a released host: %v", err)
	}

	base := &recordingBase{}
	beyond := newInProcessTransport(base)
	beyond.serve(host, held)
	if err := get(beyond, host); err != nil {
		t.Fatalf("a held host beside a base: %v", err)
	}
	if err := get(beyond, "example.com"); err == nil || err.Error() != "beyond this process" {
		t.Fatalf("an unknown host beside a base: %v", err)
	}
	if len(base.hosts) != 1 || base.hosts[0] != "example.com" {
		t.Fatalf("the base saw %v, want the one unknown host", base.hosts)
	}
}

// With no transport handed, the acquirer's round trips beyond this
// process go through the registry client's own transport, as the
// store's own default does (ocifs REQ-api-construction).
func TestAcquirerDefaultsToTheRegistryClientsTransport(t *testing.T) {
	a, err := New(Config{WorkDir: t.TempDir(), Lock: &lockfile.File{}, Policy: &trust.Policy{}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.transport.base != remote.DefaultTransport {
		t.Fatal("the transport beyond this process is not the registry client's own")
	}
}

// The staging is a registry in this process: its host is under the
// reserved domain no resolver answers (RFC 2606), an override staged
// in one acquirer is invisible to another, and an acquisition
// through the same transport still reaches the fixture registry
// beside it.
func TestStagingIsInProcess(t *testing.T) {
	fx := newFixture(t)
	a := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{}, nil)
	b := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{}, nil)
	dir := t.TempDir()
	if _, err := layout.Write(dir, indexFor(t, hostPlatform())); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AcquireOverride(ctx, fx.host+"/org/plugin:v1", dir, Host()); err != nil {
		t.Fatal(err)
	}
	staged, err := name.ParseReference(stagingRepo + ":override")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Index(staged, remote.WithTransport(a.transport)); err != nil {
		t.Fatalf("the staged index through its own acquirer's transport: %v", err)
	}
	if _, err := remote.Index(staged, remote.WithTransport(b.transport)); err == nil {
		t.Fatal("an override staged in one acquirer is visible to another")
	}
	if !strings.HasSuffix(stagingHost, ".invalid") {
		t.Fatalf("the staging host %q is not under the reserved domain", stagingHost)
	}
	if _, err := b.Acquire(ctx, fx.host+"/org/plugin:v1", Host()); err != nil {
		t.Fatalf("an acquisition beside the staging: %v", err)
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
	a, err := New(Config{WorkDir: work, Lock: lock, Policy: &trust.Policy{}, Transport: fixtures})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	ref := fx.host + "/org/plugin:v1"
	got, err := a.Acquire(ctx, ref, Daemon())
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := got.Image.(*plugin.Pulled); !ok || p.Reference() != fx.host+"/org/plugin@"+fx.digest || len(got.Process.Argv) != 0 {
		t.Fatalf("acquired %+v, want the repository at %s and nothing else", got, fx.digest)
	}
	if pin, ok := lock.Plugin(ref, lockfile.SchemeOCI); !ok || pin.Digest != fx.digest {
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
	again, err := a.Acquire(ctx, ref, Daemon())
	if err != nil || pulledRef(t, again) != pulledRef(t, got) {
		t.Fatalf("pinned acquisition: %+v %v", again, err)
	}
	// A pin the registry contradicts fails closed.
	wrong := &lockfile.File{}
	if err := wrong.AddPlugin(lockfile.PluginPin{Ref: ref, Scheme: lockfile.SchemeOCI, Digest: "sha256:" + strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{WorkDir: t.TempDir(), Lock: wrong, Policy: &trust.Policy{}, Transport: fixtures})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	if _, err := b.Acquire(ctx, ref, Daemon()); err == nil || !strings.Contains(err.Error(), strings.Repeat("0", 64)) {
		t.Fatalf("a pin the registry does not hold: %v", err)
	}
}

// rootfsOf is an export's rootfs, the acquisition's world an export.
func rootfsOf(t *testing.T, a *plugin.Acquired) string {
	t.Helper()
	e, ok := a.Image.(*plugin.Export)
	if !ok {
		t.Fatalf("image %+v is no export", a.Image)
	}
	return e.Rootfs
}

// pulledRef is a pulled image's reference, the acquisition's world a
// pulled image.
func pulledRef(t *testing.T, a *plugin.Acquired) string {
	t.Helper()
	p, ok := a.Image.(*plugin.Pulled)
	if !ok {
		t.Fatalf("image %+v is no pulled image", a.Image)
	}
	return p.Reference()
}

// entryOf is the admitted entry the acquisition's world carries: an
// export's or a pulled image's.
func entryOf(t *testing.T, a *plugin.Acquired) string {
	t.Helper()
	switch img := a.Image.(type) {
	case *plugin.Export:
		return img.Entry
	case *plugin.Pulled:
		return img.Entry
	}
	t.Fatalf("image %+v carries no admitted entry", a.Image)
	return ""
}

// The acquisition serves the first candidate the image serves, in
// the order offered: an image serving the second alone is acquired
// for it — the store asked again for that platform after the seam's
// verdict — by the daemon's pull where that candidate's byte path is
// the daemon's, by the store's export otherwise; a first candidate
// the image serves with several entries is refused, never stepped
// over; an image serving no candidate, or offered none, is refused
// naming what it serves and the candidates (REQ-plugin-runner-selection,
// REQ-plugin-platform-strict).
func TestAcquireCandidates(t *testing.T) {
	fx := newFixture(t)
	host := hostPlatform()
	foreign := plugin.Platform{OS: "plan9", Arch: "mips"}
	ref := fx.host + "/org/second:v1"
	pushIndex(t, ref, v1.Platform{OS: "plan9", Architecture: "mips", Variant: "v7"})
	a := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{}, nil)
	got, err := a.Acquire(ctx, ref, []Candidate{{Platform: plugin.HostPlatform()}, {Platform: foreign}})
	if err != nil {
		t.Fatalf("the second candidate served: %v", err)
	}
	if got.Candidate != 1 || entryOf(t, got) != "plan9/mips/v7" || rootfsOf(t, got) == "" {
		t.Fatalf("acquired for candidate %d, entry %q", got.Candidate, entryOf(t, got))
	}
	got, err = a.Acquire(ctx, ref, []Candidate{{Platform: plugin.HostPlatform()}, {Platform: foreign, Daemon: true}})
	if err != nil {
		t.Fatalf("the second candidate served by the daemon: %v", err)
	}
	if p, ok := got.Image.(*plugin.Pulled); !ok || got.Candidate != 1 || p.Entry != "plan9/mips/v7" {
		t.Fatalf("the daemon candidate's world: %+v (candidate %d)", got.Image, got.Candidate)
	}
	// The first candidate served stays first, whatever follows.
	both := fx.host + "/org/both:v1"
	pushIndex(t, both, v1.Platform{OS: "plan9", Architecture: "mips"}, v1.Platform{OS: host.OS, Architecture: host.Architecture})
	if got, err := a.Acquire(ctx, both, []Candidate{{Platform: plugin.HostPlatform()}, {Platform: foreign}}); err != nil || got.Candidate != 0 || entryOf(t, got) != host.OS+"/"+host.Architecture {
		t.Fatalf("the first candidate served: %v, candidate %d", err, got.Candidate)
	}
	// Several entries for the first candidate: refused, the second
	// not consulted.
	several := fx.host + "/org/several2:v1"
	pushIndex(t, several, v1.Platform{OS: host.OS, Architecture: host.Architecture, Variant: "v1"}, v1.Platform{OS: host.OS, Architecture: host.Architecture, Variant: "v2"}, v1.Platform{OS: "plan9", Architecture: "mips"})
	if _, err := a.Acquire(ctx, several, []Candidate{{Platform: plugin.HostPlatform()}, {Platform: foreign}}); err == nil || !strings.Contains(err.Error(), "2 entries for "+host.OS+"/"+host.Architecture) {
		t.Fatalf("several entries for the first candidate: %v", err)
	}
	// No candidate served: the refusal names the candidates and what
	// the image serves; none offered: what it serves alone.
	_, err = a.Acquire(ctx, ref, []Candidate{{Platform: plugin.HostPlatform()}, {Platform: plugin.Platform{OS: "linux", Arch: "s390x"}}})
	if !errors.Is(err, ErrNoCandidate) || !strings.Contains(err.Error(), "has no entry for ["+host.OS+"/"+host.Architecture+" linux/s390x] in its manifest list (found [plan9/mips/v7])") {
		t.Fatalf("no candidate served: %v", err)
	}
	_, err = a.Acquire(ctx, ref, nil)
	if !errors.Is(err, ErrNoCandidate) || !strings.Contains(err.Error(), "serves [plan9/mips/v7], and no runner here runs any of them") {
		t.Fatalf("no candidate offered: %v", err)
	}
	// An update resolves alone and follows the seam's verdict to
	// whichever candidate the image serves: the pin moves.
	if _, _, err := a.UpdatePlugin(ctx, ref, []Candidate{{Platform: plugin.HostPlatform()}, {Platform: foreign}}); err != nil {
		t.Fatalf("an update over two candidates: %v", err)
	}
	// An image serving another platform alone, the host's candidate
	// alone: the one-platform refusal, as the host's own.
	_, err = a.Acquire(ctx, ref, Host())
	if !errors.Is(err, ErrNoCandidate) || !strings.Contains(err.Error(), "has no "+host.OS+"/"+host.Architecture+" entry in its manifest list (found [plan9/mips/v7]): the image does not support this platform") {
		t.Fatalf("the host alone: %v", err)
	}
}

// The plugin process is the image's: the argv its entrypoint
// followed by its cmd, the environment and the working directory as
// the configuration declares them; an image declaring no process
// is refused (REQ-plugin-runner-independence).
func TestProcessOf(t *testing.T) {
	cfg := &v1.ConfigFile{Config: v1.Config{Entrypoint: []string{"/jre/bin/java", "-jar"}, Cmd: []string{"plugin.jar", ""}, Env: []string{"A=1"}, WorkingDir: "/w"}}
	got, err := processOf(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Argv, "|") != "/jre/bin/java|-jar|plugin.jar|" || strings.Join(got.Env, " ") != "A=1" || got.WorkDir != "/w" {
		t.Fatalf("process = %+v", got)
	}
	// A cmd alone is the process, as an OCI runtime runs such an
	// image; neither is no process.
	if got, err := processOf(&v1.ConfigFile{Config: v1.Config{Cmd: []string{"x"}}}); err != nil || strings.Join(got.Argv, "|") != "x" {
		t.Fatalf("an image with a cmd alone: %+v %v", got, err)
	}
	if _, err := processOf(&v1.ConfigFile{}); err == nil || !strings.Contains(err.Error(), "no process") {
		t.Fatalf("an image declaring no process: %v", err)
	}
}

// An acquisition's export offers the verified image's archive in
// either form a daemon's image store loads, the identity returned
// the one that form's store reports: the configuration's digest
// under the docker-archive's manifest.json, the manifest's under
// the OCI layout's index (ocifs api.md REQ-api-archive).
func TestAcquireExportArchive(t *testing.T) {
	fx := newFixture(t)
	a := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{}, nil)
	got, err := a.Acquire(ctx, fx.host+"/org/plugin:v1", Host())
	if err != nil {
		t.Fatal(err)
	}
	export, ok := got.Image.(*plugin.Export)
	if !ok || export.Archive == nil {
		t.Fatalf("acquired %+v, want an export offering its archive", got.Image)
	}
	var docker bytes.Buffer
	id, err := export.Archive(ctx, &docker, plugin.DockerArchive)
	if err != nil {
		t.Fatal(err)
	}
	entries := tarEntries(t, docker.Bytes())
	var manifest []struct{ Config string }
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil || len(manifest) != 1 || "sha256:"+strings.TrimSuffix(manifest[0].Config, ".json") != id {
		t.Fatalf("the docker-archive's manifest names %s, the identity returned %s (%v)", entries["manifest.json"], id, err)
	}
	var oci bytes.Buffer
	id, err = export.Archive(ctx, &oci, plugin.OCILayout)
	if err != nil {
		t.Fatal(err)
	}
	entries = tarEntries(t, oci.Bytes())
	var index struct {
		Manifests []struct{ Digest string }
	}
	if err := json.Unmarshal(entries["index.json"], &index); err != nil || len(index.Manifests) != 1 || index.Manifests[0].Digest != id {
		t.Fatalf("the OCI layout's index names %s, the identity returned %s (%v)", entries["index.json"], id, err)
	}
	if _, ok := entries["blobs/"+strings.Replace(id, ":", "/", 1)]; !ok {
		t.Fatalf("the OCI layout carries no manifest blob %s: %v", id, slices.Sorted(maps.Keys(entries)))
	}
}

// tarEntries reads a tar's regular files by name.
func tarEntries(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	entries := map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return entries
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		entries[h.Name] = body
	}
}

// A first use asks the registry what the tag names today and pins
// that, never the store's memory of the tag from an earlier
// resolution: a tag moved since another root's first use is pinned
// where it points now (REQ-plugin-digest-pin, REQ-lock-first-use).
func TestAcquireFirstUseAsksTheRegistry(t *testing.T) {
	fx := newFixture(t)
	ref := fx.host + "/org/plugin:v1"
	a := newAcquirer(t, fx, &lockfile.File{}, &trust.Policy{}, nil)
	if _, err := a.Acquire(ctx, ref, Host()); err != nil {
		t.Fatal(err)
	}
	// The store holding the tag's row — a pull by the tag, as an
	// acquisition never makes one — the row is what a store's own
	// policy would answer the next first use from.
	if err := a.enter(ref, &acquisition{declaredRef: ref, candidates: Host()}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.fs.Pull(ctx, ref); err != nil {
		t.Fatal(err)
	}
	a.leave(ref)
	moved := pushIndex(t, ref, hostPlatform())
	if moved == fx.digest {
		t.Fatal("fixture: tag did not move")
	}
	// Another root's first use over the same warm store.
	a.lock = &lockfile.File{}
	if _, err := a.Acquire(ctx, ref, Host()); err != nil {
		t.Fatal(err)
	}
	if got := pinOf(t, a, ref).Digest; got != moved {
		t.Fatalf("the second root's first use pinned %s, want the registry's %s", got, moved)
	}
}
