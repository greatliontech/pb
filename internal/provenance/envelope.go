// Package provenance implements the module provenance verification
// core: parsing provenance envelopes and verifying git-signed-tag
// evidence fully offline. The cryptographic leg — CMS signature, Fulcio
// chain, embedded Rekor transparency proof, identity matching — is
// delegated to github.com/greatliontech/gitprov; this package owns the
// wire envelope and the binding chain that ties an accepted signature
// to what it vouches for: the tag names the version, the tag references
// the commit, and the commit's tree at the module root matches the
// archive's recomputed tree hash (REQ-prov-tag-binding).
//
// Who may sign is not decided here: Verify takes the caller's
// gitprov.Identity, and the trust-policy layer constructs it. Whether
// transparency is required is not caller policy at this layer — pb
// always requires it, because evidence without an embedded proof is
// defined by REQ-prov-signed-tag to be unverifiable and absent.
package provenance

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/greatliontech/pb/internal/archive"
)

// Evidence is one git-signed-tag evidence object from a provenance
// envelope (module-proxy.md REQ-proxy-prov-envelope): the raw signed
// tag, the raw commit it references, and the raw trees on the walk from
// the commit's root tree to the module root.
type Evidence struct {
	Format   archive.ObjectFormat
	Tag      []byte
	Commit   []byte
	TreePath [][]byte
}

// ErrEnvelopeMalformed marks an envelope or evidence object violating
// the wire contract; ErrFormatVersion marks a well-formed envelope
// whose formatVersion this consumer does not support
// (REQ-proxy-prov-unknown fails only on that).
var (
	ErrEnvelopeMalformed = errors.New("provenance: malformed envelope")
	ErrFormatVersion     = errors.New("provenance: unsupported envelope formatVersion")
)

type envelopeWire struct {
	FormatVersion *int              `json:"formatVersion"`
	Evidence      []json.RawMessage `json:"evidence"`
}

type gitSignedTagWire struct {
	Type         string `json:"type"`
	ObjectFormat string `json:"objectFormat"`
	Tag          string `json:"tag"`
	Commit       string `json:"commit"`
	// Pointer: the clause makes treePath mandatory, and the empty list
	// is its one valid empty value — so absence must be distinguishable
	// from [], or the object would have two spellings.
	TreePath *[]string `json:"treePath"`
}

// ParseEnvelope decodes a provenance envelope, returning the
// git-signed-tag evidence it carries. Evidence objects of unrecognized
// type are ignored (REQ-proxy-prov-unknown); an entry that is not a
// JSON object carrying a string type, or a recognized entry violating
// the wire contract, makes the envelope malformed.
func ParseEnvelope(data []byte) ([]Evidence, error) {
	var env envelopeWire
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEnvelopeMalformed, err)
	}
	if env.FormatVersion == nil {
		return nil, fmt.Errorf("%w: missing formatVersion", ErrEnvelopeMalformed)
	}
	if *env.FormatVersion != 1 {
		return nil, fmt.Errorf("%w: %d", ErrFormatVersion, *env.FormatVersion)
	}
	out := []Evidence{}
	for i, raw := range env.Evidence {
		var hdr struct {
			Type *string `json:"type"`
		}
		if err := json.Unmarshal(raw, &hdr); err != nil || hdr.Type == nil {
			return nil, fmt.Errorf("%w: evidence[%d] is not an object carrying a string type", ErrEnvelopeMalformed, i)
		}
		if *hdr.Type != "git-signed-tag" {
			continue
		}
		var w gitSignedTagWire
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("%w: evidence[%d]: %v", ErrEnvelopeMalformed, i, err)
		}
		ev, err := w.decode()
		if err != nil {
			return nil, fmt.Errorf("%w: evidence[%d]: %v", ErrEnvelopeMalformed, i, err)
		}
		out = append(out, ev)
	}
	return out, nil
}

func (w gitSignedTagWire) decode() (Evidence, error) {
	f := archive.ObjectFormat(w.ObjectFormat)
	if f != archive.SHA1 && f != archive.SHA256 {
		return Evidence{}, fmt.Errorf("objectFormat %q", w.ObjectFormat)
	}
	tag, err := decodeCanonicalB64("tag", w.Tag)
	if err != nil {
		return Evidence{}, err
	}
	commit, err := decodeCanonicalB64("commit", w.Commit)
	if err != nil {
		return Evidence{}, err
	}
	if w.TreePath == nil {
		return Evidence{}, fmt.Errorf("missing treePath")
	}
	trees := make([][]byte, len(*w.TreePath))
	for i, s := range *w.TreePath {
		if trees[i], err = decodeCanonicalB64(fmt.Sprintf("treePath[%d]", i), s); err != nil {
			return Evidence{}, err
		}
	}
	return Evidence{Format: f, Tag: tag, Commit: commit, TreePath: trees}, nil
}

// decodeCanonicalB64 enforces the one-spelling rule: standard alphabet
// with padding, no embedded whitespace, no nonzero spare trailing bits
// — Strict rejects spare bits, and re-encoding equality rejects every
// other non-canonical spelling — and a raw git object is never empty.
func decodeCanonicalB64(field, s string) ([]byte, error) {
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", field, err)
	}
	if base64.StdEncoding.EncodeToString(b) != s {
		return nil, fmt.Errorf("%s: non-canonical base64", field)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("%s: empty object", field)
	}
	return b, nil
}
