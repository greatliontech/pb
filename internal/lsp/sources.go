package lsp

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/greatliontech/lsp/uri"

	"github.com/greatliontech/pb/internal/atomicfile"
	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/proto/modfiles"
	"github.com/greatliontech/pb/internal/source/proxy"
)

// moduleScheme is the scheme a dependency's file is addressed under
// where the client offers workspace/textDocumentContent
// (REQ-lsp-dependency-files): `pb-module://<module path>@<version>/<file
// path>`, a well-known file `pb-module://well-known/<file path>`.
const moduleScheme = "pb-module"

// wellKnownAuthority names the toolchain's well-known set under the
// scheme and in the source store.
const wellKnownAuthority = "well-known"

// moduleURI is a dependency file's address: under the module scheme
// where the client offers the content request, else a file URI into
// the dependency source store. The scheme's URI is spelled in the
// canonical form the binding decodes to — the version's `@` percent
// encoded in the path — so the address compares equal to what a
// client sends back.
func (s *Server) moduleURI(modPath, version, file string) uri.URI {
	if s.content {
		return uri.MustParse(fmt.Sprintf("%s://%s@%s/%s", moduleScheme, modPath, version, file))
	}
	return uri.File(filepath.Join(s.copyDir(modPath, version), filepath.FromSlash(file)))
}

// wellKnownURI is a well-known file's address, as moduleURI.
func (s *Server) wellKnownURI(file string) uri.URI {
	if s.content {
		return uri.MustParse(fmt.Sprintf("%s://%s/%s", moduleScheme, wellKnownAuthority, file))
	}
	return uri.File(filepath.Join(s.wellKnown.dir, filepath.FromSlash(file)))
}

// copyDir is a module's copy directory in the source store:
// `<escaped module path>@<escaped version>`, the path's segments
// directories as the module cache lays them.
func (s *Server) copyDir(modPath, version string) string {
	return filepath.Join(s.deps.Sources, filepath.FromSlash(proxy.Escape(modPath))+"@"+proxy.Escape(version))
}

// moduleContent is the bytes the build read for a module-scheme URI:
// the module's file, from the last committed judgement's table or
// the navigation index's — an address navigation handed out is
// served while the index stands, a build loaded or not — or the
// well-known set's; an error naming a path no table provides.
func (s *Server) moduleContent(u uri.URI) ([]byte, error) {
	if u.Scheme() != moduleScheme {
		return nil, fmt.Errorf("%s: not a %s URI", u, moduleScheme)
	}
	locator := u.Authority() + u.Path()
	if rest, ok := strings.CutPrefix(locator, wellKnownAuthority+"/"); ok {
		b, ok := s.wellKnown.files[rest]
		if !ok {
			return nil, fmt.Errorf("%s: no well-known import is named %s", u, rest)
		}
		return b, nil
	}
	at := strings.Index(locator, "@")
	if at < 0 {
		return nil, fmt.Errorf("%s: a module locator carries @<version>", u)
	}
	modPath, rest := locator[:at], locator[at+1:]
	version, file, ok := strings.Cut(rest, "/")
	if !ok || file == "" {
		return nil, fmt.Errorf("%s: a module locator names a file after the version", u)
	}
	s.mu.Lock()
	tables := []*buildFiles{s.files}
	if s.index != nil {
		tables = append(tables, s.index.build)
	}
	s.mu.Unlock()
	loaded := false
	for _, t := range tables {
		if t == nil {
			continue
		}
		loaded = true
		if bf, ok := t.byPath[file]; ok && bf.origin.modPath == modPath && bf.origin.version == version {
			return bf.text, nil
		}
	}
	if !loaded {
		return nil, fmt.Errorf("%s: no build is loaded", u)
	}
	return nil, fmt.Errorf("%s: no module of the build provides %s@%s/%s", u, modPath, version, file)
}

// copySources fills the dependency source store for the build's
// modules read from archives and for the toolchain's well-known
// set, where the client addresses dependency files by file URI
// (REQ-lsp-dependency-files): a copy present is compared with the
// bytes the build read and replaced where it differs, each file
// written whole and atomically.
func (s *Server) copySources(mods []modfiles.Module) error {
	if s.deps.Sources == "" {
		return errors.New("no dependency source store is configured")
	}
	for _, m := range mods {
		if m.FromTree() {
			continue
		}
		if err := copyFiles(s.copyDir(m.Path, m.Version), m.Files); err != nil {
			return fmt.Errorf("%s@%s: %w", m.Path, m.Version, err)
		}
	}
	if err := copyFiles(s.wellKnown.dir, s.wellKnown.files); err != nil {
		return fmt.Errorf("well-known imports: %w", err)
	}
	return nil
}

// copyFiles puts files under dir, each compared first and written
// atomically where absent or differing.
func copyFiles(dir string, files map[string][]byte) error {
	fsys := osfs.New(dir)
	for p, want := range files {
		have, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err == nil && bytes.Equal(have, want) {
			continue
		}
		if err := fsys.MkdirAll(filepath.Dir(filepath.FromSlash(p)), 0o755); err != nil {
			return err
		}
		if err := atomicfile.Write(fsys, filepath.FromSlash(p), dep.SourcesTempPrefix, 0o644, want); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

// wellKnownCopy is the toolchain's well-known set with its digest:
// `well-known@<digest>` in the source store, the digest the hex
// SHA-256 over the set's paths and bytes in path order, each field
// preceded by its length as eight big-endian bytes.
type wellKnownCopy struct {
	files map[string][]byte
	dir   string
}

// wellKnownSet enumerates and digests the toolchain's set, its copy
// directory under the source store.
func wellKnownSet(sources string) (*wellKnownCopy, error) {
	files, err := modfiles.WellKnownSet()
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	var n [8]byte
	for _, p := range paths {
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write([]byte(p))
		binary.BigEndian.PutUint64(n[:], uint64(len(files[p])))
		h.Write(n[:])
		h.Write(files[p])
	}
	return &wellKnownCopy{files: files, dir: filepath.Join(sources, wellKnownAuthority+"@"+hex.EncodeToString(h.Sum(nil)))}, nil
}

// relPath is p relative to dir, or an error where p does not lie
// under dir.
func relPath(dir, p string) (string, error) {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is not under %s", p, dir)
	}
	return rel, nil
}
