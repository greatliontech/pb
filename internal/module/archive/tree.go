package archive

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // git's object format; see the note on ObjectFormat.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"slices"
	"strconv"
	"strings"
)

// Git tree recomputation (REQ-archive-tree-recompute): the module root's git
// tree hash is recomputed from archive contents alone and matched against
// the origin commit (REQ-archive-tree-binding), so a signature over the
// origin's git objects proves the archive's contents without trusting
// whoever produced the archive. The recomputation is deliberately
// independent — raw bytes and stdlib hashing, no git library — because a
// library's object model would be a trusted intermediary in exactly the
// path this binding removes.
//
// Git hashes with hardened SHA-1 (collision detection): it differs from
// plain SHA-1 only on crafted collision blocks, where the recomputed hash
// then fails to match — a rejection, which fails closed.

// ObjectFormat is a git object hash format.
type ObjectFormat string

const (
	SHA1   ObjectFormat = "sha1"
	SHA256 ObjectFormat = "sha256"
)

// ErrObjectInvalid is wrapped by every raw-object parsing rejection.
var ErrObjectInvalid = errors.New("invalid git object")

// ErrTreeMismatch is wrapped when the recomputed tree hash does not match
// the origin commit's (REQ-archive-tree-binding).
var ErrTreeMismatch = errors.New("git tree mismatch")

func (f ObjectFormat) new() (hash.Hash, error) {
	switch f {
	case SHA1:
		return sha1.New(), nil //nolint:gosec // git object format
	case SHA256:
		return sha256.New(), nil
	}
	return nil, fmt.Errorf("%w: unknown object format %q", ErrObjectInvalid, string(f))
}

// Size returns the hash length in bytes.
func (f ObjectFormat) Size() (int, error) {
	switch f {
	case SHA1:
		return 20, nil
	case SHA256:
		return 32, nil
	}
	return 0, fmt.Errorf("%w: unknown object format %q", ErrObjectInvalid, string(f))
}

// ObjectHash hashes a raw git object: H("<kind> <decimal len>\x00" + body).
func ObjectHash(f ObjectFormat, kind string, body []byte) ([]byte, error) {
	h, err := f.new()
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(h, "%s %d\x00", kind, len(body))
	h.Write(body)
	return h.Sum(nil), nil
}

// BlobHash hashes size bytes from r as a git blob object.
func BlobHash(f ObjectFormat, size int64, r io.Reader) ([]byte, error) {
	h, err := f.new()
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(h, "blob %d\x00", size)
	n, err := io.Copy(h, io.LimitReader(r, size))
	if err != nil {
		return nil, err
	}
	if n != size {
		return nil, fmt.Errorf("%w: blob content is %d bytes, declared %d", ErrObjectInvalid, n, size)
	}
	return h.Sum(nil), nil
}

// TreeEntry is one entry of a module tree: its path relative to the
// module root, its kind and a file's executability, and its hash in the
// tree's object format — a file's or link's blob hash, a submodule's
// recorded commit id.
type TreeEntry struct {
	Path string
	Kind Kind
	Exec bool
	Hash []byte
}

// TreeHash computes the git tree hash of the module root from its
// entries (REQ-archive-tree-recompute). Paths are assumed to satisfy the
// file-set rules (validated relative paths, no collisions); the empty set
// hashes to git's empty tree. Every hash must be the format's size: a
// submodule's recorded id is copied, so a file set holding one
// recomputes in the recorded format alone.
func TreeHash(f ObjectFormat, entries []TreeEntry) ([]byte, error) {
	hashSize, err := f.Size()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if len(e.Hash) != hashSize {
			return nil, fmt.Errorf("%w: hash for %q is %d bytes, want %d", ErrObjectInvalid, e.Path, len(e.Hash), hashSize)
		}
	}
	return dirHash(f, entries)
}

// dirHash hashes one directory level: entries' paths are relative to this
// directory. Immediate files become blob entries; deeper paths group into
// subtrees hashed recursively.
func dirHash(f ObjectFormat, entries []TreeEntry) ([]byte, error) {
	type item struct {
		name    string
		sortKey string // git sorts directory names as name + "/"
		mode    string
		hash    []byte
	}
	var items []item
	subdirs := make(map[string][]TreeEntry)
	for _, e := range entries {
		name, rest, nested := strings.Cut(e.Path, "/")
		if nested {
			subdirs[name] = append(subdirs[name], TreeEntry{Path: rest, Kind: e.Kind, Exec: e.Exec, Hash: e.Hash})
			continue
		}
		items = append(items, item{name: name, sortKey: name, mode: modeString(e.Kind, e.Exec), hash: e.Hash})
	}
	for name, sub := range subdirs {
		h, err := dirHash(f, sub)
		if err != nil {
			return nil, err
		}
		items = append(items, item{name: name, sortKey: name + "/", mode: "40000", hash: h})
	}
	slices.SortFunc(items, func(a, b item) int { return strings.Compare(a.sortKey, b.sortKey) })

	var body bytes.Buffer
	for _, it := range items {
		body.WriteString(it.mode)
		body.WriteByte(' ')
		body.WriteString(it.name)
		body.WriteByte(0)
		body.Write(it.hash)
	}
	return ObjectHash(f, "tree", body.Bytes())
}

// CommitTree extracts the root tree hash from a raw commit object (its
// body, without the "commit <len>\x00" framing).
func CommitTree(f ObjectFormat, commit []byte) ([]byte, error) {
	hexLen, err := hexLength(f)
	if err != nil {
		return nil, err
	}
	line, _, _ := bytes.Cut(commit, []byte("\n"))
	val, ok := bytes.CutPrefix(line, []byte("tree "))
	if !ok || len(val) != hexLen {
		return nil, fmt.Errorf("%w: commit object does not begin with a %d-hex-digit tree header", ErrObjectInvalid, hexLen)
	}
	raw, err := hex.DecodeString(string(val))
	if err != nil {
		return nil, fmt.Errorf("%w: commit tree header is not hex: %v", ErrObjectInvalid, err)
	}
	return raw, nil
}

func hexLength(f ObjectFormat) (int, error) {
	n, err := f.Size()
	if err != nil {
		return 0, err
	}
	return 2 * n, nil
}

// treeEntries parses a raw tree object body into (mode, name, raw hash)
// triples.
func treeEntries(f ObjectFormat, tree []byte) (names []string, modes []string, hashes [][]byte, err error) {
	size, err := f.Size()
	if err != nil {
		return nil, nil, nil, err
	}
	rest := tree
	for len(rest) > 0 {
		sp := bytes.IndexByte(rest, ' ')
		if sp <= 0 {
			return nil, nil, nil, fmt.Errorf("%w: tree entry missing mode", ErrObjectInvalid)
		}
		mode := string(rest[:sp])
		if _, perr := strconv.ParseUint(mode, 8, 32); perr != nil {
			return nil, nil, nil, fmt.Errorf("%w: tree entry mode %q is not octal", ErrObjectInvalid, mode)
		}
		rest = rest[sp+1:]
		nul := bytes.IndexByte(rest, 0)
		if nul <= 0 {
			return nil, nil, nil, fmt.Errorf("%w: tree entry missing name", ErrObjectInvalid)
		}
		name := string(rest[:nul])
		rest = rest[nul+1:]
		if len(rest) < size {
			return nil, nil, nil, fmt.Errorf("%w: tree entry hash truncated", ErrObjectInvalid)
		}
		names = append(names, name)
		modes = append(modes, mode)
		hashes = append(hashes, rest[:size])
		rest = rest[size:]
	}
	return names, modes, hashes, nil
}

// VerifyTreeBinding checks that computed — the recomputed tree hash of an
// archive's file set — is the tree of the module root in the given raw
// commit object (REQ-archive-tree-binding). subtree is the module root's
// path within the repository ("" for the repository root); treePath holds
// the raw tree objects on the walk from the commit's root tree to the
// module root, in order, each verified against the hash that references it
// before its entries are trusted.
//
// commit itself is NOT hashed here: the caller must already have verified
// the commit bytes against the commit hash the provenance signature covers
// (the signed tag's object field) — without that, the binding is vacuous,
// since the commit names the root tree this walk trusts.
func VerifyTreeBinding(f ObjectFormat, commit []byte, subtree string, treePath [][]byte, computed []byte) error {
	want, err := CommitTree(f, commit)
	if err != nil {
		return err
	}
	segs := []string{}
	if subtree != "" {
		segs = strings.Split(subtree, "/")
	}
	if len(treePath) != len(segs) {
		return fmt.Errorf("%w: %d tree objects for a %d-segment subtree path", ErrObjectInvalid, len(treePath), len(segs))
	}
	for i, seg := range segs {
		raw := treePath[i]
		got, err := ObjectHash(f, "tree", raw)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("%w: tree object %d hashes to %x, expected %x", ErrTreeMismatch, i, got, want)
		}
		names, modes, hashes, err := treeEntries(f, raw)
		if err != nil {
			return err
		}
		next := -1
		for j, name := range names {
			// Directory-ness is the parsed octal mode value: legacy tools
			// wrote zero-padded 040000 entries, git walks them, and this
			// walk is git's walk. (Recomputed encodings stay canonical.)
			v, _ := strconv.ParseUint(modes[j], 8, 32)
			if name == seg && v == 0o40000 {
				next = j
				break
			}
		}
		if next < 0 {
			return fmt.Errorf("%w: subtree segment %q not found as a directory", ErrTreeMismatch, seg)
		}
		want = hashes[next]
	}
	if !bytes.Equal(computed, want) {
		return fmt.Errorf("%w: recomputed module tree %x, origin commit has %x", ErrTreeMismatch, computed, want)
	}
	return nil
}
