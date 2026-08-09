package provenance

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/pb/internal/archive"
	"github.com/greatliontech/pb/internal/lockfile"
	"github.com/greatliontech/pb/internal/version"
)

// ErrNoTransparency marks evidence whose signature carries no embedded
// transparency proof: unverifiable and treated as absent
// (REQ-prov-signed-tag) — distinct from rejection, so an allow-unsigned
// policy can still resolve the subject while a require-provenance
// policy fails it as evidence-less, not as evidence-invalid.
var ErrNoTransparency = errors.New("provenance: evidence carries no embedded transparency proof")

// Subject identifies what accepted evidence must vouch for: the version
// being resolved and the module root's path within the repository (""
// at the root). The expected tag name follows
// REQ-resolve-release-tags: the version itself at the root, prefixed
// with the subtree path for a subtree module.
type Subject struct {
	Version version.Version
	Subtree string
}

func (s Subject) tagName() string {
	if s.Subtree == "" {
		return s.Version.String()
	}
	return s.Subtree + "/" + s.Version.String()
}

// Verify verifies one git-signed-tag evidence object against the
// subject, fully offline (REQ-prov-offline, REQ-prov-signed-tag,
// REQ-prov-tag-binding):
//
//   - the CMS signature over the raw tag bytes validates against a
//     Fulcio chain ending in the pinned trusted root, its embedded
//     Rekor proof validates against the root's log keys, and the
//     identity matches id — all via gitprov;
//   - the signed tag names the subject's version, references a commit
//     object (never another tag), and that reference is recomputed:
//     the commit bytes hash, in the evidence's stated format, to
//     exactly the tag's object field;
//   - the commit's tree at the module root, reached through the
//     verified treePath walk, equals computedTree — the archive's
//     recomputed tree hash in the same format.
//
// Evidence with no embedded proof returns ErrNoTransparency (absent,
// per REQ-prov-signed-tag); every other failure is a rejection. The
// stated object format is attacker-supplied wire input, but a mislabel
// fails closed: the signed tag's own object field pins the true format
// by hash length and value, and every link is recomputed in the stated
// format rather than trusted.
func Verify(ctx context.Context, ev Evidence, sub Subject, computedTree []byte, id gitprov.Identity, root *gitprov.TrustedRoot) (*gitprov.VerifiedIdentity, error) {
	obj := gitprov.Object{Kind: gitprov.Tag, Format: gitprov.ObjectFormat(ev.Format), Raw: ev.Tag}

	has, err := gitprov.HasEmbeddedRekor(obj)
	if err != nil {
		return nil, fmt.Errorf("provenance: evidence signature: %w", err)
	}
	if !has {
		return nil, ErrNoTransparency
	}

	vi, err := gitprov.Verify(ctx, obj, id, root, true)
	if err != nil {
		return nil, fmt.Errorf("provenance: %w", err)
	}

	// Binding runs only on a cryptographically accepted tag: from here
	// the header fields are signer-vouched statements to hold the
	// evidence to, not attacker-controlled inputs to sanitize.
	hdr, err := parseTagHeader(ev.Format, ev.Tag)
	if err != nil {
		return nil, err
	}
	if hdr.targetType != "commit" {
		return nil, fmt.Errorf("provenance: tag targets a %s, not a commit", hdr.targetType)
	}
	if want := sub.tagName(); hdr.name != want {
		return nil, fmt.Errorf("provenance: tag %q does not name %q", hdr.name, want)
	}
	commitHash, err := archive.ObjectHash(ev.Format, "commit", ev.Commit)
	if err != nil {
		return nil, fmt.Errorf("provenance: hash commit: %w", err)
	}
	if !bytes.Equal(commitHash, hdr.object) {
		return nil, fmt.Errorf("provenance: commit object does not match the signed tag's object %x", hdr.object)
	}
	if err := archive.VerifyTreeBinding(ev.Format, ev.Commit, sub.Subtree, ev.TreePath, computedTree); err != nil {
		return nil, fmt.Errorf("provenance: %w", err)
	}
	return vi, nil
}

// Record renders the lockfile provenance record for accepted evidence
// (REQ-lock-provenance-record): the evidence type, its object format,
// the signed tag's own hash in that format, and the verified identity.
func Record(ev Evidence, vi *gitprov.VerifiedIdentity) (lockfile.Provenance, error) {
	h, err := archive.ObjectHash(ev.Format, "tag", ev.Tag)
	if err != nil {
		return lockfile.Provenance{}, fmt.Errorf("provenance: hash tag: %w", err)
	}
	return lockfile.Provenance{
		Type:         "git-signed-tag",
		ObjectFormat: string(ev.Format),
		Object:       hex.EncodeToString(h),
		SAN:          vi.Subject,
		Issuer:       vi.Issuer,
	}, nil
}

// tagHeader is the binding-relevant header block of a raw annotated
// tag: the referenced object hash, its type, and the tag name.
type tagHeader struct {
	object     []byte
	targetType string
	name       string
}

// parseTagHeader reads the fixed leading headers of a raw tag object —
// git writes exactly object, type, tag in that order — strictly: a
// deviation is a malformed object, not a shape to accommodate.
func parseTagHeader(f archive.ObjectFormat, raw []byte) (tagHeader, error) {
	hashSize, err := f.Size()
	if err != nil {
		return tagHeader{}, fmt.Errorf("provenance: %w", err)
	}
	rest := raw
	line := func(prefix string) (string, error) {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			return "", fmt.Errorf("provenance: malformed tag object: truncated header")
		}
		l := string(rest[:i])
		rest = rest[i+1:]
		if len(l) <= len(prefix) || l[:len(prefix)] != prefix {
			return "", fmt.Errorf("provenance: malformed tag object: want %q header, got %q", prefix, l)
		}
		return l[len(prefix):], nil
	}
	objHex, err := line("object ")
	if err != nil {
		return tagHeader{}, err
	}
	if len(objHex) != 2*hashSize {
		return tagHeader{}, fmt.Errorf("provenance: tag object field is %d hex digits, want %d for %s", len(objHex), 2*hashSize, f)
	}
	object, err := hex.DecodeString(objHex)
	if err != nil {
		return tagHeader{}, fmt.Errorf("provenance: tag object field: %v", err)
	}
	targetType, err := line("type ")
	if err != nil {
		return tagHeader{}, err
	}
	name, err := line("tag ")
	if err != nil {
		return tagHeader{}, err
	}
	return tagHeader{object: object, targetType: targetType, name: name}, nil
}
