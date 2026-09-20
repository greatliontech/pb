package evidence_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/gitprov/sigstoretest"
	"github.com/greatliontech/pb/internal/provenance/image"
	"github.com/greatliontech/pb/internal/provenance/image/evidence"
)

var digest = v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("5a", 32)}

// Kept evidence comes back as it was taken, in order, both carrier
// shapes intact, at the store's path (REQ-prov-plugin-evidence-store).
func TestStoreRoundTrips(t *testing.T) {
	s := sigstoretest.New(t)
	bundle := s.Bundle(t, digest.String(), "signer@example.com", "https://accounts.example.com", sigstoretest.BundleOptions{})
	env := s.Envelope(t, digest.String(), "signer@example.com", "https://accounts.example.com", sigstoretest.EnvelopeOptions{WithChain: true, Timestamp: true})
	store := evidence.Store{Dir: t.TempDir()}
	if _, ok := store.Load(digest); ok {
		t.Fatal("an empty store holds evidence")
	}
	in := []image.Carrier{{Where: "referrer a", Value: bundle}, {Where: "tag b layer 0", Value: env}}
	if err := store.Save(digest, in); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.Dir, "sha256", digest.Hex+".json")); err != nil {
		t.Fatalf("the entry is not at the store's path: %v", err)
	}
	out, ok := store.Load(digest)
	if !ok || len(out) != 2 {
		t.Fatalf("Load = %v %v", out, ok)
	}
	if b, isBundle := out[0].Value.(gitprov.SigstoreBundle); !isBundle || !bytes.Equal(b.JSON, bundle.JSON) || out[0].Where != "referrer a" {
		t.Fatalf("the bundle came back as %+v", out[0])
	}
	if e, isEnvelope := out[1].Value.(gitprov.SimpleSigningEnvelope); !isEnvelope || !bytes.Equal(e.Payload, env.Payload) || e.Signature != env.Signature || e.Certificate != env.Certificate || e.Chain != env.Chain || e.RekorBundle != env.RekorBundle || e.RFC3161Timestamp != env.RFC3161Timestamp || out[1].Where != "tag b layer 0" {
		t.Fatalf("the envelope came back as %+v", out[1])
	}
	// Saving again replaces.
	if err := store.Save(digest, in[:1]); err != nil {
		t.Fatal(err)
	}
	if out, _ := store.Load(digest); len(out) != 1 {
		t.Fatalf("a second save did not replace: %d carriers", len(out))
	}
	// Nothing is kept for no carriers, and what was kept stays.
	if err := store.Save(digest, nil); err != nil {
		t.Fatal(err)
	}
	if out, _ := store.Load(digest); len(out) != 1 {
		t.Fatalf("saving nothing changed the entry: %d carriers", len(out))
	}
	// No temporary file survives a save.
	entries, err := os.ReadDir(filepath.Join(store.Dir, "sha256"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("the store holds %d entries, want the one", len(entries))
	}
}

// An entry that cannot be read — not the store's shape, over the
// bound, a directory, unreadable — is absent, never an error: kept
// evidence carries no authority (REQ-prov-plugin-evidence-kept).
func TestStoreUnreadableIsAbsent(t *testing.T) {
	store := evidence.Store{Dir: t.TempDir()}
	dir := filepath.Join(store.Dir, "sha256")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dir, digest.Hex+".json")
	for name, body := range map[string]string{
		"not JSON":        "{",
		"no carriers":     `{"carriers":[]}`,
		"both shapes":     `{"carriers":[{"where":"x","bundle":"e30=","envelope":{"payload":""}}]}`,
		"neither shape":   `{"carriers":[{"where":"x"}]}`,
		"unknown carrier": `{"carriers":[{"where":"x","attestation":{}}]}`,
		"over the bound":  `{"carriers":[{"where":"` + strings.Repeat("x", evidence.MaxEntryBytes) + `","bundle":"e30="}]}`,
		"padded past it":  `{"carriers":[{"where":"x","bundle":"e30="}]}` + strings.Repeat(" ", evidence.MaxEntryBytes),
	} {
		if err := os.WriteFile(entry, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, ok := store.Load(digest); ok || out != nil {
			t.Fatalf("%s: Load = %v %v, want absent", name, out, ok)
		}
	}
	os.Remove(entry)
	if err := os.Mkdir(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, ok := store.Load(digest); ok || out != nil {
		t.Fatalf("a directory at the entry: Load = %v %v, want absent", out, ok)
	}
	os.Remove(entry)
	if os.Geteuid() != 0 {
		if err := os.WriteFile(entry, []byte(`{"carriers":[{"where":"x","bundle":"e30="}]}`), 0o000); err != nil {
			t.Fatal(err)
		}
		if out, ok := store.Load(digest); ok || out != nil {
			t.Fatalf("an unreadable entry: Load = %v %v, want absent", out, ok)
		}
	}
}

// A carrier of no kept shape, or an empty bundle, is refused rather
// than kept mangled.
func TestStoreRefusesForeignShapes(t *testing.T) {
	store := evidence.Store{Dir: t.TempDir()}
	if err := store.Save(digest, []image.Carrier{{Where: "x", Value: gitprov.SigstoreBundle{}}}); err == nil {
		t.Fatal("an empty bundle was kept")
	}
	if err := store.Save(digest, []image.Carrier{{Where: "x", Value: nil}}); err == nil {
		t.Fatal("a carrier of no shape was kept")
	}
	if _, ok := store.Load(digest); ok {
		t.Fatal("a refused save left an entry")
	}
}
