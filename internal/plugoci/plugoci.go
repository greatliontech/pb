// Package plugoci materializes oci-scheme plugin images through the
// image store (ocifs) with pb as the verifying core
// (plugin-execution.md REQ-plugin-core-verifies): pin resolution,
// trust evaluation, and the platform check run in the store's
// verification seam — after top-level resolution, before any content
// is materialized — so runners only ever execute what the seam
// admitted. The store is pb's, not a runner's: the seam's bytes are
// the manifest list pb fetched, retained under the store's
// digest-verified boundary.
package plugoci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"

	"github.com/google/go-containerregistry/pkg/authn"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/greatliontech/ocifs"
	"github.com/greatliontech/pb/internal/genfile"
	"github.com/greatliontech/pb/internal/lockfile"
	"github.com/greatliontech/pb/internal/plugexec"
	"github.com/greatliontech/pb/internal/trust"
)

// ErrNoImageVerifier marks a require-provenance acquisition with no
// image-signature verifier available: the requirement fails closed
// (docs/issues/image-signature-verifier-home.md tracks the verifier).
var ErrNoImageVerifier = errors.New("plugoci: trust policy requires provenance but no image-signature verifier is available")

// Evidence-classification sentinels an ImageVerifier wraps so the
// policy arms can tell tolerable absence from tampering — the module
// pipeline's three-way classification at the plugin surface: absence
// and identity non-acceptance are tolerable under allow-unsigned;
// anything else is rejected evidence and aborts even there.
var (
	ErrNoEvidence          = errors.New("plugoci: no provenance evidence found")
	ErrIdentityNotAccepted = errors.New("plugoci: evidence identity not accepted by policy")
)

// ImageVerifier verifies a plugin image's provenance evidence offline
// against pb's trusted root: a sigstore signature over the
// manifest-list digest (provenance.md REQ-prov-plugin-signature).
type ImageVerifier interface {
	VerifyImage(ctx context.Context, reference, digest string, identity *trust.IdentityRule) (lockfile.Provenance, error)
}

// Platform is the host platform an acquisition enforces
// (REQ-plugin-platform-strict).
type Platform struct {
	OS   string
	Arch string
}

// HostPlatform is the running host's platform.
func HostPlatform() Platform {
	return Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// Config assembles an Acquirer. WorkDir roots the image store;
// Verifier may be nil — require-provenance then fails closed; a zero
// Platform means the host's.
type Config struct {
	WorkDir     string
	Lock        *lockfile.File
	Policy      *trust.Policy
	Verifier    ImageVerifier
	Platform    Platform
	Credentials map[string]authn.AuthConfig
	// Pull is the byte path an acquisition yields (plugin-execution.md
	// REQ-plugin-core-verifies): the zero value exports from the store.
	Pull PullMode
}

// PullMode is the byte path by which a verified image reaches the
// runner: pb's store, or a Docker daemon pulling the verified digest.
type PullMode int

const (
	// PullStore exports the verified image from pb's store.
	PullStore PullMode = iota
	// PullDaemon verifies without materializing and yields the
	// repository at the verified digest for the daemon to pull.
	PullDaemon
)

// Acquirer materializes plugin images: verified through the seam,
// pinned in the lockfile, exported to a root filesystem.
type Acquirer struct {
	fs         *ocifs.OCIFS
	staging    *staging // nil where the loopback bind failed; stagingErr says why
	stagingErr error
	lock       *lockfile.File
	policy     *trust.Policy
	verifier   ImageVerifier
	platform   Platform
	pull       PullMode

	mu      sync.Mutex
	pending map[string]*acquisition // digest-or-tag target -> in-flight state
}

// acquisition carries one Acquire call's seam contract and results.
type acquisition struct {
	declaredRef  string // the reference as declared in generation config
	pinnedDigest string // "" on first use
	resolved     string
	provenance   lockfile.Provenance
}

// New constructs the acquirer and its verifying store.
func New(cfg Config) (*Acquirer, error) {
	if cfg.Lock == nil || cfg.Policy == nil {
		return nil, errors.New("plugoci: acquirer needs a lockfile and a trust policy")
	}
	platform := cfg.Platform
	if platform == (Platform{}) {
		platform = HostPlatform()
	}
	a := &Acquirer{
		lock:     cfg.Lock,
		policy:   cfg.Policy,
		verifier: cfg.Verifier,
		platform: platform,
		pull:     cfg.Pull,
		pending:  map[string]*acquisition{},
	}
	opts := []ocifs.Option{
		ocifs.WithWorkDir(cfg.WorkDir),
		ocifs.WithDefaultPlatform(v1.Platform{OS: platform.OS, Architecture: platform.Arch}),
		ocifs.WithVerifier(a.verify),
	}
	// The override staging binds a loopback port whose credential the
	// store's keychain must hold from construction; a host refusing
	// the bind refuses overrides alone, never acquisition.
	if st, err := newStaging(); err != nil {
		a.stagingErr = err
	} else {
		a.staging = st
		opts = append(opts, ocifs.WithAuthSource(st.repo, st.auth()))
	}
	for prefix, auth := range cfg.Credentials {
		opts = append(opts, ocifs.WithAuthSource(prefix, auth))
	}
	fs, err := ocifs.New(opts...)
	if err != nil {
		if a.staging != nil {
			a.staging.close()
		}
		return nil, err
	}
	a.fs = fs
	return a, nil
}

// Close releases the store and the override staging.
func (a *Acquirer) Close() error {
	var stagingErr error
	if a.staging != nil {
		stagingErr = a.staging.close()
	}
	return errors.Join(stagingErr, a.fs.Close())
}

// Acquired is one materialized plugin image.
type Acquired struct {
	// Rootfs is the exported root filesystem — the store's shared
	// export-cache entry; treat it as read-only. Empty under
	// PullDaemon.
	Rootfs string
	// Image is the repository at the verified digest, for the daemon
	// to pull (PullDaemon); empty where a rootfs was exported.
	Image string
	// Process is the image config's process: argv as Entrypoint then
	// Cmd, exactly as OCI runtimes compose them, environment, and
	// working directory. Zero under PullDaemon: the daemon applies
	// the image's own configuration.
	Process plugexec.Process
	// Pin is the lockfile pin the acquisition ran under, freshly
	// recorded on first use.
	Pin lockfile.PluginPin
}

// Acquire materializes ref (REQ-plugin-digest-pin, REQ-lock-first-use):
// a pinned reference is materialized at its pinned digest — the tag is
// never re-resolved — and a first use resolves the tag once through
// the seam, records the pin, and materializes. Under PullDaemon the
// seam runs the same and the pin is recorded the same, but nothing
// materializes: the acquisition yields the repository at the verified
// digest for the daemon to pull (REQ-plugin-core-verifies).
func (a *Acquirer) Acquire(ctx context.Context, ref string) (*Acquired, error) {
	pin, pinned := a.lock.Plugin(ref, lockfile.SchemeOCI)
	target := ref
	acq := &acquisition{declaredRef: ref}
	if pinned {
		target = atDigest(ref, pin.Digest)
		acq.pinnedDigest = pin.Digest
	}
	if err := a.enter(target, acq); err != nil {
		return nil, err
	}
	defer a.leave(target)

	// The pin records what resolved (REQ-lock-first-use) before the
	// image's fitness as a plugin is judged: a resolution that
	// happened is the record, entrypoint or not.
	record := func() error {
		if pinned {
			return nil
		}
		if acq.resolved == "" {
			return fmt.Errorf("plugoci: %s: acquisition ran no verification seam", ref)
		}
		pin = lockfile.PluginPin{Ref: ref, Scheme: lockfile.SchemeOCI, Digest: acq.resolved, Provenance: acq.provenance}
		return a.lock.AddPlugin(pin)
	}
	if a.pull == PullDaemon {
		// Resolution runs the seam and materializes nothing (ocifs
		// api.md REQ-api-resolve); the daemon fetches the content at
		// the digest the seam admitted.
		res, err := a.fs.Resolve(ctx, target)
		if err != nil {
			return nil, err
		}
		if err := record(); err != nil {
			return nil, err
		}
		return &Acquired{Image: atDigest(ref, res.Digest.String()), Pin: pin}, nil
	}
	// One acquisition: the pull resolves and runs the seam, and the
	// export of the image it returned materializes exactly that,
	// resolving nothing again (ocifs api.md REQ-api-export).
	img, err := a.fs.Pull(ctx, target)
	if err != nil {
		return nil, err
	}
	rootfs, err := img.Export(ctx)
	if err != nil {
		return nil, err
	}
	if err := record(); err != nil {
		return nil, err
	}
	process, err := processOf(img.ConfigFile())
	if err != nil {
		return nil, fmt.Errorf("plugoci: %s: %v", ref, err)
	}
	return &Acquired{Rootfs: rootfs, Process: process, Pin: pin}, nil
}

// atDigest is ref's repository at digest: the digest-form reference
// a pinned acquisition resolves and a daemon pulls.
func atDigest(ref, digest string) string {
	return genfile.ReferenceRepository(ref) + "@" + digest
}

// processOf reads an image configuration into the plugin process: argv
// as Entrypoint then Cmd, exactly as OCI runtimes compose them, the
// environment as stated (KEY=VALUE throughout), and the working
// directory. An image with no entrypoint is no plugin.
func processOf(cfg *v1.ConfigFile) (plugexec.Process, error) {
	argv := append(append([]string{}, cfg.Config.Entrypoint...), cfg.Config.Cmd...)
	if len(argv) == 0 {
		return plugexec.Process{}, errors.New("the image declares no entrypoint: a plugin image's entrypoint is its plugin process")
	}
	if err := plugexec.CheckEnv(cfg.Config.Env); err != nil {
		return plugexec.Process{}, err
	}
	return plugexec.Process{Argv: argv, Env: cfg.Config.Env, WorkDir: cfg.Config.WorkingDir}, nil
}

func (a *Acquirer) enter(target string, acq *acquisition) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, busy := a.pending[target]; busy {
		return fmt.Errorf("plugoci: %s: concurrent acquisition of one target", target)
	}
	a.pending[target] = acq
	return nil
}

func (a *Acquirer) leave(target string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pending, target)
}

// verify is the store's verification seam (verification-seam.md): it
// holds the resolution to the pinned digest (REQ-plugin-digest-pin),
// requires a platform entry for the host (REQ-plugin-platform-strict —
// an artifact that is not a manifest list is refused the same way),
// and evaluates the trust policy (REQ-plugin-verify-before-run):
// unsigned images pass only under allow-unsigned and are recorded as
// provenance none (REQ-prov-unsigned-recorded); require-provenance
// fails closed without a verifier.
func (a *Acquirer) verify(ctx context.Context, id ocifs.ResolvedIdentity) error {
	a.mu.Lock()
	acq := a.pending[id.Reference]
	a.mu.Unlock()
	if acq == nil {
		return fmt.Errorf("plugoci: unsolicited acquisition of %s", id.Reference)
	}
	digest := id.Digest.String()
	if acq.pinnedDigest != "" && digest != acq.pinnedDigest {
		return fmt.Errorf("plugoci: %s resolved to %s, pin records %s (%w)", acq.declaredRef, digest, acq.pinnedDigest, lockfile.ErrPinMismatch)
	}
	if err := a.checkPlatforms(acq.declaredRef, id.Artifact); err != nil {
		return err
	}
	decision := a.policy.EvaluatePlugin(acq.declaredRef)
	if a.verifier == nil {
		if decision.Require {
			return fmt.Errorf("%w (plugin %s)", ErrNoImageVerifier, acq.declaredRef)
		}
		acq.resolved = digest
		acq.provenance = lockfile.Provenance{}
		return nil
	}
	// A verifier is always consulted when present — the module
	// pipeline's semantics: what verifies is recorded even under
	// allow-unsigned, and none is recorded only when nothing was
	// accepted for a tolerable reason (absence, identity
	// non-acceptance). Rejected evidence aborts under either posture.
	prov, err := a.verifier.VerifyImage(ctx, acq.declaredRef, digest, decision.Identity)
	switch {
	case err == nil:
		acq.resolved = digest
		acq.provenance = prov
		return nil
	case errors.Is(err, ErrNoEvidence) || errors.Is(err, ErrIdentityNotAccepted):
		if decision.Require {
			return err
		}
		acq.resolved = digest
		acq.provenance = lockfile.Provenance{}
		return nil
	default:
		return err
	}
}

// checkPlatforms requires the artifact to be a manifest list carrying
// the host platform. <os>/<arch> is the deliberate granularity —
// variant is not consulted (REQ-plugin-platform-strict).
func (a *Acquirer) checkPlatforms(ref string, artifact []byte) error {
	var top v1.IndexManifest
	if err := json.Unmarshal(artifact, &top); err != nil {
		return fmt.Errorf("plugoci: %s: unreadable top-level artifact: %v", ref, err)
	}
	if top.MediaType != types.OCIImageIndex && top.MediaType != types.DockerManifestList {
		return fmt.Errorf("plugoci: %s is not a manifest list (%s): the manifest list is the image's platform declaration, and pb refuses what it cannot match", ref, top.MediaType)
	}
	var listed []string
	for _, m := range top.Manifests {
		if m.Platform == nil {
			continue
		}
		if m.Platform.OS == a.platform.OS && m.Platform.Architecture == a.platform.Arch {
			return nil
		}
		listed = append(listed, m.Platform.OS+"/"+m.Platform.Architecture)
	}
	return fmt.Errorf("plugoci: %s has no %s/%s entry in its manifest list (found %v): the image does not support this platform", ref, a.platform.OS, a.platform.Arch, listed)
}
