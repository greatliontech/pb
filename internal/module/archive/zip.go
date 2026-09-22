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
// methods, members of a kind no file set holds, or unreadable member
// content.
var ErrZipInvalid = errors.New("invalid module zip")

// ErrDigestMismatch is wrapped when a zip's recomputed manifest digest does
// not match the expected module digest (REQ-archive-zip-verification).
var ErrDigestMismatch = errors.New("module digest mismatch")

// File pairs an entry's metadata with its bytes for producing the wire
// container: a file's content, a link's target path, a submodule's
// recorded id — one Body for every kind, so an entry cannot carry two
// contents. Sizes and hashes are derived from what is written, so a
// producer cannot declare what it does not write.
type File struct {
	Path string
	Kind Kind
	Exec bool
	Body io.Reader
}

// The Unix file type bits a member's kind rides in the external
// attributes (REQ-archive-zip-mode): a link as git stores one, a
// submodule as git's gitlink mode, which no filesystem type bears. The
// attributes are Unix ones only under a "version made by" naming the
// Unix host (APPNOTE 4.4.2.2): archive/zip's SetMode records it, the
// submodule arm records it itself, and a reader takes no mode from a
// member any other host made.
const (
	unixTypeMask      = 0o170000
	unixTypeRegular   = 0o100000
	unixTypeLink      = 0o120000
	unixTypeSubmodule = 0o160000
	creatorUnix       = 3
)

// memberMode is what a member's recorded attributes say of it: its kind
// and, for a regular file, whether it is executable.
type memberMode struct {
	kind Kind
	exec bool
}

// readMode derives a member's mode from its recorded attributes, the
// one reading every consumer shares (REQ-archive-zip-verification): a
// member no Unix host made records no mode and is a regular file; under
// a Unix host, a regular file where the type bits are unset or regular,
// executable when any execute bit is set, a link, a submodule, and no
// other type.
func readMode(m *zip.File) (memberMode, error) {
	if m.CreatorVersion>>8 != creatorUnix {
		return memberMode{kind: KindFile}, nil
	}
	mode := m.ExternalAttrs >> 16
	switch mode & unixTypeMask {
	case 0, unixTypeRegular:
		return memberMode{kind: KindFile, exec: mode&0o111 != 0}, nil
	case unixTypeLink:
		return memberMode{kind: KindLink}, nil
	case unixTypeSubmodule:
		return memberMode{kind: KindSubmodule}, nil
	}
	return memberMode{}, fmt.Errorf("%w: member %q is neither a regular file, a symbolic link nor a submodule entry", ErrZipInvalid, m.Name)
}

// zipEpoch is the fixed member timestamp: member times are not part of the
// module digest, and a fixed value keeps produced archives reproducible
// byte-for-byte for equal inputs without making bytes contractual.
var zipEpoch = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// WriteZip writes the file set as a module zip (REQ-archive-zip): member
// names are exactly the file set's paths, modes ride the Unix external
// attributes (REQ-archive-zip-mode) — a link's 120000 with its target as
// the member's bytes, a submodule's 160000 with the recorded id as its
// bytes — members are deflate-compressed, entries are written in
// ascending path order with a fixed timestamp. The file set is validated
// (paths, collisions, size limit) and the module digest of the written
// set is returned. On error, bytes already streamed to w are garbage: the
// caller discards them.
func WriteZip(w io.Writer, files []File) (string, error) {
	sorted := make([]File, len(files))
	copy(sorted, files)
	slices.SortFunc(sorted, func(a, b File) int { return strings.Compare(a.Path, b.Path) })

	zw := zip.NewWriter(w)
	infos := make([]FileInfo, 0, len(sorted))
	for _, f := range sorted {
		hdr := &zip.FileHeader{
			Name:     f.Path,
			Method:   zip.Deflate,
			Modified: zipEpoch,
		}
		body := f.Body
		info := FileInfo{Path: f.Path, Kind: f.Kind, Exec: f.Exec}
		switch f.Kind {
		case KindFile:
			mode := fs.FileMode(0o644)
			if f.Exec {
				mode = 0o755
			}
			hdr.SetMode(mode)
		case KindLink:
			hdr.SetMode(fs.ModeSymlink | 0o777)
		case KindSubmodule:
			// The body is the recorded id, one of git's two object
			// formats, and the member's bytes; the type bits alone ride
			// the attributes, under the Unix host that makes them Unix
			// attributes (SetMode knows no gitlink).
			id, err := io.ReadAll(io.LimitReader(body, 33))
			if err != nil {
				return "", fmt.Errorf("reading submodule id of %q: %w", f.Path, err)
			}
			if len(id) != 20 && len(id) != 32 {
				return "", fmt.Errorf("%w: submodule %q records an id of %d bytes", ErrEntryInvalid, f.Path, len(id))
			}
			hdr.ExternalAttrs = unixTypeSubmodule << 16
			hdr.CreatorVersion = creatorUnix << 8
			body, info.Submodule = bytes.NewReader(id), id
		default:
			return "", fmt.Errorf("%w: %q has kind %d", ErrEntryInvalid, f.Path, f.Kind)
		}
		mw, err := zw.CreateHeader(hdr)
		if err != nil {
			return "", fmt.Errorf("writing zip member %q: %w", f.Path, err)
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(mw, h), body)
		if err != nil {
			return "", fmt.Errorf("writing zip member %q: %w", f.Path, err)
		}
		if f.Kind != KindSubmodule {
			info.Size = n
			copy(info.SHA256[:], h.Sum(nil))
		}
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
// recorded attributes (a link, a submodule, else any execute bit means
// executable) — and accepts the zip only when the manifest's digest
// equals expected (REQ-archive-zip-verification). Directory entries are
// ignored; duplicate member names, encrypted members, members of another
// type, and compression methods other than store or deflate are rejected
// (REQ-archive-zip). On success the verified file set is returned in
// manifest order.
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
	err := walkZip(r, size, func(m *zip.File, mode memberMode) error {
		info, err := hashMember(m, mode)
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

// walkZip iterates the zip's members under the wire-container
// discipline every consumer shares (REQ-archive-zip): directory entries
// are skipped; duplicate member names, encrypted members, members of a
// type no file set holds, and compression methods other than store or
// deflate are rejected. One walker keeps the accepted member surface
// from drifting between the digest, tree-hash, and member-read paths;
// each member's mode is handed on with it.
func walkZip(r io.ReaderAt, size int64, fn func(*zip.File, memberMode) error) error {
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
		mode, err := readMode(m)
		if err != nil {
			return err
		}
		if m.Method != zip.Store && m.Method != zip.Deflate {
			return fmt.Errorf("%w: member %q uses unsupported compression method %d", ErrZipInvalid, m.Name, m.Method)
		}
		if err := fn(m, mode); err != nil {
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
// each file's or link's content is blob-hashed, a submodule's recorded id
// is the entry's hash, and the tree assembled per
// REQ-archive-tree-recompute. The zip's digest acceptance is separate and
// prior (VerifyZip); this recomputation trusts nothing about the container
// beyond the shared member discipline.
func ZipTreeHash(f ObjectFormat, r io.ReaderAt, size int64) ([]byte, error) {
	var entries []TreeEntry
	err := walkZip(r, size, func(m *zip.File, mode memberMode) error {
		b, err := readMember(m)
		if err != nil {
			return err
		}
		hash := b
		if mode.kind != KindSubmodule {
			if hash, err = BlobHash(f, int64(len(b)), bytes.NewReader(b)); err != nil {
				return err
			}
		}
		entries = append(entries, TreeEntry{Path: m.Name, Kind: mode.kind, Exec: mode.exec, Hash: hash})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return TreeHash(f, entries)
}

// ZipFiles returns every regular file's content keyed by path — the
// files of an already-verified container, for consumers that walk
// module content (import analysis). A link or a submodule entry the
// container carries is no file of the module: a file reachable only
// through a link is never read (REQ-archive-links-carried). The total
// is bounded by MaxTotalSize through the shared member discipline.
func ZipFiles(r io.ReaderAt, size int64) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := walkZip(r, size, func(m *zip.File, mode memberMode) error {
		if mode.kind != KindFile {
			return nil
		}
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

// ZipFile returns the content bytes of the named regular file, reporting
// whether the zip has one — a link or a submodule entry at the name is
// no file, so a link named like the module file is no module file. The
// name is matched exactly against member names — the file-set path rules
// make the module file's spelling unique.
func ZipFile(r io.ReaderAt, size int64, path string) ([]byte, bool, error) {
	var content []byte
	found := false
	err := walkZip(r, size, func(m *zip.File, mode memberMode) error {
		if m.Name != path || mode.kind != KindFile {
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

func hashMember(m *zip.File, mode memberMode) (FileInfo, error) {
	if mode.kind == KindSubmodule {
		// The member's bytes are the recorded id, one of git's two
		// object formats; the manifest carries the id, no content.
		id, err := readMember(m)
		if err != nil {
			return FileInfo{}, err
		}
		if len(id) != 20 && len(id) != 32 {
			return FileInfo{}, fmt.Errorf("%w: submodule member %q holds %d bytes, not a commit id", ErrZipInvalid, m.Name, len(id))
		}
		return FileInfo{Path: m.Name, Kind: KindSubmodule, Submodule: id}, nil
	}
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
		Kind: mode.kind,
		Exec: mode.exec,
		Size: n,
	}
	copy(info.SHA256[:], h.Sum(nil))
	return info, nil
}

// ExtractZip verifies the zip against expected and materializes the file
// set's regular files under dir. A mid-extraction error leaves a partial
// tree under dir: callers wanting atomicity extract into a fresh directory
// and rename. No written file is executable and none is world-writable,
// and no link nor submodule entry is written at all
// (REQ-archive-no-exec-materialization): the execute mode, a link's
// target and a submodule's id exist in the manifest solely for digest and
// git-tree fidelity, module content is never executed, and a link may
// point anywhere.
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
		if !ok || info.Kind != KindFile {
			continue // a directory entry, a link or a submodule entry
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
