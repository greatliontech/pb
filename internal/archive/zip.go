package archive

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ErrZipInvalid is wrapped by every wire-container rejection that is not a
// digest mismatch: duplicate members, encryption, unsupported compression
// methods, non-regular members, or unreadable member content.
var ErrZipInvalid = errors.New("invalid module zip")

// ErrDigestMismatch is wrapped when a zip's recomputed manifest digest does
// not match the expected module digest (REQ-archive-zip-verification).
var ErrDigestMismatch = errors.New("module digest mismatch")

// File pairs a file's metadata with its content for producing the wire
// container. Size and SHA256 in FileInfo are ignored by WriteZip — they are
// derived from Body — so a producer cannot declare what it does not write.
type File struct {
	Path string
	Exec bool
	Body io.Reader
}

// zipEpoch is the fixed member timestamp: member times are not part of the
// module digest, and a fixed value keeps produced archives reproducible
// byte-for-byte for equal inputs without making bytes contractual.
var zipEpoch = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// WriteZip writes the file set as a module zip (REQ-archive-zip): member
// names are exactly the file paths, modes ride the Unix external attributes
// (REQ-archive-zip-mode), members are deflate-compressed, entries are
// written in ascending path order with a fixed timestamp. The file set is
// validated (paths, collisions, size limit) and the module digest of the
// written set is returned. On error, bytes already streamed to w are
// garbage: the caller discards them.
func WriteZip(w io.Writer, files []File) (string, error) {
	sorted := make([]File, len(files))
	copy(sorted, files)
	slices.SortFunc(sorted, func(a, b File) int { return strings.Compare(a.Path, b.Path) })

	zw := zip.NewWriter(w)
	infos := make([]FileInfo, 0, len(sorted))
	for _, f := range sorted {
		mode := fs.FileMode(0o644)
		if f.Exec {
			mode = 0o755
		}
		hdr := &zip.FileHeader{
			Name:     f.Path,
			Method:   zip.Deflate,
			Modified: zipEpoch,
		}
		hdr.SetMode(mode)
		mw, err := zw.CreateHeader(hdr)
		if err != nil {
			return "", fmt.Errorf("writing zip member %q: %w", f.Path, err)
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(mw, h), f.Body)
		if err != nil {
			return "", fmt.Errorf("writing zip member %q: %w", f.Path, err)
		}
		info := FileInfo{Path: f.Path, Exec: f.Exec, Size: n}
		copy(info.SHA256[:], h.Sum(nil))
		infos = append(infos, info)
	}
	m, err := Manifest(infos)
	if err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	return Digest(m), nil
}

// VerifyZip recomputes the canonical manifest from the zip's members —
// hashing actual content bytes, deriving each mode from the member's
// recorded attributes (any execute bit means executable) — and accepts the
// zip only when the manifest's digest equals expected
// (REQ-archive-zip-verification). Directory entries are ignored; duplicate
// member names, encrypted members, and compression methods other than store
// or deflate are rejected (REQ-archive-zip). On success the verified file
// set is returned in manifest order.
func VerifyZip(r io.ReaderAt, size int64, expected string) ([]FileInfo, error) {
	d, infos, err := DigestZip(r, size)
	if err != nil {
		return nil, err
	}
	if d != expected {
		return nil, fmt.Errorf("%w: computed %s, expected %s", ErrDigestMismatch, d, expected)
	}
	return infos, nil
}

// DigestZip recomputes the canonical manifest from the zip's members under
// the same discipline as VerifyZip and returns the resulting module digest
// with the file set in manifest order. It computes and never compares:
// first-use pinning (REQ-lock-first-use) records what was fetched, while
// VerifyZip enforces an expectation over the same recomputation.
func DigestZip(r io.ReaderAt, size int64) (string, []FileInfo, error) {
	var infos []FileInfo
	err := walkZip(r, size, func(m *zip.File) error {
		info, err := hashMember(m)
		if err != nil {
			return err
		}
		infos = append(infos, info)
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	manifest, err := Manifest(infos)
	if err != nil {
		return "", nil, err
	}
	slices.SortFunc(infos, func(a, b FileInfo) int { return strings.Compare(a.Path, b.Path) })
	return Digest(manifest), infos, nil
}

// walkZip iterates the zip's file members under the wire-container
// discipline every consumer shares (REQ-archive-zip): directory entries are
// skipped; duplicate member names, encrypted members, non-regular members,
// and compression methods other than store or deflate are rejected. One
// walker keeps the accepted member surface from drifting between the
// digest, tree-hash, and member-read paths.
func walkZip(r io.ReaderAt, size int64, fn func(*zip.File) error) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrZipInvalid, err)
	}
	seen := make(map[string]struct{}, len(zr.File))
	for _, m := range zr.File {
		if strings.HasSuffix(m.Name, "/") {
			continue // directory entry
		}
		if _, dup := seen[m.Name]; dup {
			return fmt.Errorf("%w: duplicate member %q", ErrZipInvalid, m.Name)
		}
		seen[m.Name] = struct{}{}
		if m.Flags&0x1 != 0 { // general-purpose bit 0: encrypted
			return fmt.Errorf("%w: member %q is encrypted", ErrZipInvalid, m.Name)
		}
		if m.Mode()&fs.ModeType != 0 {
			// A symlink (or any non-regular) member could otherwise verify
			// as a regular file with the link target as content
			// (REQ-archive-forbidden-entries: verification MUST fail).
			return fmt.Errorf("%w: member %q is not a regular file", ErrZipInvalid, m.Name)
		}
		if m.Method != zip.Store && m.Method != zip.Deflate {
			return fmt.Errorf("%w: member %q uses unsupported compression method %d", ErrZipInvalid, m.Name, m.Method)
		}
		if err := fn(m); err != nil {
			return err
		}
	}
	return nil
}

// readMember decompresses one member's content whole, reading at most one
// byte past MaxTotalSize: content beyond it can never belong to a valid
// file set, so the bound caps decompression work without trusting the
// declared size (a zip bomb declares small and inflates large).
func readMember(m *zip.File) ([]byte, error) {
	rc, err := m.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: opening member %q: %v", ErrZipInvalid, m.Name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, MaxTotalSize+1))
	if err != nil {
		return nil, fmt.Errorf("%w: reading member %q: %v", ErrZipInvalid, m.Name, err)
	}
	if int64(len(b)) > MaxTotalSize {
		return nil, fmt.Errorf("%w: member %q exceeds the %d-byte file-set limit", ErrZipInvalid, m.Name, MaxTotalSize)
	}
	return b, nil
}

// ZipTreeHash recomputes the module root's git tree hash from the zip's
// members in the given object format — the archive side of
// REQ-archive-tree-binding for a consumer holding only the wire container:
// each member's content is blob-hashed and the tree assembled per
// REQ-archive-tree-recompute. The zip's digest acceptance is separate and
// prior (VerifyZip); this recomputation trusts nothing about the container
// beyond the shared member discipline.
func ZipTreeHash(f ObjectFormat, r io.ReaderAt, size int64) ([]byte, error) {
	var entries []TreeEntry
	err := walkZip(r, size, func(m *zip.File) error {
		b, err := readMember(m)
		if err != nil {
			return err
		}
		blob, err := BlobHash(f, int64(len(b)), bytes.NewReader(b))
		if err != nil {
			return err
		}
		entries = append(entries, TreeEntry{Path: m.Name, Exec: m.Mode()&0o111 != 0, Blob: blob})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return TreeHash(f, entries)
}

// ZipFiles returns every member's content keyed by path — the whole
// file set of an already-verified container, for consumers that walk
// module content (import analysis). The total is bounded by
// MaxTotalSize through the shared member discipline.
func ZipFiles(r io.ReaderAt, size int64) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := walkZip(r, size, func(m *zip.File) error {
		b, err := readMember(m)
		if err != nil {
			return err
		}
		files[m.Name] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// ZipFile returns the content bytes of the named member, reporting whether
// the zip has it. The name is matched exactly against member names — the
// file-set path rules make the module file's spelling unique.
func ZipFile(r io.ReaderAt, size int64, path string) ([]byte, bool, error) {
	var content []byte
	found := false
	err := walkZip(r, size, func(m *zip.File) error {
		if m.Name != path {
			return nil
		}
		b, err := readMember(m)
		if err != nil {
			return err
		}
		content, found = b, true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return content, found, nil
}

func hashMember(m *zip.File) (FileInfo, error) {
	rc, err := m.Open()
	if err != nil {
		return FileInfo{}, fmt.Errorf("%w: opening member %q: %v", ErrZipInvalid, m.Name, err)
	}
	defer rc.Close()
	h := sha256.New()
	// Read at most one byte past the size limit: content beyond it can only
	// push the file set over MaxTotalSize, which Manifest rejects — this
	// bounds decompression work without trusting the declared size
	// (a zip bomb declares small and inflates large).
	n, err := io.Copy(h, io.LimitReader(rc, MaxTotalSize+1))
	if err != nil {
		return FileInfo{}, fmt.Errorf("%w: reading member %q: %v", ErrZipInvalid, m.Name, err)
	}
	// Declared sizes carry no authority and are not cross-checked: content
	// is hashed as read, Size is the actual byte count, and the digest is
	// the only acceptance criterion (REQ-archive-zip-verification).
	info := FileInfo{
		Path: m.Name,
		Exec: m.Mode()&0o111 != 0,
		Size: n,
	}
	copy(info.SHA256[:], h.Sum(nil))
	return info, nil
}

// ExtractZip verifies the zip against expected and materializes the file
// set under dir. A mid-extraction error leaves a partial tree under dir:
// callers wanting atomicity extract into a fresh directory and rename.
// No written file is executable and none is world-writable
// (REQ-archive-no-exec-materialization): the execute mode exists in the
// manifest solely for digest and git-tree fidelity, and module content is
// never executed.
func ExtractZip(dir string, r io.ReaderAt, size int64, expected string) error {
	infos, err := VerifyZip(r, size, expected)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrZipInvalid, err)
	}
	byPath := make(map[string]FileInfo, len(infos))
	for _, info := range infos {
		byPath[info.Path] = info
	}
	for _, m := range zr.File {
		info, ok := byPath[m.Name]
		if !ok {
			continue // directory entry
		}
		dst := filepath.Join(dir, filepath.FromSlash(info.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := writeMember(dst, m, info); err != nil {
			return err
		}
	}
	return nil
}

// writeMember materializes one verified member, hashing while writing and
// refusing to keep bytes that differ from the verified FileInfo — extraction
// re-reads the container, and that second read carries no more authority
// than the first (REQ-archive-zip-verification).
func writeMember(dst string, m *zip.File, info FileInfo) error {
	rc, err := m.Open()
	if err != nil {
		return fmt.Errorf("%w: opening member %q: %v", ErrZipInvalid, info.Path, err)
	}
	defer rc.Close()
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(rc, info.Size+1))
	if err != nil {
		f.Close()
		return fmt.Errorf("extracting %q: %w", info.Path, err)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	if n != info.Size || sum != info.SHA256 {
		f.Close()
		return fmt.Errorf("%w: member %q changed between verification and extraction", ErrZipInvalid, info.Path)
	}
	return f.Close()
}
