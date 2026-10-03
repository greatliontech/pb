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
	"github.com/greatliontech/pb/internal/module/archive"
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
// <escaped module path>/@v/<escaped version>.<digest>.<kind> for the
// archive, the module file and the evidence — the digest the
// archive's, spelled with its `:` as `-` — and <escaped
// version>.info for the info object, which no digest fixes; the
// escaping the proxy protocol's, with its case-insensitivity
// rationale. Two roots pinning one pair at two digests hold two
// entries, each the content its name fixes.
func entryPath(modPath string, v version.Version, kind, digest string) (string, error) {
	name := proxy.Escape(v.String())
	if kind != KindInfo {
		if digest == "" {
			return "", fmt.Errorf("fetch: a %s entry for %s@%s is named by its digest, none given", kind, modPath, v)
		}
		name += "." + DigestName(digest)
	}
	return path.Join(proxy.Escape(modPath), VersionDir, name+"."+kind), nil
}

// digestNameSep spells the module digest's `:` in a file name, which
// no file name on every platform admits.
const digestNameSep = "-"

// DigestName spells a digest as a file name's part: `pb1:<hex>` as
// `pb1-<hex>`.
func DigestName(digest string) string { return strings.Replace(digest, ":", digestNameSep, 1) }

// SplitDigest reads a digest name off the end of a name spelled
// `<base>.<digest name>`: the base and the digest where the name ends
// in one as DigestName spells a module digest — DigestName inverted,
// the tail after the last dot read back to a digest and held to the
// digest's own form — else the name whole and no digest.
func SplitDigest(name string) (base, digest string, ok bool) {
	i := strings.LastIndex(name, ".")
	if i <= 0 {
		return name, "", false
	}
	digest = strings.Replace(name[i+1:], digestNameSep, ":", 1)
	if !strings.HasPrefix(digest, archive.DigestPrefix) || !isHex(strings.TrimPrefix(digest, archive.DigestPrefix), 64) || DigestName(digest) != name[i+1:] {
		return name, "", false
	}
	return name[:i], digest, true
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
// the cache's: an artifact of one of the kinds — `<version>.info`,
// `<version>.<digest>.<kind>` for the others, and `<version>.<kind>`
// as the layout before the digest spelled every kind, read by nothing
// and removed by the emptying — or a temporary of an atomic write
// (REQ-dep-cache-layout). Anything else under the cache is not the
// cache's, whatever directory the setting named. A stem with no
// well-formed digest name at its end is the earlier layout's, a
// version spelled however it was.
func IsArtifactName(name string) bool {
	if strings.HasPrefix(name, putPrefix) {
		return true
	}
	stem := strings.TrimSuffix(name, path.Ext(name))
	switch path.Ext(name) {
	case "." + KindInfo, "." + KindMod, "." + KindZip, "." + KindProv:
		return stem != ""
	}
	return false
}

// IsDigestHex reports whether s is a module digest's hex alone:
// sixty-four lowercase hex digits, as the well-known set's copy in
// the dependency source store is named (lsp.md
// REQ-lsp-dependency-files).
func IsDigestHex(s string) bool { return isHex(s, 64) }

// isHex reports whether s is exactly n lowercase hex digits.
func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Get reads a cached artifact — the pair's at the digest, the info
// object by the pair alone — reporting absence without error; a
// digest-named kind asked for with no digest, a pin recording none,
// names no entry and is absent.
func (c *Cache) Get(modPath string, v version.Version, kind, digest string) ([]byte, bool, error) {
	if kind != KindInfo && digest == "" {
		return nil, false, nil
	}
	p, err := entryPath(modPath, v, kind, digest)
	if err != nil {
		return nil, false, err
	}
	f, err := c.FS.Open(p)
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
// (REQ-dep-cache-layout). Callers write only verified bytes under the
// digest that fixes them, so a write never rewrites bytes another
// root's pin names: an entry present under the name holds the same
// content; a replacement of one happens only on the discard-refetch
// path, where the replacing bytes verified against the same digest
// the discarded ones failed.
func (c *Cache) Put(modPath string, v version.Version, kind, digest string, data []byte) error {
	p, err := entryPath(modPath, v, kind, digest)
	if err != nil {
		return err
	}
	if err := c.FS.MkdirAll(path.Dir(p), 0o755); err != nil {
		return fmt.Errorf("fetch: writing cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	if err := atomicfile.Write(c.FS, p, putPrefix, 0o644, data); err != nil {
		return fmt.Errorf("fetch: writing cache entry for %s@%s.%s: %w", modPath, v, kind, err)
	}
	return nil
}
