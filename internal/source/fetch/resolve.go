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
	"github.com/greatliontech/glob"
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
// record the pin); a pinned pair is held to the trust policy of the
// day (holdToPolicy) and verifies cached or fetched bytes against the
// pin, the pin the record.
func (c *Client) Module(ctx context.Context, modPath string, v version.Version) (*modfile.File, error) {
	if pin, ok := c.Lock.Module(modPath, v.String()); ok {
		key, err := c.holdToPolicy(modPath, v, pin)
		if err != nil {
			return nil, err
		}
		return c.pinnedModule(ctx, modPath, v, pin, key)
	}
	return c.firstUse(ctx, modPath, v, c.Lock.ModulePins())
}

// holdToPolicy holds a pinned pair's provenance record to the trust
// policy of the day before anything of the pair is served
// (provenance.md REQ-prov-pin-held): a pin is held to today's
// contract, not the one it was born under. A record of none under a
// rule requiring provenance — by mode or by naming keys — fails; a
// pinned-key record resolves only while the policy still pins that
// key for the module — unpinning is the tier's one revocation, and it
// reaches every later resolution (REQ-prov-pinned-key-recorded) — and
// the key as pinned today is returned, the one re-verification runs
// against; an identity record is held to the rule's identity where
// one is written, and fails under a rule naming keys alone, while
// under the origin default it stands on its record: the default is
// the origin's, derived once at the pin, which no policy edit moves.
// Every failure names the explicit update that re-resolves the pair.
func (c *Client) holdToPolicy(modPath string, v version.Version, pin lockfile.ModulePin) (*gitprov.PinnedKey, error) {
	var dec trust.Decision
	if c.Policy != nil {
		dec = c.Policy.EvaluateModule(modPath)
	}
	rec := pin.Provenance
	refuse := func(what string) error {
		return fmt.Errorf("fetch: %s@%s: %s; run pb dep update %s@%s to resolve it anew: %w", modPath, v, what, modPath, v, ErrPinUnderPolicy)
	}
	switch {
	case rec == (lockfile.Provenance{}):
		if dec.Require || len(dec.Keys) > 0 {
			return nil, refuse("the pin records no provenance and the trust policy requires it")
		}
		return nil, nil
	case lockfile.NamesKey(rec):
		key, ok := c.pinnedKeyOf(modPath, rec)
		if !ok {
			return nil, fmt.Errorf("fetch: %s@%s: the pin records the %s key %s, which the trust policy no longer pins for it; run pb dep update %s@%s to resolve it anew: %w", modPath, v, rec.KeyKind, rec.KeyFingerprint, modPath, v, lockfile.ErrProvenanceDowngrade)
		}
		return &key, nil
	case dec.Identity != nil:
		id, err := trust.ExplicitIdentity(*dec.Identity)
		if err != nil {
			return nil, err
		}
		accepts, err := identityMatches(id, rec.SAN, rec.Issuer)
		if err != nil {
			return nil, err
		}
		if !accepts {
			return nil, refuse(fmt.Sprintf("the pin records the identity %s by %s, which the trust policy's identity rule does not accept", rec.SAN, rec.Issuer))
		}
		return nil, nil
	case len(dec.Keys) > 0:
		return nil, refuse("the pin records a signed-tag identity and the trust policy names pinned keys alone")
	}
	// The origin default, re-derived offline from the repository the
	// record was accepted under (REQ-prov-origin-consistency): the
	// record stands only where the default accepts it — a record made
	// under an explicit rule since removed is not the default's.
	if rec.Repo == "" {
		return nil, refuse("the pin's record names no repository to derive the origin default from")
	}
	id, err := trust.DefaultIdentity(rec.Repo)
	if errors.Is(err, trust.ErrNoDefaultIdentity) {
		return nil, refuse(fmt.Sprintf("the pin records the identity %s by %s under %s, which has no default identity, and the trust policy names no rule for the module", rec.SAN, rec.Issuer, rec.Repo))
	}
	if err != nil {
		return nil, err
	}
	accepts, err := identityMatches(id, rec.SAN, rec.Issuer)
	if err != nil {
		return nil, err
	}
	if !accepts {
		return nil, refuse(fmt.Sprintf("the pin records the identity %s by %s, which is not %s's own, and the trust policy names no rule for the module", rec.SAN, rec.Issuer, rec.Repo))
	}
	return nil, nil
}

// identityMatches reports whether an identity — a rule's or the origin
// default — accepts a recorded one: the issuer exactly, the subject
// under the glob, as gitprov holds a certificate's.
func identityMatches(id gitprov.Identity, san, issuer string) (bool, error) {
	if id.Issuer != issuer {
		return false, nil
	}
	p, err := glob.Compile(id.SubjectGlob)
	if err != nil {
		return false, fmt.Errorf("trust: identity: %w", err)
	}
	return p.Match(san), nil
}

// pinnedModule serves a pinned pair's module file, cheapest verifiable
// source first: a cached or fetched standalone module file held to the
// pinned hash, falling back to the digest-verified archive. Cache
// entries failing their pin are discarded as absent
// (REQ-dep-cache-transparent); fetched bytes failing it fail the
// operation (REQ-lock-digest-enforcement).
func (c *Client) pinnedModule(ctx context.Context, modPath string, v version.Version, pin lockfile.ModulePin, key *gitprov.PinnedKey) (*modfile.File, error) {
	if pin.Modfile == "" {
		// The pin records a module that declares no module file: the
		// digest-verified archive is the only artifact, and its file set
		// must agree with the record.
		zip, err := c.pinnedZip(ctx, modPath, v, pin, key)
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
	if b, ok, err := c.Cache.Get(modPath, v, KindMod, pin.Digest); err != nil {
		return nil, err
	} else if ok && ModfileHash(b) == pin.Modfile {
		return parseModfile(modPath, b)
	}
	b, err := c.fetch(ctx, modPath, v, KindMod)
	if err == nil {
		if got := ModfileHash(b); got != pin.Modfile {
			return nil, fmt.Errorf("%w: %s@%s modfile: expected %s, computed %s", lockfile.ErrPinMismatch, modPath, v, pin.Modfile, got)
		}
		// A pin recording no digest names no entry: the module file
		// is read from the sources at every resolution and cached by
		// nothing.
		if pin.Digest != "" {
			if err := c.Cache.Put(modPath, v, KindMod, pin.Digest, b); err != nil {
				return nil, err
			}
		}
		return parseModfile(modPath, b)
	}
	if !errors.Is(err, proxy.ErrNotHere) {
		return nil, err
	}
	// No source serves the standalone module file; the archive carries
	// the declared copy in its file set (module-archive.md).
	zip, err := c.pinnedZip(ctx, modPath, v, pin, key)
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
	key, err := c.holdToPolicy(modPath, v, pin)
	if err != nil {
		return nil, err
	}
	return c.pinnedZip(ctx, modPath, v, pin, key)
}

// pinnedZip returns the pinned pair's archive bytes, digest-verified
// against the pin: the cached entry when it verifies, else a fresh
// fetch — a cache entry failing the pin is local corruption, discarded
// and replaced (REQ-dep-cache-transparent); a fetched artifact failing
// it fails the operation (REQ-lock-digest-enforcement) — and, for a
// pin recording evidence, the evidence re-verified against the
// archive served (held), the key being the pinned one the policy of
// the day still names, nil for an identity record.
func (c *Client) pinnedZip(ctx context.Context, modPath string, v version.Version, pin lockfile.ModulePin, key *gitprov.PinnedKey) ([]byte, error) {
	if pin.Digest == "" {
		// Unreachable through this package's own pins — first use always
		// fetches the archive — but the lockfile wire format admits
		// digestless pins, and serving unverifiable bytes is not an option.
		return nil, fmt.Errorf("fetch: pin for %s@%s has no digest to verify the archive against", modPath, v)
	}
	if b, ok, err := c.Cache.Get(modPath, v, KindZip, pin.Digest); err != nil {
		return nil, err
	} else if ok {
		if _, err := archive.VerifyZip(bytes.NewReader(b), int64(len(b)), pin.Digest); err == nil {
			if err := c.held(ctx, modPath, v, pin, key, b); err != nil {
				return nil, err
			}
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
	if err := c.Cache.Put(modPath, v, KindZip, pin.Digest, b); err != nil {
		return nil, err
	}
	if err := c.held(ctx, modPath, v, pin, key, b); err != nil {
		return nil, err
	}
	return b, nil
}

// held holds a pin's evidence record to the trusted root against the
// archive served (provenance.md REQ-prov-pin-held): the cached
// envelope judged first, fetched where absent or failing, the record
// reproduced or the read refused (REQ-lock-no-silent-downgrade) —
// once per pair, digest and record for the client's life, the
// judgement a function of the archive, the record, the evidence and
// the root, none of which move under one client; the policy's hold is
// holdToPolicy's, before. A record of none has no evidence to hold; an
// identity record with no root to judge it fails before any envelope
// is read or fetched. A refusal names the explicit update.
func (c *Client) held(ctx context.Context, modPath string, v version.Version, pin lockfile.ModulePin, key *gitprov.PinnedKey, zip []byte) error {
	if pin.Provenance == (lockfile.Provenance{}) {
		return nil
	}
	if key == nil && c.TrustedRoot == nil {
		return fmt.Errorf("fetch: %s@%s: the pin records verified provenance but no trusted root is configured to re-verify it (the trustedroot setting)", modPath, v)
	}
	k := fmt.Sprintf("%s@%s@%s\x00%+v", modPath, v, pin.Digest, pin.Provenance)
	if _, ok := c.heldPairs[k]; ok {
		return nil
	}
	if err := c.ensureProv(ctx, modPath, v, pin, key, zip); err != nil {
		if errors.Is(err, lockfile.ErrProvenanceDowngrade) {
			return fmt.Errorf("%w; run pb dep update %s@%s to resolve it anew", err, modPath, v)
		}
		return err
	}
	if c.heldPairs == nil {
		c.heldPairs = map[string]struct{}{}
	}
	c.heldPairs[k] = struct{}{}
	return nil
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
	if err := c.Cache.Put(modPath, v, KindZip, digest, zip); err != nil {
		return nil, err
	}
	if hasMod {
		if err := c.Cache.Put(modPath, v, KindMod, digest, mb); err != nil {
			return nil, err
		}
	}
	if provBytes != nil {
		if err := c.Cache.Put(modPath, v, KindProv, digest, provBytes); err != nil {
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
// recorded, an identity record naming the origin's repository it was
// accepted under. Evidence that is absent — no envelope served, a kind
// no arm takes, no embedded transparency proof (REQ-prov-signed-tag),
// an identity the policy does not accept, a signature by no pinned
// key, or no identity to hold a sigstore signature to (no explicit
// rule and no derivable origin default, REQ-prov-origin-consistency)
// — leaves the subject unsigned: recorded none under allow-unsigned
// (REQ-prov-unsigned-recorded), a failure under require-provenance or
// a rule naming keys. Evidence that fails verification or binding any
// other way is tampered-with or corrupt and fails the operation
// outright (REQ-prov-tag-binding: rejected, not ignored). Sigstore
// evidence an identity arm would judge, with no trusted root to judge
// it, fails the operation under either posture: a judgement with a
// missing input has no answer (the trusted root term).
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
	// Evidence is served and an identity arm would judge it: without
	// a trusted root the judgement has no answer, under either
	// posture — a missing input, never an unsigned subject
	// (provenance.md, the trusted root term).
	if id != nil && c.TrustedRoot == nil && hasSigstore(evidence) {
		return lockfile.Provenance{}, nil, fmt.Errorf("fetch: %s@%s: sigstore provenance evidence is served but no trusted root is configured to judge it (the trustedroot setting)", modPath, v)
	}

	rec, accepted, skipped, err := c.verifyEvidence(ctx, o.Subtree, v, zip, evidence, id, dec.Keys, func(lockfile.Provenance) bool { return true })
	if err != nil {
		return lockfile.Provenance{}, nil, fmt.Errorf("%s@%s: %w", modPath, v, err)
	}
	if accepted {
		if !lockfile.NamesKey(rec) {
			// The repository the record was accepted under, the one
			// the origin default re-derives from on every later use
			// (REQ-lock-provenance-record).
			rec.Repo = o.Repo
		}
		return rec, provBytes, nil
	}
	return fail("no evidence accepted: " + strings.Join(skipped, "; "))
}

// hasSigstore reports whether any evidence object is a sigstore
// signature, the kind a trusted root judges.
func hasSigstore(evidence []provenance.Evidence) bool {
	for _, ev := range evidence {
		if kind, err := provenance.Kind(ev); err == nil && kind == gitprov.Sigstore {
			return true
		}
	}
	return false
}
