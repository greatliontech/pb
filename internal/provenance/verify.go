package provenance

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/pb/internal/module/archive"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/version"
)

// ErrNoTransparency marks evidence whose signature carries no embedded
// transparency proof: unverifiable and treated as absent
// (REQ-prov-signed-tag) — distinct from rejection, so an allow-unsigned
// policy can still resolve the subject while a require-provenance
// policy fails it as evidence-less, not as evidence-invalid.
var ErrNoTransparency = errors.New("provenance: evidence carries no embedded transparency proof")

// Subject identifies what accepted evidence must vouch for: the version
// being resolved, the tag namespace that names it, and the module
// root's path within the repository ("" at the root). The namespace
// and the root are two things (REQ-resolve-release-tags): a module
// declared at the tagged commit is named by its subtree's own tag, so
// the namespace is the subtree; a synthesized subtree is named by the
// repository's tag, the namespace "", while its root is still the
// subtree the tree binding walks to. The expected tag name is the
// version itself in the repository's namespace, prefixed with the
// namespace otherwise.
type Subject struct {
	Version   version.Version
	Namespace string
	Subtree   string
}

func (s Subject) tagName() string {
	if s.Namespace == "" {
		return s.Version.String()
	}
	return s.Namespace + "/" + s.Version.String()
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
	if err := bind(ev, sub, computedTree); err != nil {
		return nil, err
	}
	return vi, nil
}

// VerifyPinned verifies one git-signed-tag evidence object against the
// subject under pinned keys (REQ-prov-pinned-key-eval,
// REQ-prov-tag-binding): the tag's OpenPGP or SSH signature verifies
// against exactly the keys, offline and with no transparency proof
// consulted — gitprov's pinned-key verification — and the binding
// chain then holds as for a sigstore signature. A signature no pinned
// key made is gitprov.ErrUnpinnedKey, a sigstore signature
// gitprov.ErrSignatureKind — each a non-acceptance a caller may
// classify; every other failure is a rejection.
func VerifyPinned(ev Evidence, sub Subject, computedTree []byte, keys []gitprov.PinnedKey) (*gitprov.VerifiedKey, error) {
	obj := gitprov.Object{Kind: gitprov.Tag, Format: gitprov.ObjectFormat(ev.Format), Raw: ev.Tag}
	vk, err := gitprov.VerifyPinned(obj, keys)
	if err != nil {
		return nil, fmt.Errorf("provenance: %w", err)
	}
	if err := bind(ev, sub, computedTree); err != nil {
		return nil, err
	}
	return vk, nil
}

// Kind is the kind of the evidence's signature (gitprov's reading of
// the armor label), so a caller routes the object to the verification
// its kind takes; an unsigned or malformed tag is an error.
func Kind(ev Evidence) (gitprov.SignatureKind, error) {
	return gitprov.SignatureKindOf(gitprov.Object{Kind: gitprov.Tag, Format: gitprov.ObjectFormat(ev.Format), Raw: ev.Tag})
}

// bind holds a cryptographically accepted tag to what it vouches for
// (REQ-prov-tag-binding): from here the header fields are
// signer-vouched statements to hold the evidence to, not
// attacker-controlled inputs to sanitize — the tag names the subject's
// version, references a commit object (never another tag), that
// reference is recomputed from the commit bytes in the stated format,
// and the commit's tree at the module root, reached through the
// verified treePath walk, equals the archive's recomputed tree hash.
func bind(ev Evidence, sub Subject, computedTree []byte) error {
	hdr, err := parseTagHeader(ev.Format, ev.Tag)
	if err != nil {
		return err
	}
	if hdr.targetType != "commit" {
		return fmt.Errorf("provenance: tag targets a %s, not a commit", hdr.targetType)
	}
	if want := sub.tagName(); hdr.name != want {
		return fmt.Errorf("provenance: tag %q does not name %q", hdr.name, want)
	}
	commitHash, err := archive.ObjectHash(ev.Format, "commit", ev.Commit)
	if err != nil {
		return fmt.Errorf("provenance: hash commit: %w", err)
	}
	if !bytes.Equal(commitHash, hdr.object) {
		return fmt.Errorf("provenance: commit object does not match the signed tag's object %x", hdr.object)
	}
	if err := archive.VerifyTreeBinding(ev.Format, ev.Commit, sub.Subtree, ev.TreePath, computedTree); err != nil {
		return fmt.Errorf("provenance: %w", err)
	}
	return nil
}

// Record renders the lockfile provenance record for accepted evidence
// (REQ-lock-provenance-record): the evidence type, its object format,
// the signed tag's own hash in that format, and the verified identity.
func Record(ev Evidence, vi *gitprov.VerifiedIdentity) (lockfile.Provenance, error) {
	rec, err := signedObject(ev)
	if err != nil {
		return lockfile.Provenance{}, err
	}
	rec.Type, rec.SAN, rec.Issuer = lockfile.ProvenanceGitSignedTag, vi.Subject, vi.Issuer
	return rec, nil
}

// RecordPinned renders the lockfile provenance record for evidence
// accepted under a pinned key (REQ-lock-pinned-key-record,
// REQ-prov-pinned-key-recorded): the evidence type, the signed object
// as Record has it, and the key's kind and fingerprint in place of an
// identity.
func RecordPinned(ev Evidence, vk *gitprov.VerifiedKey) (lockfile.Provenance, error) {
	rec, err := signedObject(ev)
	if err != nil {
		return lockfile.Provenance{}, err
	}
	rec.Type, rec.KeyKind, rec.KeyFingerprint = lockfile.ProvenanceGitPinnedKey, string(vk.Kind), vk.Fingerprint
	return rec, nil
}

// signedObject is a record's signed object: the evidence's format and
// the signed tag's own hash in it.
func signedObject(ev Evidence) (lockfile.Provenance, error) {
	h, err := archive.ObjectHash(ev.Format, "tag", ev.Tag)
	if err != nil {
		return lockfile.Provenance{}, fmt.Errorf("provenance: hash tag: %w", err)
	}
	return lockfile.Provenance{ObjectFormat: string(ev.Format), Object: hex.EncodeToString(h)}, nil
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
