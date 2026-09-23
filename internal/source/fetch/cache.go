package fetch

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	"github.com/go-git/go-billy/v6"
	"github.com/greatliontech/pb/internal/atomicfile"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/source/proxy"
)

// Artifact kinds — the cache-entry and endpoint suffixes of one module
// version's artifacts (dep-verbs.md REQ-dep-cache-layout).
const (
	KindInfo = "info"
	KindMod  = "mod"
	KindZip  = "zip"
	KindProv = "prov"
)

// Cache is the module cache (dep-verbs.md, the module cache term):
// fetched, verified version-addressed artifacts under a filesystem
// rooted at the cache directory. The cache carries no authority
// (REQ-dep-cache-transparent): nothing here verifies — consumers hold
// every entry they read to the same digests and pins as fetched bytes,
// and discard an entry that fails as if it were absent.
type Cache struct {
	FS billy.Filesystem
}

// entryPath is the REQ-dep-cache-layout location of one artifact:
// <escaped module path>/@v/<escaped version>.<kind>, sharing the proxy
// protocol's escaping and its case-insensitivity rationale.
func entryPath(modPath string, v version.Version, kind string) string {
	return path.Join(proxy.Escape(modPath), "@v", proxy.Escape(v.String())+"."+kind)
}

// Get reads a cached artifact, reporting absence without error.
func (c *Cache) Get(modPath string, v version.Version, kind string) ([]byte, bool, error) {
	f, err := c.FS.Open(entryPath(modPath, v, kind))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("fetch: reading cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, false, fmt.Errorf("fetch: reading cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	return b, true, nil
}

// Put writes a cached artifact atomically and whole through the shared
// discipline, so no reader ever observes a partial entry
// (REQ-dep-cache-layout). Callers write only verified bytes; a
// replacement of an existing entry happens only on the discard-refetch
// path, where the replacing bytes verified against the same pin the
// discarded ones failed.
func (c *Cache) Put(modPath string, v version.Version, kind string, data []byte) error {
	p := entryPath(modPath, v, kind)
	if err := c.FS.MkdirAll(path.Dir(p), 0o755); err != nil {
		return fmt.Errorf("fetch: writing cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	if err := atomicfile.Write(c.FS, p, ".put-", 0o644, data); err != nil {
		return fmt.Errorf("fetch: writing cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	return nil
}
