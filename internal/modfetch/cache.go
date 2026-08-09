package modfetch

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	"github.com/go-git/go-billy/v6"
	"github.com/greatliontech/pb/internal/proxy"
	"github.com/greatliontech/pb/internal/version"
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
		return nil, false, fmt.Errorf("modfetch: reading cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, false, fmt.Errorf("modfetch: reading cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	return b, true, nil
}

// Put writes a cached artifact atomically and whole: a temporary file in
// the entry's directory, renamed into place, so no reader ever observes
// a partial entry (REQ-dep-cache-layout). Callers write only verified
// bytes; a replacement of an existing entry happens only on the
// discard-refetch path, where the replacing bytes verified against the
// same pin the discarded ones failed.
func (c *Cache) Put(modPath string, v version.Version, kind string, data []byte) error {
	p := entryPath(modPath, v, kind)
	dir := path.Dir(p)
	if err := c.FS.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("modfetch: writing cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	tmp, err := c.FS.TempFile(dir, ".put-")
	if err != nil {
		return fmt.Errorf("modfetch: writing cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		c.FS.Remove(tmpName)
		return fmt.Errorf("modfetch: writing cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	if err := tmp.Close(); err != nil {
		c.FS.Remove(tmpName)
		return fmt.Errorf("modfetch: writing cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	if err := c.FS.Rename(tmpName, p); err != nil {
		c.FS.Remove(tmpName)
		return fmt.Errorf("modfetch: writing cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	return nil
}
