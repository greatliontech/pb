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
	ErrEntryInvalid  = errors.New("invalid file set entry")
)

// Kind is what a file set entry is: a regular file, a symbolic link
// carried as its target path, or a submodule entry carried as its
// recorded commit id (REQ-archive-links-carried).
type Kind uint8

const (
	KindFile Kind = iota
	KindLink
	KindSubmodule
)

// FileInfo describes one entry of a module file set: its path relative
// to the module root, its kind, whether a file is executable, its
// content size, and the SHA-256 of its content bytes — a link's target
// path being its content — or, for a submodule entry, the recorded
// commit id, which carries no content.
//
// Size and SHA256 are caller-declared: Manifest enforces the size limit
// against declared sizes and never sees content bytes, so the caller must set
// Size to the exact content length and SHA256 to the hash of those bytes.
// Verification paths derive both from actual content, never from a
// container's headers.
type FileInfo struct {
	Path      string
	Kind      Kind
	Exec      bool
	Size      int64
	SHA256    [32]byte
	Submodule []byte // the recorded commit id, for KindSubmodule
}

// IsModuleFile reports whether an entry of the kind at the path is the
// module file: a regular file named so at the root — a link or a
// submodule entry named like it being no module file
// (REQ-archive-links-carried). The one reading a repository's tree, a
// file set and a container share, so what declares a module is decided
// once.
func IsModuleFile(path string, kind Kind) bool {
	return path == module.ModuleFileName && kind == KindFile
}

// Mode returns the entry's canonical git mode string
// (REQ-archive-mode-normalization).
func (f FileInfo) Mode() string { return modeString(f.Kind, f.Exec) }

// modeString is the canonical git mode of an entry of the kind, the
// execute bit read for a regular file alone
// (REQ-archive-mode-normalization).
func modeString(kind Kind, exec bool) string {
	switch kind {
	case KindLink:
		return "120000"
	case KindSubmodule:
		return "160000"
	}
	if exec {
		return "100755"
	}
	return "100644"
}

// hashHex is the manifest's hash column: the content's SHA-256, or a
// submodule's recorded id.
func (f FileInfo) hashHex() string {
	if f.Kind == KindSubmodule {
		return hex.EncodeToString(f.Submodule)
	}
	return hex.EncodeToString(f.SHA256[:])
}

// Manifest validates the file set and renders its canonical manifest
// (REQ-archive-manifest): the header line, then one "<mode> <hash> <path>"
// line per entry in ascending raw-byte path order — the hash a file's
// or link's content SHA-256, a submodule's recorded id — each line
// LF-terminated. The input slice is not modified.
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
		b.WriteString(f.hashHex())
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
	c := newCollisions(len(files))
	for _, f := range files {
		if err := validatePath(f.Path); err != nil {
			return err
		}
		// Both creation (WriteZip) and verification (VerifyZip) pass
		// through here, so the nested-module invariant has one home
		// (REQ-archive-nested-module). The root's own module file is
		// the declared-module case, not nesting.
		if f.Kind == KindFile && strings.HasSuffix(f.Path, "/"+module.ModuleFileName) {
			return fmt.Errorf("%w: %q (repositories host modules as disjoint subtrees, never nested)", ErrNestedModule, f.Path)
		}
		switch f.Kind {
		case KindFile, KindLink:
			if f.Submodule != nil {
				return fmt.Errorf("%w: %q carries a submodule id", ErrEntryInvalid, f.Path)
			}
		case KindSubmodule:
			// A submodule entry carries the recorded id alone: no content,
			// the id one of git's two object formats.
			if f.Size != 0 || (len(f.Submodule) != 20 && len(f.Submodule) != 32) {
				return fmt.Errorf("%w: submodule %q records an id of %d bytes", ErrEntryInvalid, f.Path, len(f.Submodule))
			}
		default:
			return fmt.Errorf("%w: %q has kind %d", ErrEntryInvalid, f.Path, f.Kind)
		}
		if err := c.add(f.Path); err != nil {
			return err
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
		if err := c.dirClash(f.Path); err != nil {
			return err
		}
	}
	return nil
}

// ValidatePaths holds a set of file paths to the path rules and the
// case-collision rule alone (REQ-archive-path-rules,
// REQ-archive-case-collision): what an export tree asks of every
// module's exported paths taken together, the size limit and the
// nesting rule being a module's own (export.md REQ-export-layout).
// Paths are judged in the order given, so the named offender is
// deterministic; a collision is a *CollisionError naming the paths.
func ValidatePaths(paths []string) error {
	c := newCollisions(len(paths))
	for _, p := range paths {
		if err := validatePath(p); err != nil {
			return err
		}
		if err := c.add(p); err != nil {
			return err
		}
	}
	for _, p := range paths {
		if err := c.dirClash(p); err != nil {
			return err
		}
	}
	return nil
}

// CollisionError is two file paths of one file set that cannot
// coexist (REQ-archive-case-collision): equal under case folding, or
// the first a file fold-equal to a directory the second's path
// implies. It unwraps to ErrPathCollision.
type CollisionError struct {
	Paths  [2]string
	reason string
}

func (e *CollisionError) Error() string { return ErrPathCollision.Error() + ": " + e.reason }
func (e *CollisionError) Unwrap() error { return ErrPathCollision }

// collisions tracks a file set's paths under case folding: a path
// fold-equal to a recorded one is refused at add, a path fold-equal to a
// directory prefix another implies at dirClash — byte-identical means a
// git tree cannot represent the pair (breaking tree-hash recomputation,
// REQ-archive-tree-recompute), fold-equal means a case-insensitive
// filesystem cannot hold it (REQ-archive-case-collision).
type collisions struct {
	byFold   map[string]string     // folded path → one original spelling
	dirFolds map[string]impliedDir // folded implied directory → one original spelling and the file implying it
}

// impliedDir is a directory some file's path implies, and that file.
type impliedDir struct{ dir, file string }

func newCollisions(n int) *collisions {
	return &collisions{byFold: make(map[string]string, n), dirFolds: map[string]impliedDir{}}
}

func (c *collisions) add(p string) error {
	fold := caseFold(p)
	if prev, clash := c.byFold[fold]; clash {
		if prev == p {
			return &CollisionError{Paths: [2]string{prev, p}, reason: fmt.Sprintf("duplicate path %q", p)}
		}
		return &CollisionError{Paths: [2]string{prev, p}, reason: fmt.Sprintf("%q and %q are equal under case folding (REQ-archive-case-collision)", prev, p)}
	}
	c.byFold[fold] = p
	for prefix := parentDir(p); prefix != ""; prefix = parentDir(prefix) {
		c.dirFolds[caseFold(prefix)] = impliedDir{dir: prefix, file: p}
	}
	return nil
}

// dirClash refuses a path fold-equal to a directory another path
// implies; the error names the two files, the directory's implier
// second, so a caller can say whose files cannot coexist.
func (c *collisions) dirClash(p string) error {
	d, isDir := c.dirFolds[caseFold(p)]
	if !isDir {
		return nil
	}
	if d.dir == p {
		return &CollisionError{Paths: [2]string{p, d.file}, reason: fmt.Sprintf("%q is both a file and a directory (of %q)", p, d.file)}
	}
	return &CollisionError{Paths: [2]string{p, d.file}, reason: fmt.Sprintf("file %q and directory %q (of %q) are equal under case folding (REQ-archive-case-collision)", p, d.dir, d.file)}
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
