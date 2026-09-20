package fetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

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

// Module returns the verified module file for (modPath, v) — the
// requirement loader version selection consumes. An unpinned pair runs
// the full first-use pipeline (fetch, verify, evaluate provenance,
// record the pin); a pinned pair verifies cached or fetched bytes
// against the pin and re-evaluates nothing (the pin is the record).
func (c *Client) Module(ctx context.Context, modPath string, v version.Version) (*modfile.File, error) {
	if pin, ok := c.Lock.Module(modPath, v.String()); ok {
		return c.pinnedModule(ctx, modPath, v, pin)
	}
	return c.firstUse(ctx, modPath, v)
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
		if _, has, err := archive.ZipFile(bytes.NewReader(zip), int64(len(zip)), module.ModuleFileName); err != nil {
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
	pin, ok := c.Lock.Module(modPath, v.String())
	if !ok {
		if _, err := c.firstUse(ctx, modPath, v); err != nil {
			return nil, err
		}
		pin, _ = c.Lock.Module(modPath, v.String())
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
// complete in one step.
func (c *Client) firstUse(ctx context.Context, modPath string, v version.Version) (*modfile.File, error) {
	zip, err := c.fetch(ctx, modPath, v, KindZip)
	if err != nil {
		return nil, err
	}
	digest, _, err := archive.DigestZip(bytes.NewReader(zip), int64(len(zip)))
	if err != nil {
		return nil, err
	}
	mb, hasMod, err := archive.ZipFile(bytes.NewReader(zip), int64(len(zip)), module.ModuleFileName)
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
	if err := c.Lock.AddModule(pin); err != nil {
		return nil, err
	}
	if err := c.Cache.Put(modPath, v, KindZip, zip); err != nil {
		return nil, err
	}
	if hasMod {
		if err := c.Cache.Put(modPath, v, KindMod, mb); err != nil {
			return nil, err
		}
	}
	if provBytes != nil {
		if err := c.Cache.Put(modPath, v, KindProv, provBytes); err != nil {
			return nil, err
		}
	}
	return mf, nil
}

// evaluateProvenance fetches and evaluates a pair's provenance evidence
// against the trust policy (REQ-prov-policy-eval), returning the record
// for the pin and the envelope bytes when evidence was accepted.
//
// Classification is three-way. Evidence that verifies and matches the
// accepted identity is recorded. Evidence that is absent — no envelope
// served, no embedded transparency proof (REQ-prov-signed-tag), an
// identity the policy does not accept, or no identity to hold it to
// (no explicit rule and no derivable origin default,
// REQ-prov-origin-consistency; or no trusted root) — leaves the subject
// unsigned: recorded none under allow-unsigned
// (REQ-prov-unsigned-recorded), a failure under require-provenance.
// Evidence that fails verification or binding any other way is
// tampered-with or corrupt and fails the operation outright
// (REQ-prov-tag-binding: rejected, not ignored).
func (c *Client) evaluateProvenance(ctx context.Context, modPath string, v version.Version, zip []byte) (lockfile.Provenance, []byte, error) {
	var dec trust.Decision
	if c.Policy != nil {
		dec = c.Policy.EvaluateModule(modPath)
	}
	fail := func(reason string) (lockfile.Provenance, []byte, error) {
		if dec.Require {
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
	if c.TrustedRoot == nil {
		return fail("no trusted root is configured to verify evidence against")
	}

	// Evidence exists, so the origin is needed regardless of which
	// identity arm applies: the subject's subtree binds the tree walk.
	o, err := c.origin(ctx, modPath)
	if err != nil {
		return lockfile.Provenance{}, nil, err
	}
	var id gitprov.Identity
	if dec.Identity != nil {
		id, err = trust.ExplicitIdentity(*dec.Identity)
		if err != nil {
			return lockfile.Provenance{}, nil, err
		}
	} else {
		id, err = trust.DefaultIdentity(o.Repo)
		if errors.Is(err, trust.ErrNoDefaultIdentity) {
			return fail(err.Error())
		}
		if err != nil {
			return lockfile.Provenance{}, nil, err
		}
	}

	rec, accepted, skipped, err := c.verifyEvidence(ctx, o.Subtree, v, zip, evidence, id, func(lockfile.Provenance) bool { return true })
	if err != nil {
		return lockfile.Provenance{}, nil, fmt.Errorf("%s@%s: %w", modPath, v, err)
	}
	if accepted {
		return rec, provBytes, nil
	}
	return fail(fmt.Sprintf("no evidence accepted (%d object(s) absent or by an unaccepted identity)", skipped))
}
