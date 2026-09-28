package oci

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/greatliontech/ocifs"
	"github.com/greatliontech/pb/internal/provenance/image/evidence"
)

// Empty empties the plugin store at workDir through the store's own
// removal and collection — every root severed at once, the content
// collected without its retention grace — and the evidence kept at
// evidenceDir with it, returning the platform manifests of images a
// live run's hold or a live mount kept (dep-verbs.md REQ-dep-clean).
// A store that does not exist is empty already, and is not created
// to say so.
func Empty(ctx context.Context, workDir, evidenceDir string) ([]string, error) {
	kept, err := emptyStore(ctx, workDir)
	if err != nil {
		return nil, err
	}
	if err := (evidence.Store{Dir: evidenceDir}).Empty(); err != nil {
		return nil, err
	}
	return kept, nil
}

func emptyStore(ctx context.Context, workDir string) ([]string, error) {
	if _, err := os.Stat(workDir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("oci: plugin store: %w", err)
	}
	store, err := ocifs.New(ocifs.WithWorkDir(workDir))
	if err != nil {
		return nil, fmt.Errorf("oci: plugin store: %w", err)
	}
	defer store.Close()
	return emptyThrough(ctx, store)
}

// storeEmptier is the store's emptying surface: the removal of every
// root and a collection reporting what it left.
type storeEmptier interface {
	RemoveAll(ctx context.Context) ([]string, error)
	GC(ctx context.Context, opts ...ocifs.GCOption) (*ocifs.GCResult, error)
}

// emptyThrough severs every root and collects without the grace, and
// holds the collection to its word: emptied means collected, so a
// collection halted by a live row of another version, or one that
// could not delete what it condemned, is the verb's failure naming
// why, never a report calling gone what stayed.
func emptyThrough(ctx context.Context, s storeEmptier) ([]string, error) {
	kept, err := s.RemoveAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("oci: plugin store: %w", err)
	}
	res, err := s.GC(ctx, ocifs.GCIgnoreGrace())
	if err != nil {
		return nil, fmt.Errorf("oci: plugin store: %w", err)
	}
	if len(res.ForeignVersionRows) > 0 {
		return nil, fmt.Errorf("oci: plugin store: collection halted by live rows of another ocifs version (%s): the store is not emptied", strings.Join(res.ForeignVersionRows, ", "))
	}
	if len(res.Deferred) > 0 {
		return nil, fmt.Errorf("oci: plugin store: %d item(s) could not be deleted (%s): the store is not emptied", len(res.Deferred), res.Deferred[0])
	}
	return kept, nil
}
