package oci

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/greatliontech/gitprov"
	"github.com/greatliontech/ocifs"

	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/provenance/image"
	"github.com/greatliontech/pb/internal/provenance/image/evidence"
	"github.com/greatliontech/pb/internal/provenance/trust"
)

// Empty empties the plugin store through its own removal and
// collection and the kept evidence with it (REQ-dep-clean): an
// acquired image no longer resolves from the store, its evidence is
// gone, and a store that does not exist is empty already and is not
// created to say so.
func TestEmptyEmptiesTheStoreAndItsEvidence(t *testing.T) {
	fx := newFixture(t)
	workDir, evidenceDir := t.TempDir(), t.TempDir()
	a := newAcquirerAt(t, fx, &lockfile.File{}, &trust.Policy{}, nil, workDir, evidenceDir)
	if _, err := a.Acquire(ctx, fx.host+"/org/plugin:v1"); err != nil {
		t.Fatal(err)
	}
	digest, err := v1.NewHash(fx.digest)
	if err != nil {
		t.Fatal(err)
	}
	kept := evidence.Store{Dir: evidenceDir}
	if err := kept.Save(digest, []image.Carrier{{Where: "here", Value: gitprov.SigstoreBundle{JSON: []byte(`{"kept":true}`)}}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(evidenceDir, "sha256", ".keep-interrupted"), filepath.Join(evidenceDir, "notes.txt")} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a.Close()

	keptImages, err := Empty(ctx, workDir, evidenceDir)
	if err != nil {
		t.Fatalf("Empty: %v", err)
	}
	if len(keptImages) != 0 {
		t.Fatalf("kept = %v with no live mount", keptImages)
	}
	store, err := ocifs.New(ocifs.WithWorkDir(workDir))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Resolve(ctx, fx.host+"/org/plugin:v1", ocifs.ResolveUnder(ocifs.PullNever)); err == nil {
		t.Fatal("the acquired image still resolves from the emptied store")
	}
	if _, ok := kept.Load(digest); ok {
		t.Fatal("the kept evidence survived the emptying")
	}
	if entries, err := os.ReadDir(evidenceDir); err != nil || len(entries) != 1 || entries[0].Name() != "notes.txt" {
		t.Fatalf("evidence store after the emptying: %v, %v; want the stranger alone, entries and temporaries gone", entries, err)
	}

	absent := filepath.Join(t.TempDir(), "never")
	if kept, err := Empty(ctx, absent, filepath.Join(absent, "evidence")); err != nil || len(kept) != 0 {
		t.Fatalf("Empty over an absent store: %v, %v", kept, err)
	}
	if _, err := os.Stat(absent); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("an absent store was created by its emptying: %v", err)
	}
}

// The emptying holds the collection to its word: a collection halted
// by a live row of another version, or one that deferred a deletion,
// fails the emptying naming why rather than reporting the store
// emptied (REQ-dep-clean).
func TestEmptyFailsWhenCollectionLeavesContent(t *testing.T) {
	for name, tc := range map[string]struct {
		res  ocifs.GCResult
		want string
	}{
		"halted by another version": {ocifs.GCResult{ForeignVersionRows: []string{"future"}}, "halted by live rows of another ocifs version (future)"},
		"a deletion deferred":       {ocifs.GCResult{Deferred: []string{"blobs/sha256/ab"}}, "1 item(s) could not be deleted (blobs/sha256/ab)"},
	} {
		t.Run(name, func(t *testing.T) {
			s := &stubEmptier{res: tc.res}
			if _, err := emptyThrough(ctx, s); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "not emptied") {
				t.Fatalf("emptying under %s: %v", name, err)
			}
			if !s.removed || !s.collected {
				t.Fatalf("removed %v, collected %v: the roots are severed and the collection run before its word is read", s.removed, s.collected)
			}
		})
	}
	s := &stubEmptier{kept: []string{"sha256:" + strings.Repeat("ab", 32)}}
	if kept, err := emptyThrough(ctx, s); err != nil || len(kept) != 1 || kept[0] != s.kept[0] {
		t.Fatalf("a clean collection: %v, %v", kept, err)
	}
}

type stubEmptier struct {
	kept               []string
	res                ocifs.GCResult
	removed, collected bool
}

func (s *stubEmptier) RemoveAll(context.Context) ([]string, error) {
	s.removed = true
	return s.kept, nil
}

func (s *stubEmptier) GC(context.Context, ...ocifs.GCOption) (*ocifs.GCResult, error) {
	s.collected = true
	return &s.res, nil
}

// An acquirer holds every image it exported until it closes: an
// emptying in another process reports the image among the kept and
// leaves its export, which the run reads (REQ-dep-clean).
func TestAcquirerHoldsItsExportsThroughEmptying(t *testing.T) {
	fx := newFixture(t)
	workDir, evidenceDir := t.TempDir(), t.TempDir()
	a := newAcquirerAt(t, fx, &lockfile.File{}, &trust.Policy{}, nil, workDir, evidenceDir)
	got, err := a.Acquire(ctx, fx.host+"/org/plugin:v1")
	if err != nil {
		t.Fatal(err)
	}
	rootfs := rootfsOf(t, got)
	kept, err := Empty(ctx, workDir, evidenceDir)
	if err != nil {
		t.Fatalf("Empty under a running acquirer: %v", err)
	}
	// The hold is over what the store materialized: the platform
	// child of the pinned index, not the index.
	if len(kept) != 1 || !strings.HasPrefix(kept[0], "sha256:") || kept[0] == fx.digest {
		t.Fatalf("kept = %v, want the held child manifest", kept)
	}
	if entries, err := os.ReadDir(rootfs); err != nil || len(entries) == 0 {
		t.Fatalf("the held export after the emptying: %v, %v", entries, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if kept, err := Empty(ctx, workDir, evidenceDir); err != nil || len(kept) != 0 {
		t.Fatalf("Empty after the acquirer's close: %v, %v", kept, err)
	}
	if _, err := os.Stat(rootfs); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the export after the release: %v", err)
	}
}
