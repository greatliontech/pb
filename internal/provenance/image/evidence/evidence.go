// Package evidence keeps a plugin image's signature evidence with its
// content: the carriers a fetch took, keyed by the digest, so a later
// acquisition judges them first and fetches only when they hold none
// the policy accepts (provenance.md REQ-prov-plugin-evidence-kept,
// REQ-prov-plugin-evidence-store). What is kept carries no authority
// — it is judged exactly as fetched evidence is — and what cannot be
// read is absent.
package evidence

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-git/go-billy/v6/osfs"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/pb/internal/atomicfile"
	"github.com/greatliontech/pb/internal/provenance/image"
)

// MaxEntryBytes bounds a kept entry (REQ-prov-plugin-evidence-store):
// carriers are kilobytes each and a fetch keeps a handful, so an
// entry over the bound is not the store's and is absent.
const MaxEntryBytes = 64 << 20

// Store is the evidence store rooted at Dir.
type Store struct {
	Dir string
}

// document is the store's entry for one digest.
type document struct {
	Carriers []carrier `json:"carriers"`
}

// carrier is one kept carrier: where it was found and one of its
// two shapes.
type carrier struct {
	Where    string    `json:"where"`
	Bundle   []byte    `json:"bundle,omitempty"`
	Envelope *envelope `json:"envelope,omitempty"`
}

// envelope is a simple-signing envelope's parts as kept.
type envelope struct {
	Payload          []byte `json:"payload"`
	Signature        string `json:"signature,omitempty"`
	Certificate      string `json:"certificate,omitempty"`
	Chain            string `json:"chain,omitempty"`
	RekorBundle      string `json:"rekorBundle,omitempty"`
	RFC3161Timestamp string `json:"rfc3161Timestamp,omitempty"`
}

func (s Store) path(digest v1.Hash) string {
	return filepath.Join(s.Dir, digest.Algorithm, digest.Hex+".json")
}

// Load is the evidence kept for digest, in discovery order, and
// whether any is kept; an entry that cannot be read — missing,
// unreadable, over the bound, not the store's shape — is absent,
// never an error: kept evidence carries no authority and a fetch
// replaces it (REQ-prov-plugin-evidence-kept).
func (s Store) Load(digest v1.Hash) ([]image.Carrier, bool) {
	f, err := os.Open(s.path(digest))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxEntryBytes+1))
	if err != nil || len(raw) > MaxEntryBytes {
		return nil, false
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false
	}
	out := make([]image.Carrier, 0, len(doc.Carriers))
	for _, c := range doc.Carriers {
		switch {
		case c.Bundle != nil && c.Envelope == nil:
			out = append(out, image.Carrier{Where: c.Where, Value: gitprov.SigstoreBundle{JSON: c.Bundle}})
		case c.Envelope != nil && c.Bundle == nil:
			e := c.Envelope
			out = append(out, image.Carrier{Where: c.Where, Value: gitprov.SimpleSigningEnvelope{
				Payload: e.Payload, Signature: e.Signature, Certificate: e.Certificate,
				Chain: e.Chain, RekorBundle: e.RekorBundle, RFC3161Timestamp: e.RFC3161Timestamp,
			}})
		default:
			return nil, false
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// Save keeps carriers for digest, replacing what was kept, written
// atomically and whole; nothing is kept for no carriers.
func (s Store) Save(digest v1.Hash, carriers []image.Carrier) error {
	if len(carriers) == 0 {
		return nil
	}
	doc := document{Carriers: make([]carrier, 0, len(carriers))}
	for _, c := range carriers {
		switch v := c.Value.(type) {
		case gitprov.SigstoreBundle:
			if len(v.JSON) == 0 {
				return fmt.Errorf("evidence: %s: an empty bundle", c.Where)
			}
			doc.Carriers = append(doc.Carriers, carrier{Where: c.Where, Bundle: v.JSON})
		case gitprov.SimpleSigningEnvelope:
			doc.Carriers = append(doc.Carriers, carrier{Where: c.Where, Envelope: &envelope{
				Payload: v.Payload, Signature: v.Signature, Certificate: v.Certificate,
				Chain: v.Chain, RekorBundle: v.RekorBundle, RFC3161Timestamp: v.RFC3161Timestamp,
			}})
		default:
			return fmt.Errorf("evidence: %s: a carrier of no kept shape (%T)", c.Where, c.Value)
		}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("evidence: %w", err)
	}
	if err := atomicfile.Write(osfs.New(s.Dir), filepath.Join(digest.Algorithm, digest.Hex+".json"), ".keep-", raw); err != nil {
		return fmt.Errorf("evidence: %w", err)
	}
	return nil
}
