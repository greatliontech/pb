// Package archive implements the canonical module archive contract: file-set
// validation, canonical manifest rendering, and the module digest.
//
// The digest is manifest-based rather than a hash of archive bytes so that it
// is a pure function of the file set (path, mode, content) and independent of
// any container encoding: any wire form carrying exactly the same files
// verifies against the same digest (REQ-archive-digest-purity).
package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/greatliontech/pb/internal/module"
)

// ManifestHeader is the first line of every canonical manifest; it is part of
// the digested bytes and provides domain separation for the format version
// (REQ-archive-manifest).
const ManifestHeader = "pb-module-manifest/v1"

// DigestPrefix prefixes the rendered module digest (REQ-archive-digest).
const DigestPrefix = "pb1:"

// MaxTotalSize is the file-set content size limit in bytes; a file set whose
// total content size exceeds it is rejected (REQ-archive-size-limit).
const MaxTotalSize int64 = 500 << 20

// File-set validation failure classes. Every validation error wraps exactly
// one of these.
var (
	ErrPathInvalid   = errors.New("invalid path")
	ErrPathCollision = errors.New("path collision")
	ErrTooLarge      = errors.New("file set exceeds size limit")
	ErrNestedModule  = errors.New("module file below the module root")
)

// FileInfo describes one file of a module file set: its path relative to the
// module root, whether it is executable, its content size, and the SHA-256 of
// its content bytes.
//
// Size and SHA256 are caller-declared: Manifest enforces the size limit
// against declared sizes and never sees content bytes, so the caller must set
// Size to the exact content length and SHA256 to the hash of those bytes.
// Verification paths derive both from actual content, never from a
// container's headers.
type FileInfo struct {
	Path   string
	Exec   bool
	Size   int64
	SHA256 [32]byte
}

// Mode returns the file's canonical git mode string
// (REQ-archive-mode-normalization).
func (f FileInfo) Mode() string {
	if f.Exec {
		return "100755"
	}
	return "100644"
}

// Manifest validates the file set and renders its canonical manifest
// (REQ-archive-manifest): the header line, then one "<mode> <sha256> <path>"
// line per file in ascending raw-byte path order, each line LF-terminated.
// The input slice is not modified.
func Manifest(files []FileInfo) ([]byte, error) {
	if err := ValidateFileSet(files); err != nil {
		return nil, err
	}
	sorted := make([]FileInfo, len(files))
	copy(sorted, files)
	slices.SortFunc(sorted, func(a, b FileInfo) int { return strings.Compare(a.Path, b.Path) })

	var b strings.Builder
	b.WriteString(ManifestHeader)
	b.WriteByte('\n')
	for _, f := range sorted {
		b.WriteString(f.Mode())
		b.WriteByte(' ')
		b.WriteString(hex.EncodeToString(f.SHA256[:]))
		b.WriteByte(' ')
		b.WriteString(f.Path)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// Digest returns the module digest of a canonical manifest: "pb1:" followed
// by the lowercase hex SHA-256 of the manifest bytes (REQ-archive-digest).
func Digest(manifest []byte) string {
	sum := sha256.Sum256(manifest)
	return DigestPrefix + hex.EncodeToString(sum[:])
}

// ValidateFileSet checks the whole file-set discipline — path rules,
// collisions, nested module file, size limit over declared sizes —
// without touching content. Manifest applies it on every creation and
// verification path; sources that know their file set before reading
// content (the direct source walks blob metadata) apply it first, so
// an invalid version fails before serving or materializing anything.
func ValidateFileSet(files []FileInfo) error {
	var total int64
	byFold := make(map[string]string, len(files))
	// dirFolds maps the case-folded form of every implied directory prefix
	// to one original spelling. A file whose folded path hits it cannot
	// coexist with that directory: byte-identical means a git tree cannot
	// represent the pair (breaking tree-hash recomputation,
	// REQ-archive-tree-recompute), fold-equal means a case-insensitive
	// filesystem cannot extract it (REQ-archive-case-collision).
	dirFolds := make(map[string]string)

	for _, f := range files {
		if err := validatePath(f.Path); err != nil {
			return err
		}
		// Both creation (WriteZip) and verification (VerifyZip) pass
		// through here, so the nested-module invariant has one home
		// (REQ-archive-nested-module). The root's own module file is
		// the declared-module case, not nesting.
		if strings.HasSuffix(f.Path, "/"+module.ModuleFileName) {
			return fmt.Errorf("%w: %q (repositories host modules as disjoint subtrees, never nested)", ErrNestedModule, f.Path)
		}
		fold := caseFold(f.Path)
		if prev, clash := byFold[fold]; clash {
			if prev == f.Path {
				return fmt.Errorf("%w: duplicate path %q", ErrPathCollision, f.Path)
			}
			return fmt.Errorf("%w: %q and %q are equal under case folding (REQ-archive-case-collision)", ErrPathCollision, prev, f.Path)
		}
		byFold[fold] = f.Path

		for prefix := parentDir(f.Path); prefix != ""; prefix = parentDir(prefix) {
			dirFolds[caseFold(prefix)] = prefix
		}

		if f.Size < 0 {
			return fmt.Errorf("%w: negative size for %q", ErrPathInvalid, f.Path)
		}
		// Bound before accumulating: total never exceeds MaxTotalSize, so the
		// subtraction cannot underflow and the sum cannot wrap int64.
		if f.Size > MaxTotalSize-total {
			return fmt.Errorf("%w: total content size exceeds %d bytes", ErrTooLarge, MaxTotalSize)
		}
		total += f.Size
	}
	// Second pass in input order, so the named offender is deterministic.
	for _, f := range files {
		if dir, isDir := dirFolds[caseFold(f.Path)]; isDir {
			if dir == f.Path {
				return fmt.Errorf("%w: %q is both a file and a directory", ErrPathCollision, f.Path)
			}
			return fmt.Errorf("%w: file %q and directory %q are equal under case folding (REQ-archive-case-collision)", ErrPathCollision, f.Path, dir)
		}
	}
	return nil
}

func parentDir(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}

// windowsInvalid are the characters invalid in Windows file names ('\' is a
// separator there); paths carrying them cannot be extracted portably
// (REQ-archive-path-rules).
const windowsInvalid = `:<>"|?*\`

// validatePath enforces REQ-archive-path-rules: relative, '/'-separated,
// valid UTF-8, no empty/'.'/'..' segments, no leading or trailing separator,
// no control characters (a '\n' would break manifest line framing, a NUL git
// tree encoding), no Windows-invalid characters, no segment ending in a dot
// or a space (Windows strips them on extraction, colliding distinct names),
// no Windows reserved device name segments.
func validatePath(p string) error {
	if !utf8.ValidString(p) {
		return fmt.Errorf("%w: %q is not valid UTF-8", ErrPathInvalid, p)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %q contains a control character", ErrPathInvalid, p)
		}
	}
	if strings.ContainsAny(p, windowsInvalid) {
		return fmt.Errorf("%w: %q contains a character invalid in Windows file names (one of %s)", ErrPathInvalid, p, windowsInvalid)
	}
	for seg := range strings.SplitSeq(p, "/") {
		switch seg {
		case "":
			return fmt.Errorf("%w: %q has an empty segment", ErrPathInvalid, p)
		case ".", "..":
			return fmt.Errorf("%w: %q has a %q segment", ErrPathInvalid, p, seg)
		}
		if strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") {
			return fmt.Errorf("%w: %q has a segment ending in a dot or a space", ErrPathInvalid, p)
		}
		if isReservedDeviceName(seg) {
			return fmt.Errorf("%w: %q has a Windows reserved device name segment", ErrPathInvalid, p)
		}
	}
	return nil
}

func isReservedDeviceName(seg string) bool {
	base := seg
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	r := []rune(strings.ToUpper(base))
	switch len(r) {
	case 3:
		switch string(r) {
		case "CON", "PRN", "AUX", "NUL":
			return true
		}
	case 4:
		if p := string(r[:3]); p == "COM" || p == "LPT" {
			switch r[3] {
			case '1', '2', '3', '4', '5', '6', '7', '8', '9', '¹', '²', '³':
				return true
			}
		}
	}
	return false
}

// caseFold maps a path to its Unicode-simple-case-fold canonical form: two
// paths with equal folds collide (REQ-archive-case-collision).
func caseFold(p string) string {
	return strings.Map(foldRune, p)
}

func foldRune(r rune) rune {
	m := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		if f < m {
			m = f
		}
	}
	return m
}
