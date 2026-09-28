// Package oci materializes oci-scheme plugin images through the
// image store (ocifs) with pb as the verifying core
// (plugin-execution.md REQ-plugin-core-verifies): pin resolution,
// trust evaluation, and the platform check run in the store's
// verification seam — after top-level resolution, before any content
// is materialized — so runners only ever execute what the seam
// admitted. The store is pb's, not a runner's: the seam's bytes are
// the manifest list pb fetched, retained under the store's
// digest-verified boundary.
package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/ocifs"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/plugin"
	"github.com/greatliontech/pb/internal/plugin/genfile"
	"github.com/greatliontech/pb/internal/provenance/image"
	"github.com/greatliontech/pb/internal/provenance/image/discover"
	"github.com/greatliontech/pb/internal/provenance/image/evidence"
	"github.com/greatliontech/pb/internal/provenance/trust"
)

// The two reasons no evidence is judged for an image
// (provenance.md REQ-prov-plugin-identity), tolerable under
// allow-unsigned like absence: no identity rule governs the
// reference, so no signer could be accepted; no trusted root is
// configured to verify against.
var (
	ErrNoIdentityRule = errors.New("oci: no identity rule names an accepted signer for the plugin")
	ErrNoTrustedRoot  = errors.New("oci: no trusted root is configured to verify plugin signatures against")
)

// v1Platform is the image library's spelling of a platform.
func v1Platform(p plugin.Platform) v1.Platform {
	return v1.Platform{OS: p.OS, Architecture: p.Arch}
}

// Config assembles an Acquirer. WorkDir roots the image store and
// EvidenceDir the evidence kept with it (provenance.md
// REQ-prov-plugin-evidence-store), empty keeping none; TrustedRoot
// is the root plugin signatures verify against, nil leaving every
// image unsigned (require-provenance then fails closed); a zero
// Platform means the host's.
type Config struct {
	WorkDir     string
	EvidenceDir string
	Lock        *lockfile.File
	Policy      *trust.Policy
	TrustedRoot *gitprov.TrustedRoot
	Platform    plugin.Platform
	Credentials map[string]authn.AuthConfig
	// Transport carries every round trip to a registry outside this
	// process; nil is the registry client's own. A suite serving its
	// registries in this process hands the transport serving them.
	Transport http.RoundTripper
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
	fs        *ocifs.OCIFS
	transport *inProcessTransport // the staging in this process, then Config.Transport
	lock      *lockfile.File
	policy    *trust.Policy
	root      *gitprov.TrustedRoot
	kept      *evidence.Store // nil keeps none
	platform  plugin.Platform
	pull      PullMode

	mu      sync.Mutex
	pending map[string]*acquisition // digest-or-tag target -> in-flight state
	// holds are the acquirer's holds over every image it exported
	// (ocifs api.md REQ-api-hold): the export outlives an emptying of
	// the store in another process for as long as the acquirer, whose
	// runs read it (dep-verbs.md REQ-dep-clean). Released at Close.
	holds []*ocifs.Hold
}

// acquisition carries one Acquire call's seam contract and results.
type acquisition struct {
	declaredRef      string // the reference as declared in generation config
	pinnedDigest     string // "" on first use and on an explicit update
	pinnedProvenance lockfile.Provenance
	fresh            bool   // an explicit update: evidence fetched anew, kept evidence not judged
	entry            string // the manifest-list entry the seam admitted, os/arch with its variant
	resolved         string
	provenance       lockfile.Provenance
}

// New constructs the acquirer and its verifying store.
func New(cfg Config) (*Acquirer, error) {
	if cfg.Lock == nil || cfg.Policy == nil {
		return nil, errors.New("oci: acquirer needs a lockfile and a trust policy")
	}
	platform := cfg.Platform
	if platform == (plugin.Platform{}) {
		platform = plugin.HostPlatform()
	}
	a := &Acquirer{
		kept:     evidenceStore(cfg.EvidenceDir),
		lock:     cfg.Lock,
		policy:   cfg.Policy,
		root:     cfg.TrustedRoot,
		platform: platform,
		pull:     cfg.Pull,
		pending:  map[string]*acquisition{},
	}
	opts := []ocifs.Option{
		ocifs.WithWorkDir(cfg.WorkDir),
		ocifs.WithDefaultPlatform(v1Platform(platform)),
		ocifs.WithVerifier(a.verify),
	}
	// The override staging is a registry in this process, served by
	// the acquirer's own transport ahead of the one the caller hands.
	base := cfg.Transport
	if base == nil {
		base = remote.DefaultTransport
	}
	a.transport = newInProcessTransport(base)
	a.transport.serve(stagingHost, newStagingRegistry())
	opts = append(opts, ocifs.WithTransport(a.transport))
	for prefix, auth := range cfg.Credentials {
		opts = append(opts, ocifs.WithAuthSource(prefix, auth))
	}
	fs, err := ocifs.New(opts...)
	if err != nil {
		return nil, err
	}
	a.fs = fs
	return a, nil
}

// Close releases the store.
func (a *Acquirer) Close() error {
	a.mu.Lock()
	holds := a.holds
	a.holds = nil
	a.mu.Unlock()
	var err error
	for _, h := range holds {
		err = errors.Join(err, h.Release(context.Background()))
	}
	return errors.Join(err, a.fs.Close())
}

// Acquire materializes ref (REQ-plugin-digest-pin, REQ-lock-first-use):
// a pinned reference is materialized at its pinned digest — the tag is
// never re-resolved — and a first use resolves the tag once through
// the seam, records the pin, and materializes. Under PullDaemon the
// seam runs the same and the pin is recorded the same, but nothing
// materializes: the acquisition yields the repository at the verified
// digest for the daemon to pull (REQ-plugin-core-verifies).
func (a *Acquirer) Acquire(ctx context.Context, ref string) (*plugin.Acquired, error) {
	pin, pinned := a.lock.Plugin(ref, lockfile.SchemeOCI)
	target := ref
	acq := &acquisition{declaredRef: ref}
	if pinned {
		target = genfile.ReferenceRepository(ref) + "@" + pin.Digest
		acq.pinnedDigest = pin.Digest
		acq.pinnedProvenance = pin.Provenance
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
		p, err := acq.pin()
		if err != nil {
			return err
		}
		return a.lock.AddPlugin(p)
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
		return &plugin.Acquired{Image: &plugin.Pulled{Repository: genfile.ReferenceRepository(ref), Digest: res.Digest.String(), Entry: acq.entry}}, nil
	}
	// One acquisition: the pull resolves and runs the seam, and the
	// export of the image it returned materializes exactly that,
	// resolving nothing again (ocifs api.md REQ-api-export).
	img, err := a.fs.Pull(ctx, target)
	if err != nil {
		return nil, err
	}
	// The image is held for the acquirer's life before its export is
	// materialized, so the export is the hold's and a run reads it
	// after an emptying of the store elsewhere (ocifs api.md
	// REQ-api-hold, dep-verbs.md REQ-dep-clean). An emptying landing
	// between the pull's return and the hold collects the image, and
	// the export below fails loudly; a rerun by the user succeeds,
	// and no result is ever wrong.
	hold, err := a.fs.Hold(ctx, img)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.holds = append(a.holds, hold)
	a.mu.Unlock()
	rootfs, err := img.Export(ctx)
	if err != nil {
		return nil, err
	}
	if err := record(); err != nil {
		return nil, err
	}
	process, err := processOf(img.ConfigFile())
	if err != nil {
		return nil, fmt.Errorf("oci: %s: %v", ref, err)
	}
	return &plugin.Acquired{Process: process, Image: &plugin.Export{Rootfs: rootfs, Entry: acq.entry}}, nil
}

// UpdatePlugin re-resolves ref and rewrites its pin: the tag to the
// digest it names now, the evidence fetched anew and judged under
// the policy, the pin's digest and record replaced — the explicit
// user-invoked update REQ-lock-no-silent-downgrade sanctions
// (dep-verbs.md REQ-dep-update). Nothing materializes. A reference
// with no pin, or one the seam refuses, fails and leaves the pin.
// Returns the pin as it was and as it is.
func (a *Acquirer) UpdatePlugin(ctx context.Context, ref string) (before, after lockfile.PluginPin, err error) {
	before, pinned := a.lock.Plugin(ref, lockfile.SchemeOCI)
	if !pinned {
		return lockfile.PluginPin{}, lockfile.PluginPin{}, fmt.Errorf("oci: %s: no pin to update", ref)
	}
	acq := &acquisition{declaredRef: ref, fresh: true}
	if err := a.enter(ref, acq); err != nil {
		return lockfile.PluginPin{}, lockfile.PluginPin{}, err
	}
	defer a.leave(ref)
	// The tag is asked of the registry for this call alone: the
	// store's own policy would answer a cached tag from the cache.
	if _, err := a.fs.Resolve(ctx, ref, ocifs.ResolveUnder(ocifs.PullAlways)); err != nil {
		return lockfile.PluginPin{}, lockfile.PluginPin{}, err
	}
	after, err = acq.pin()
	if err != nil {
		return lockfile.PluginPin{}, lockfile.PluginPin{}, err
	}
	if err := a.lock.UpdatePlugin(after); err != nil {
		return lockfile.PluginPin{}, lockfile.PluginPin{}, err
	}
	return before, after, nil
}

// pin is the pin the seam's verdict makes for the declared reference:
// what resolved and what was judged; an acquisition that ran no seam
// has none.
func (acq *acquisition) pin() (lockfile.PluginPin, error) {
	if acq.resolved == "" {
		return lockfile.PluginPin{}, fmt.Errorf("oci: %s: acquisition ran no verification seam", acq.declaredRef)
	}
	return lockfile.PluginPin{Ref: acq.declaredRef, Scheme: lockfile.SchemeOCI, Digest: acq.resolved, Provenance: acq.provenance}, nil
}

// processOf reads an image configuration into the plugin process: argv
// as Entrypoint then Cmd, exactly as OCI runtimes compose them, the
// environment as stated (KEY=VALUE throughout), and the working
// directory. An image with no entrypoint is no plugin.
func processOf(cfg *v1.ConfigFile) (plugin.Process, error) {
	argv := append(append([]string{}, cfg.Config.Entrypoint...), cfg.Config.Cmd...)
	if len(argv) == 0 {
		return plugin.Process{}, errors.New("the image declares no entrypoint: a plugin image's entrypoint is its plugin process")
	}
	if err := plugin.CheckEnv(cfg.Config.Env); err != nil {
		return plugin.Process{}, err
	}
	return plugin.Process{Argv: argv, Env: cfg.Config.Env, WorkDir: cfg.Config.WorkingDir}, nil
}

// enter admits one acquisition of target at a time; an acquisition is
// keyed by what it resolves — a pinned one by its digest form, a
// first use and an update by the tag — so the guard is against two
// resolutions of one target, not against every pairing of one
// plugin, and the lockfile is the verb's to serialize.
func (a *Acquirer) enter(target string, acq *acquisition) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, busy := a.pending[target]; busy {
		return fmt.Errorf("oci: %s: concurrent acquisition of one target", target)
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
// the image's signature evidence is judged on every acquisition,
// unsigned images pass only under allow-unsigned and are recorded as
// provenance none (REQ-prov-unsigned-recorded), and a pinned image
// whose record the evidence no longer bears fails
// (REQ-lock-no-silent-downgrade).
func (a *Acquirer) verify(ctx context.Context, id ocifs.ResolvedIdentity) error {
	a.mu.Lock()
	acq := a.pending[id.Reference]
	a.mu.Unlock()
	if acq == nil {
		return fmt.Errorf("oci: unsolicited acquisition of %s", id.Reference)
	}
	digest := id.Digest.String()
	if acq.pinnedDigest != "" && digest != acq.pinnedDigest {
		return fmt.Errorf("oci: %s resolved to %s, pin records %s (%w)", acq.declaredRef, digest, acq.pinnedDigest, lockfile.ErrPinMismatch)
	}
	entry, err := a.checkPlatforms(acq.declaredRef, id.Artifact)
	if err != nil {
		return err
	}
	acq.entry = entry
	// Evidence is always judged — the module pipeline's semantics:
	// what verifies is recorded even under allow-unsigned, and none
	// is recorded only when nothing was accepted for a tolerable
	// reason. Rejected evidence aborts under either posture.
	decision := a.policy.EvaluatePlugin(acq.declaredRef)
	var accept func(lockfile.Provenance) bool
	if acq.pinnedDigest != "" {
		// A pinned image's record must still hold: among the carriers
		// the policy accepts, the one reproducing it is the one taken
		// (REQ-lock-no-silent-downgrade). The record-not-reproduced arm
		// below re-derives this very check on the declined record to
		// name what changed, so the two must stay one predicate.
		accept = func(rec lockfile.Provenance) bool {
			return lockfile.CheckProvenanceTransition(acq.pinnedProvenance, rec) == nil
		}
	}
	prov, err := a.evidence(ctx, id.Reference, digest, decision, accept, acq.fresh)
	switch {
	case err == nil:
	case errors.Is(err, image.ErrRecordNotReproduced):
		return fmt.Errorf("oci: %s: %w", acq.declaredRef, lockfile.CheckProvenanceTransition(acq.pinnedProvenance, prov))
	case tolerable(err):
		if decision.Require {
			return fmt.Errorf("oci: plugin %s requires provenance: %w", acq.declaredRef, err)
		}
		prov = lockfile.Provenance{}
	default:
		return err
	}
	if acq.pinnedDigest != "" {
		if err := lockfile.CheckProvenanceTransition(acq.pinnedProvenance, prov); err != nil {
			return fmt.Errorf("oci: %s: %w", acq.declaredRef, err)
		}
	}
	acq.resolved = digest
	acq.provenance = prov
	return nil
}

// tolerable reports a judgement that accepted nothing for a reason
// allow-unsigned tolerates (REQ-prov-plugin-classification,
// REQ-prov-plugin-identity): absence, a signer the policy refuses, no
// identity rule, no trusted root.
func tolerable(err error) bool {
	return errors.Is(err, image.ErrNoEvidence) || errors.Is(err, image.ErrIdentityNotAccepted) ||
		errors.Is(err, ErrNoIdentityRule) || errors.Is(err, ErrNoTrustedRoot)
}

// evidenceStore is the store at dir, none for no dir.
func evidenceStore(dir string) *evidence.Store {
	if dir == "" {
		return nil
	}
	return &evidence.Store{Dir: dir}
}

// evidence judges the signature evidence for digest: the evidence
// kept for it first, and, when that holds none the policy accepts,
// the evidence fetched from the repository the image was fetched
// from — the resolved reference's, so an override's evidence is
// sought where the override was staged — each carrier fetched as it
// is judged and what was taken kept in place of the old
// (REQ-prov-plugin-evidence-kept, REQ-prov-plugin-carriers,
// REQ-prov-plugin-classification). The judgement is against the
// identity rule governing the declared reference; accept is the
// caller's further acceptance of a verified record, nil for every
// one; fresh — an explicit update — fetches without judging what was
// kept. With no identity rule or no trusted root nothing is judged:
// no evidence could be accepted (REQ-prov-plugin-identity).
func (a *Acquirer) evidence(ctx context.Context, reference, digest string, decision trust.Decision, accept func(lockfile.Provenance) bool, fresh bool) (lockfile.Provenance, error) {
	if decision.Identity == nil {
		return lockfile.Provenance{}, ErrNoIdentityRule
	}
	if a.root == nil {
		return lockfile.Provenance{}, ErrNoTrustedRoot
	}
	id, err := trust.ExplicitIdentity(*decision.Identity)
	if err != nil {
		return lockfile.Provenance{}, err
	}
	ref, err := name.ParseReference(reference)
	if err != nil {
		return lockfile.Provenance{}, fmt.Errorf("oci: %w", err)
	}
	h, err := v1.NewHash(digest)
	if err != nil {
		return lockfile.Provenance{}, fmt.Errorf("oci: %w", err)
	}
	if a.kept != nil && !fresh {
		if kept, ok := a.kept.Load(h); ok {
			if rec, err := image.Judge(ctx, digest, image.Sequence(kept), id, a.root, accept); err == nil {
				return rec, nil
			}
		}
	}
	var fetched image.Recorder
	rec, err := image.Judge(ctx, digest, fetched.Of(discover.Discover(ctx, ref.Context(), h, remote.WithTransport(a.transport), remote.WithAuthFromKeychain(a.fs.Keychain()))), id, a.root, accept)
	if a.kept != nil && (err == nil || tolerable(err)) {
		// The keep is for later acquisitions; one that fails changes
		// no outcome (REQ-prov-plugin-evidence-kept), as the cache
		// carries no authority.
		_ = a.kept.Save(h, fetched.Taken)
	}
	return rec, err
}

// checkPlatforms admits the one manifest-list entry for the host —
// os and architecture, the variant not consulted — and returns its
// platform as a daemon spells it, variant included, so the runner can
// name exactly that child (REQ-plugin-platform-strict). None is a
// refusal attributing the gap to the image; several (an index
// carrying more than one variant for the host) is a refusal too:
// choosing among them would be a fallback, as the store's own rule
// holds.
func (a *Acquirer) checkPlatforms(ref string, artifact []byte) (string, error) {
	var top v1.IndexManifest
	if err := json.Unmarshal(artifact, &top); err != nil {
		return "", fmt.Errorf("oci: %s: unreadable top-level artifact: %v", ref, err)
	}
	if top.MediaType != types.OCIImageIndex && top.MediaType != types.DockerManifestList {
		return "", fmt.Errorf("oci: %s is not a manifest list (%s): the manifest list is the image's platform declaration, and pb refuses what it cannot match", ref, top.MediaType)
	}
	var listed, admitted []string
	for _, m := range top.Manifests {
		if m.Platform == nil {
			continue
		}
		// Spelled as a daemon's --platform parses it: os/arch, then
		// the variant; an OS version, which that flag does not take,
		// is left out of the spelling (and of the match).
		spelled := m.Platform.OS + "/" + m.Platform.Architecture
		if m.Platform.Variant != "" {
			spelled += "/" + m.Platform.Variant
		}
		listed = append(listed, spelled)
		if m.Platform.OS == a.platform.OS && m.Platform.Architecture == a.platform.Arch {
			admitted = append(admitted, spelled)
		}
	}
	switch len(admitted) {
	case 1:
		return admitted[0], nil
	case 0:
		return "", fmt.Errorf("oci: %s has no %s entry in its manifest list (found %v): the image does not support this platform", ref, a.platform, listed)
	}
	return "", fmt.Errorf("oci: %s has %d entries for %s in its manifest list (%v): choosing among them would be a fallback, and pb refuses it", ref, len(admitted), a.platform, admitted)
}
