package fetch

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/go-git/go-billy/v6"
	"github.com/greatliontech/pb/internal/atomicfile"
	"github.com/greatliontech/pb/internal/module"
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

// VersionDir is the directory of a module path's artifacts under the
// cache, and putPrefix the prefix of the temporaries atomic writes
// leave beside them (REQ-dep-cache-layout).
const (
	VersionDir = "@v"
	putPrefix  = ".put-"
)

// entryPath is the REQ-dep-cache-layout location of one artifact:
// <escaped module path>/@v/<escaped version>.<kind>, sharing the proxy
// protocol's escaping and its case-insensitivity rationale.
func entryPath(modPath string, v version.Version, kind string) string {
	return path.Join(proxy.Escape(modPath), VersionDir, proxy.Escape(v.String())+"."+kind)
}

// IsModuleDir reports whether a cache-relative directory path is an
// escaped module path — the parent an `@v` directory of the cache's
// has (REQ-dep-cache-layout). Under any other parent an `@v`
// directory is not the cache's: another tool's cache of the same
// shape under a directory the setting named.
func IsModuleDir(escaped string) bool {
	p, err := proxy.Unescape(escaped)
	if err != nil {
		return false
	}
	return module.ValidatePath(p) == nil
}

// IsArtifactName reports whether a name under an `@v` directory is
// the cache's: an artifact of one of the kinds, or a temporary of an
// atomic write (REQ-dep-cache-layout). Anything else under the cache
// is not the cache's, whatever directory the setting named.
func IsArtifactName(name string) bool {
	if strings.HasPrefix(name, putPrefix) {
		return true
	}
	switch path.Ext(name) {
	case "." + KindInfo, "." + KindMod, "." + KindZip, "." + KindProv:
		return strings.TrimSuffix(name, path.Ext(name)) != ""
	}
	return false
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

// Keep writes a cached artifact where the cache holds none for the
// pair and kind, and leaves an entry present as it is: a first use
// writes through it, so a root's first use never rewrites bytes
// another root's pin names (REQ-dep-cache-transparent); the root
// whose pin the kept entry fails reads past it on the discard-refetch
// path. Present or absent is read once: two first uses of one pair
// racing here are the shared discipline's to order, each writing
// whole.
func (c *Cache) Keep(modPath string, v version.Version, kind string, data []byte) error {
	if _, err := c.FS.Stat(entryPath(modPath, v, kind)); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("fetch: reading cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	return c.Put(modPath, v, kind, data)
}

// Put writes a cached artifact atomically and whole through the shared
// discipline, so no reader ever observes a partial entry
// (REQ-dep-cache-layout). Callers write only verified bytes; a
// replacement of an existing entry happens only on the discard-refetch
// path, where the replacing bytes verified against the same pin the
// discarded ones failed — a first use writes through Keep.
func (c *Cache) Put(modPath string, v version.Version, kind string, data []byte) error {
	p := entryPath(modPath, v, kind)
	if err := c.FS.MkdirAll(path.Dir(p), 0o755); err != nil {
		return fmt.Errorf("fetch: writing cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	if err := atomicfile.Write(c.FS, p, putPrefix, 0o644, data); err != nil {
		return fmt.Errorf("fetch: writing cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	return nil
}
