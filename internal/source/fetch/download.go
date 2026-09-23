package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/pb/internal/module/archive"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/provenance"
	"github.com/greatliontech/pb/internal/source/proxy"
)

// Download materializes a pair's full artifact set in the module cache,
// verified and pinned (dep-verbs.md REQ-dep-download): the archive, the
// standalone module file when the module declares one, the info object,
// and — when the pin records verified evidence — the provenance
// envelope, re-verified to reproduce exactly the pinned record. An
// unpinned pair runs the first-use pipeline on the way. Cached entries
// are held to the same validation as fetched bytes and discarded as
// absent when they fail (REQ-dep-cache-transparent).
func (c *Client) Download(ctx context.Context, modPath string, v version.Version) error {
	pin, ok := c.Lock.Module(modPath, v.String())
	if !ok {
		if _, err := c.firstUse(ctx, modPath, v); err != nil {
			return err
		}
		pin, _ = c.Lock.Module(modPath, v.String())
	}
	key, err := c.pinGoverned(modPath, v, pin)
	if err != nil {
		return err
	}
	zip, err := c.pinnedZip(ctx, modPath, v, pin)
	if err != nil {
		return err
	}
	if pin.Modfile != "" {
		if err := c.downloadModfile(ctx, modPath, v, pin, zip); err != nil {
			return err
		}
	}
	if err := c.downloadInfo(ctx, modPath, v); err != nil {
		return err
	}
	if pin.Provenance != (lockfile.Provenance{}) {
		if err := c.downloadProv(ctx, modPath, v, pin, key, zip); err != nil {
			return err
		}
	}
	return nil
}

// archiveModfile extracts the declared module file from the
// digest-verified archive and holds it to the pinned hash: the pin and
// the archive answer for the same file set, so any disagreement is a
// pin mismatch, not a variant.
func archiveModfile(modPath string, v version.Version, pin lockfile.ModulePin, zip []byte) ([]byte, error) {
	b, has, err := archive.ZipModuleFile(bytes.NewReader(zip), int64(len(zip)))
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, fmt.Errorf("%w: %s@%s pin records a module file but the digest-verified archive has none", lockfile.ErrPinMismatch, modPath, v)
	}
	if got := ModfileHash(b); got != pin.Modfile {
		return nil, fmt.Errorf("%w: %s@%s modfile (archive copy): expected %s, computed %s", lockfile.ErrPinMismatch, modPath, v, pin.Modfile, got)
	}
	return b, nil
}

// downloadModfile ensures the standalone module file is cached and
// consistent: a fetched standalone copy must agree with both the pinned
// hash and the archive's in-set copy (REQ-lock-modfile-consistency);
// with no source serving one, the digest-verified archive copy — the
// same bytes by that consistency — fills the cache entry.
func (c *Client) downloadModfile(ctx context.Context, modPath string, v version.Version, pin lockfile.ModulePin, zip []byte) error {
	if b, ok, err := c.Cache.Get(modPath, v, KindMod); err != nil {
		return err
	} else if ok && ModfileHash(b) == pin.Modfile {
		return nil
	}
	archiveCopy, err := archiveModfile(modPath, v, pin, zip)
	if err != nil {
		return err
	}
	standalone, err := c.fetch(ctx, modPath, v, KindMod)
	switch {
	case err == nil:
		if err := lockfile.CheckModfileConsistency(pin.Modfile, standalone, archiveCopy); err != nil {
			return fmt.Errorf("%s@%s: %w", modPath, v, err)
		}
		return c.Cache.Put(modPath, v, KindMod, standalone)
	case errors.Is(err, proxy.ErrNotHere):
		return c.Cache.Put(modPath, v, KindMod, archiveCopy)
	default:
		return err
	}
}

// downloadInfo ensures the info object is cached: parse-valid and
// naming exactly the version it is addressed by — a version-addressed
// artifact stating another version is a malformed response, not a
// variant. A cached entry failing the same check is discarded as
// absent and refetched (REQ-dep-cache-transparent).
func (c *Client) downloadInfo(ctx context.Context, modPath string, v version.Version) error {
	if b, ok, err := c.Cache.Get(modPath, v, KindInfo); err != nil {
		return err
	} else if ok && checkInfo(b, v) == nil {
		return nil
	}
	b, err := c.fetch(ctx, modPath, v, KindInfo)
	if err != nil {
		return err
	}
	if err := checkInfo(b, v); err != nil {
		return fmt.Errorf("fetch: info object for %s@%s: %w", modPath, v, err)
	}
	return c.Cache.Put(modPath, v, KindInfo, b)
}

func checkInfo(b []byte, v version.Version) error {
	info, err := proxy.ParseInfo(b)
	if err != nil {
		return err
	}
	if info.Version.String() != v.String() {
		return fmt.Errorf("names version %s", info.Version)
	}
	return nil
}

// downloadProv ensures the provenance envelope behind a verified pin is
// cached, re-verifying that some evidence object reproduces exactly the
// pinned record — held to the recorded identity, not the policy's
// current patterns, or to the recorded key as the policy of the day
// still pins it — with the same evidence classification as first use
// (verifyEvidence): tampered evidence aborts even here. A cached
// envelope failing re-verification is local state and discarded as
// absent (REQ-dep-cache-transparent); served evidence that cannot
// reproduce the record fails rather than rewriting anything
// (REQ-lock-no-silent-downgrade).
func (c *Client) downloadProv(ctx context.Context, modPath string, v version.Version, pin lockfile.ModulePin, key *gitprov.PinnedKey, zip []byte) error {
	if b, ok, err := c.Cache.Get(modPath, v, KindProv); err != nil {
		return err
	} else if ok && c.reverifyProv(ctx, modPath, v, pin, key, zip, b) == nil {
		return nil
	}
	provBytes, err := c.fetch(ctx, modPath, v, KindProv)
	if errors.Is(err, proxy.ErrNotHere) {
		return fmt.Errorf("fetch: %s@%s: pin records verified provenance but no source serves evidence: %w", modPath, v, lockfile.ErrProvenanceDowngrade)
	}
	if err != nil {
		return err
	}
	if err := c.reverifyProv(ctx, modPath, v, pin, key, zip, provBytes); err != nil {
		return err
	}
	return c.Cache.Put(modPath, v, KindProv, provBytes)
}

// reverifyProv checks that an envelope reproduces the pinned record:
// some evidence object verifies against the recorded identity — or,
// for a pinned-key record, against the recorded key as the policy of
// the day pins it, which pinGoverned resolved before anything was
// served (REQ-prov-pinned-key-recorded) — and renders exactly the
// recorded provenance facts (the downgrade guard's acceptance
// criterion).
func (c *Client) reverifyProv(ctx context.Context, modPath string, v version.Version, pin lockfile.ModulePin, key *gitprov.PinnedKey, zip, provBytes []byte) error {
	evidence, err := provenance.ParseEnvelope(provBytes)
	if err != nil {
		return err
	}
	var id *gitprov.Identity
	var keys []gitprov.PinnedKey
	if key != nil {
		keys = []gitprov.PinnedKey{*key}
	} else {
		if c.TrustedRoot == nil {
			return fmt.Errorf("fetch: %s@%s: pin records verified provenance but no trusted root is configured to re-verify it", modPath, v)
		}
		id = &gitprov.Identity{Subject: pin.Provenance.SAN, Issuer: pin.Provenance.Issuer}
	}
	o, err := c.origin(ctx, modPath)
	if err != nil {
		return err
	}
	_, accepted, skipped, err := c.verifyEvidence(ctx, o.Subtree, v, zip, evidence, id, keys, func(rec lockfile.Provenance) bool {
		return lockfile.CheckProvenanceTransition(pin.Provenance, rec) == nil
	})
	if err != nil {
		return fmt.Errorf("%s@%s: %w", modPath, v, err)
	}
	if !accepted {
		return fmt.Errorf("fetch: %s@%s: no served evidence reproduces the pinned provenance record (%s): %w", modPath, v, strings.Join(skipped, "; "), lockfile.ErrProvenanceDowngrade)
	}
	return nil
}

// pinnedKeyOf is the key a pinned-key record names among the keys the
// policy pins for the module today, by kind and fingerprint.
func (c *Client) pinnedKeyOf(modPath string, rec lockfile.Provenance) (gitprov.PinnedKey, bool) {
	if c.Policy == nil {
		return gitprov.PinnedKey{}, false
	}
	for _, k := range c.Policy.EvaluateModule(modPath).Keys {
		if string(k.Kind()) == rec.KeyKind && k.Fingerprint() == rec.KeyFingerprint {
			return k, true
		}
	}
	return gitprov.PinnedKey{}, false
}
