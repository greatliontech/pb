package fetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/greatliontech/pb/internal/module"

	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/pb/internal/module/archive"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/modfile"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/provenance"
	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/pb/internal/source/proxy"
)

// ModfileHash renders the module-file hash of exact module-file bytes
// (module-lockfile.md, module-file hash term).
func ModfileHash(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// UnpinnedError is a read-only client's answer for a pair its pin
// store does not pin: the pair, and the list — modules or rulesets —
// the pin would be recorded in.
type UnpinnedError struct {
	Path, Version string
	List          string
}

func (e *UnpinnedError) Error() string {
	return fmt.Sprintf("%s@%s is not pinned in the lockfile's %s list; run pb dep download", e.Path, e.Version, e.List)
}

// Module returns the verified module file for (modPath, v) — the
// requirement loader version selection consumes. An unpinned pair runs
// the full first-use pipeline (fetch, verify, evaluate provenance,
// record the pin); a pinned pair verifies cached or fetched bytes
// against the pin, the pin the record, and a pin accepted under a
// pinned key is held to the policy of the day besides (pinGoverned).
func (c *Client) Module(ctx context.Context, modPath string, v version.Version) (*modfile.File, error) {
	if pin, ok := c.Lock.Module(modPath, v.String()); ok {
		if _, err := c.pinGoverned(modPath, v, pin); err != nil {
			return nil, err
		}
		return c.pinnedModule(ctx, modPath, v, pin)
	}
	return c.firstUse(ctx, modPath, v, c.Lock.ModulePins())
}

// pinGoverned holds a pinned pair to the policy of the day before it
// is served: a pin accepted under a pinned key resolves only while the
// policy still pins that key for the module — unpinning is the tier's
// one revocation, and it reaches every later resolution
// (REQ-prov-pinned-key-recorded) — and the key as pinned today is
// returned, the one re-verification runs against; nil for a pin
// accepted under an identity, which stands on its record
// (REQ-lock-no-silent-downgrade): the record, not the policy's current
// patterns, is what re-verification holds it to.
func (c *Client) pinGoverned(modPath string, v version.Version, pin lockfile.ModulePin) (*gitprov.PinnedKey, error) {
	if !lockfile.NamesKey(pin.Provenance) {
		return nil, nil
	}
	key, ok := c.pinnedKeyOf(modPath, pin.Provenance)
	if !ok {
		return nil, fmt.Errorf("fetch: %s@%s: the pin records the %s key %s, which the trust policy no longer pins for it: %w", modPath, v, pin.Provenance.KeyKind, pin.Provenance.KeyFingerprint, lockfile.ErrProvenanceDowngrade)
	}
	return &key, nil
}

// pinnedModule serves a pinned pair's module file, cheapest verifiable
// source first: a cached or fetched standalone module file held to the
// pinned hash, falling back to the digest-verified archive. Cache
// entries failing their pin are discarded as absent
// (REQ-dep-cache-transparent); fetched bytes failing it fail the
// operation (REQ-lock-digest-enforcement).
func (c *Client) pinnedModule(ctx context.Context, modPath string, v version.Version, pin lockfile.ModulePin) (*modfile.File, error) {
	if pin.Modfile == "" {
		// The pin records a module that declares no module file: the
		// digest-verified archive is the only artifact, and its file set
		// must agree with the record.
		zip, err := c.pinnedZip(ctx, modPath, v, pin)
		if err != nil {
			return nil, err
		}
		if _, has, err := archive.ZipModuleFile(bytes.NewReader(zip), int64(len(zip))); err != nil {
			return nil, err
		} else if has {
			return nil, fmt.Errorf("%w: %s@%s archive declares a module file but the pin records none", lockfile.ErrPinMismatch, modPath, v)
		}
		return modfile.FromFileSet(modPath, nil)
	}
	if b, ok, err := c.Cache.Get(modPath, v, KindMod); err != nil {
		return nil, err
	} else if ok && ModfileHash(b) == pin.Modfile {
		return parseModfile(modPath, b)
	}
	b, err := c.fetch(ctx, modPath, v, KindMod)
	if err == nil {
		if got := ModfileHash(b); got != pin.Modfile {
			return nil, fmt.Errorf("%w: %s@%s modfile: expected %s, computed %s", lockfile.ErrPinMismatch, modPath, v, pin.Modfile, got)
		}
		if err := c.Cache.Put(modPath, v, KindMod, b); err != nil {
			return nil, err
		}
		return parseModfile(modPath, b)
	}
	if !errors.Is(err, proxy.ErrNotHere) {
		return nil, err
	}
	// No source serves the standalone module file; the archive carries
	// the declared copy in its file set (module-archive.md).
	zip, err := c.pinnedZip(ctx, modPath, v, pin)
	if err != nil {
		return nil, err
	}
	mb, err := archiveModfile(modPath, v, pin, zip)
	if err != nil {
		return nil, err
	}
	return parseModfile(modPath, mb)
}

func parseModfile(modPath string, b []byte) (*modfile.File, error) {
	f, err := modfile.Parse(b)
	if err != nil {
		return nil, err
	}
	if err := modfile.CheckIdentity(f, modPath); err != nil {
		return nil, err
	}
	return f, nil
}

// Zip returns a pair's verified archive bytes: an unpinned pair runs
// the first-use pipeline on the way, a pinned pair serves the cache-or-
// fetch path held to the pin. The bytes back import analysis and
// verification above the pipeline; they are already accepted, never
// Unverified.
func (c *Client) Zip(ctx context.Context, modPath string, v version.Version) ([]byte, error) {
	return c.zip(ctx, modPath, v, c.Lock.ModulePins())
}

// RulesetZip is Zip for a pair a ruleset import names: pinned in the
// lockfile's rulesets list, never among the modules
// (module-lockfile.md REQ-lock-ruleset-entry), the pipeline the same.
func (c *Client) RulesetZip(ctx context.Context, modPath string, v version.Version) ([]byte, error) {
	return c.zip(ctx, modPath, v, c.Lock.RulesetPins())
}

func (c *Client) zip(ctx context.Context, modPath string, v version.Version, pins lockfile.Pins) ([]byte, error) {
	pin, ok := pins.Module(modPath, v.String())
	if !ok {
		if _, err := c.firstUse(ctx, modPath, v, pins); err != nil {
			return nil, err
		}
		pin, _ = pins.Module(modPath, v.String())
	}
	if _, err := c.pinGoverned(modPath, v, pin); err != nil {
		return nil, err
	}
	return c.pinnedZip(ctx, modPath, v, pin)
}

// pinnedZip returns the pinned pair's archive bytes, digest-verified
// against the pin: the cached entry when it verifies, else a fresh
// fetch — a cache entry failing the pin is local corruption, discarded
// and replaced (REQ-dep-cache-transparent); a fetched artifact failing
// it fails the operation (REQ-lock-digest-enforcement).
func (c *Client) pinnedZip(ctx context.Context, modPath string, v version.Version, pin lockfile.ModulePin) ([]byte, error) {
	if pin.Digest == "" {
		// Unreachable through this package's own pins — first use always
		// fetches the archive — but the lockfile wire format admits
		// digestless pins, and serving unverifiable bytes is not an option.
		return nil, fmt.Errorf("fetch: pin for %s@%s has no digest to verify the archive against", modPath, v)
	}
	if b, ok, err := c.Cache.Get(modPath, v, KindZip); err != nil {
		return nil, err
	} else if ok {
		if _, err := archive.VerifyZip(bytes.NewReader(b), int64(len(b)), pin.Digest); err == nil {
			return b, nil
		}
	}
	b, err := c.fetch(ctx, modPath, v, KindZip)
	if err != nil {
		return nil, err
	}
	if _, err := archive.VerifyZip(bytes.NewReader(b), int64(len(b)), pin.Digest); err != nil {
		return nil, fmt.Errorf("%s@%s: %w", modPath, v, err)
	}
	if err := c.Cache.Put(modPath, v, KindZip, b); err != nil {
		return nil, err
	}
	return b, nil
}

// firstUse runs the complete pipeline for an unpinned pair
// (REQ-lock-first-use): fetch the archive from sources — never the
// cache: a pin is what makes cache bytes verifiable, so an unpinned
// resolution reading the cache would let unverifiable local state pick
// the content the pin then blesses (REQ-dep-cache-transparent) — then
// digest, module facts, provenance evaluation, and the pin, recorded
// complete in one step in the list the pair belongs to.
func (c *Client) firstUse(ctx context.Context, modPath string, v version.Version, pins lockfile.Pins) (*modfile.File, error) {
	if c.ReadOnly {
		return nil, &UnpinnedError{Path: modPath, Version: v.String(), List: pins.Name()}
	}
	zip, err := c.fetch(ctx, modPath, v, KindZip)
	if err != nil {
		return nil, err
	}
	digest, _, err := archive.DigestZip(bytes.NewReader(zip), int64(len(zip)))
	if err != nil {
		return nil, err
	}
	// One pair names one content: a pin of the pair in the other list
	// is held to the same digest (REQ-lock-ruleset-entry), so a moved
	// tag never serves one reader what the other refuses.
	if other, ok := pins.Other().Module(modPath, v.String()); ok && other.Digest != "" && other.Digest != digest {
		return nil, fmt.Errorf("%w: %s@%s digest: pinned as a %s at %s, fetched %s", lockfile.ErrPinMismatch, modPath, v, pins.Other().Name(), other.Digest, digest)
	}
	mb, hasMod, err := archive.ZipModuleFile(bytes.NewReader(zip), int64(len(zip)))
	if err != nil {
		return nil, err
	}
	var files map[string][]byte
	if hasMod {
		files = map[string][]byte{module.ModuleFileName: mb}
	}
	mf, err := modfile.FromFileSet(modPath, files)
	if err != nil {
		return nil, err
	}
	rec, provBytes, err := c.evaluateProvenance(ctx, modPath, v, zip)
	if err != nil {
		return nil, err
	}
	pin := lockfile.ModulePin{Path: modPath, Version: v.String(), Digest: digest, Provenance: rec}
	if hasMod {
		pin.Modfile = ModfileHash(mb)
	}
	if err := pins.Add(pin); err != nil {
		return nil, err
	}
	if err := c.Cache.Keep(modPath, v, KindZip, zip); err != nil {
		return nil, err
	}
	if hasMod {
		if err := c.Cache.Keep(modPath, v, KindMod, mb); err != nil {
			return nil, err
		}
	}
	if provBytes != nil {
		if err := c.Cache.Keep(modPath, v, KindProv, provBytes); err != nil {
			return nil, err
		}
	}
	return mf, nil
}

// evaluateProvenance fetches and evaluates a pair's provenance evidence
// against the trust policy (REQ-prov-policy-eval,
// REQ-prov-pinned-key-eval), returning the record for the pin and the
// envelope bytes when evidence was accepted.
//
// The governing rule has up to two arms: an identity, explicit or the
// origin-consistency default, for a sigstore signature; and pinned
// keys, for an OpenPGP or SSH one. A rule naming keys is requiring
// them, whatever the mode says; a rule naming both accepts either
// evidence; a rule naming keys alone accepts a pinned key's signature
// and no identity, the default included.
//
// Classification is three-way. Evidence that verifies under an arm is
// recorded. Evidence that is absent — no envelope served, a kind no
// arm takes, no embedded transparency proof (REQ-prov-signed-tag), an
// identity the policy does not accept, a signature by no pinned key,
// or no identity to hold a sigstore signature to (no explicit rule and
// no derivable origin default, REQ-prov-origin-consistency; or no
// trusted root) — leaves the subject unsigned: recorded none under
// allow-unsigned (REQ-prov-unsigned-recorded), a failure under
// require-provenance or a rule naming keys. Evidence that fails
// verification or binding any other way is tampered-with or corrupt
// and fails the operation outright (REQ-prov-tag-binding: rejected,
// not ignored).
func (c *Client) evaluateProvenance(ctx context.Context, modPath string, v version.Version, zip []byte) (lockfile.Provenance, []byte, error) {
	var dec trust.Decision
	if c.Policy != nil {
		dec = c.Policy.EvaluateModule(modPath)
	}
	// Naming keys is requiring them (REQ-prov-pinned-key-eval).
	require := dec.Require || len(dec.Keys) > 0
	fail := func(reason string) (lockfile.Provenance, []byte, error) {
		if require {
			return lockfile.Provenance{}, nil, fmt.Errorf("fetch: %s@%s requires provenance: %s", modPath, v, reason)
		}
		return lockfile.Provenance{}, nil, nil
	}

	provBytes, err := c.fetch(ctx, modPath, v, KindProv)
	if errors.Is(err, proxy.ErrNotHere) {
		return fail("no source has provenance evidence")
	}
	if err != nil {
		return lockfile.Provenance{}, nil, err
	}
	evidence, err := provenance.ParseEnvelope(provBytes)
	if err != nil {
		return lockfile.Provenance{}, nil, err
	}
	if len(evidence) == 0 {
		return fail("the provenance envelope carries no recognized evidence")
	}
	// Without a trusted root a sigstore signature verifies against
	// nothing; where pinned keys are the only arm, none is needed.
	if c.TrustedRoot == nil && len(dec.Keys) == 0 {
		return fail("no trusted root is configured to verify evidence against")
	}

	// Evidence exists, so the origin is needed regardless of which
	// arm applies: the subject's subtree binds the tree walk.
	o, err := c.origin(ctx, modPath)
	if err != nil {
		return lockfile.Provenance{}, nil, err
	}
	// The identity arm: explicit, or the origin's default where the
	// rule names no keys.
	var id *gitprov.Identity
	switch {
	case dec.Identity != nil:
		explicit, err := trust.ExplicitIdentity(*dec.Identity)
		if err != nil {
			return lockfile.Provenance{}, nil, err
		}
		id = &explicit
	case len(dec.Keys) == 0:
		byDefault, err := trust.DefaultIdentity(o.Repo)
		if errors.Is(err, trust.ErrNoDefaultIdentity) {
			return fail(err.Error())
		}
		if err != nil {
			return lockfile.Provenance{}, nil, err
		}
		id = &byDefault
	}

	rec, accepted, skipped, err := c.verifyEvidence(ctx, o.Subtree, v, zip, evidence, id, dec.Keys, func(lockfile.Provenance) bool { return true })
	if err != nil {
		return lockfile.Provenance{}, nil, fmt.Errorf("%s@%s: %w", modPath, v, err)
	}
	if accepted {
		return rec, provBytes, nil
	}
	return fail("no evidence accepted: " + strings.Join(skipped, "; "))
}
