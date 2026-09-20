package direct

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/greatliontech/pb/internal/module"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/greatliontech/pb/internal/module/archive"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/origin"
)

// This file constructs the direct source's module artifacts from a
// fetched origin repository — the same canonical archives, module file
// bytes, info objects, and verification packs a well-behaved proxy
// serves (REQ-proxy-direct-equivalence). Wire shapes are the module
// proxy protocol's; content derivation is the module archive
// contract's.

// ErrForbiddenEntry is wrapped when a module tree carries a symbolic
// link or git submodule entry under the module root
// (REQ-archive-forbidden-entries): the file set cannot represent it,
// and silently dropping it would produce an archive that digests
// cleanly yet can never tree-bind to the origin commit.
var ErrForbiddenEntry = errors.New("forbidden entry under module root")

// ErrNoModuleRoot is wrapped when a commit's tree has no directory at
// the module's subtree path — an in-spec state consumers classify: a
// subtree absent at a commit is not a declared module there
// (REQ-resolve-synthesized-tags reads listings through it), while for
// artifact construction against a resolved version it is simply a
// failing walk.
var ErrNoModuleRoot = errors.New("module root not present in commit tree")

// commitObject looks up the full-hash commit a resolved version bound.
func (r *Repo) commitObject(commitHash string) (*object.Commit, error) {
	c, err := r.r.CommitObject(plumbing.NewHash(commitHash))
	if err != nil {
		return nil, fmt.Errorf("reading commit %s: %w", commitHash, err)
	}
	return c, nil
}

// moduleRoot walks a commit's tree to the module root, returning the
// root tree and the raw tree bodies along the way — one per subtree
// segment, the commit's root tree first, the module root itself
// excluded — exactly the treePath the verification pack carries and
// archive.VerifyTreeBinding consumes.
func (r *Repo) moduleRoot(c *object.Commit, subtree string) (*object.Tree, [][]byte, error) {
	tree, err := c.Tree()
	if err != nil {
		return nil, nil, fmt.Errorf("reading root tree of %s: %w", c.Hash, err)
	}
	if subtree == "" {
		return tree, nil, nil
	}
	var treePath [][]byte
	for {
		raw, err := r.rawBody(tree.Hash)
		if err != nil {
			return nil, nil, err
		}
		treePath = append(treePath, raw)
		seg, rest, _ := strings.Cut(subtree, "/")
		entry, err := tree.FindEntry(seg)
		if err != nil || entry.Mode != filemode.Dir {
			return nil, nil, fmt.Errorf("%w: %q is not a directory of tree %s", ErrNoModuleRoot, seg, tree.Hash)
		}
		sub, err := object.GetTree(r.r.Storer, entry.Hash)
		if err != nil {
			return nil, nil, fmt.Errorf("reading tree %s at %q: %w", entry.Hash, seg, err)
		}
		tree, subtree = sub, rest
		if subtree == "" {
			return tree, treePath, nil
		}
	}
}

// fileEntry is one regular file of the module root's tree: its
// root-relative path, normalized executability, and blob.
type fileEntry struct {
	path string
	exec bool
	blob *object.Blob
}

// fileSet walks the module root tree into its file set
// (REQ-archive-file-set): every regular file, path relative to the
// root, mode normalized to the exec bit
// (REQ-archive-mode-normalization). A symbolic link or submodule entry
// fails the walk (REQ-archive-forbidden-entries). go-git's tree
// decoder canonicalizes every wire mode onto Dir, Regular, Executable,
// Symlink, or Submodule — nonstandard regular modes (like git's legacy
// group-writable one) arrive here as Regular, and the final arm is the
// submodule arm with no other mode able to reach it.
func (r *Repo) fileSet(tree *object.Tree) ([]fileEntry, error) {
	var out []fileEntry
	var walk func(prefix string, t *object.Tree) error
	walk = func(prefix string, t *object.Tree) error {
		for _, e := range t.Entries {
			path := prefix + e.Name
			switch e.Mode {
			case filemode.Regular, filemode.Executable:
				blob, err := object.GetBlob(r.r.Storer, e.Hash)
				if err != nil {
					return fmt.Errorf("reading blob %s at %q: %w", e.Hash, path, err)
				}
				out = append(out, fileEntry{path: path, exec: e.Mode == filemode.Executable, blob: blob})
			case filemode.Dir:
				sub, err := object.GetTree(r.r.Storer, e.Hash)
				if err != nil {
					return fmt.Errorf("reading tree %s at %q: %w", e.Hash, path, err)
				}
				if err := walk(path+"/", sub); err != nil {
					return err
				}
			case filemode.Symlink:
				return fmt.Errorf("%w: %q is a symbolic link", ErrForbiddenEntry, path)
			default:
				return fmt.Errorf("%w: %q is a git submodule", ErrForbiddenEntry, path)
			}
		}
		return nil
	}
	if err := walk("", tree); err != nil {
		return nil, err
	}
	return out, nil
}

// fileSetInfos maps walked entries to the archive contract's file-set
// description, sizes from blob metadata — available before any content
// is read.
func fileSetInfos(entries []fileEntry) []archive.FileInfo {
	infos := make([]archive.FileInfo, len(entries))
	for i, e := range entries {
		infos[i] = archive.FileInfo{Path: e.path, Exec: e.exec, Size: e.blob.Size}
	}
	return infos
}

// moduleFileSet resolves a commit's module root and walks its file
// set, then validates the whole archive discipline — paths,
// collisions, nested module file, size limit — over blob metadata,
// before any content is read. It also returns the raw tree bodies of
// the walk (the verification pack's treePath). Every repository-bound
// artifact constructor goes through it: a version whose file set the
// archive contract rejects has no artifacts at all
// (REQ-proxy-direct-equivalence), so a tree Archive rejects is never
// partially served by another endpoint.
func (r *Repo) moduleFileSet(commitHash, subtree string) ([]fileEntry, [][]byte, error) {
	c, err := r.commitObject(commitHash)
	if err != nil {
		return nil, nil, err
	}
	root, treePath, err := r.moduleRoot(c, subtree)
	if err != nil {
		return nil, nil, err
	}
	entries, err := r.fileSet(root)
	if err != nil {
		return nil, nil, err
	}
	if err := archive.ValidateFileSet(fileSetInfos(entries)); err != nil {
		return nil, nil, err
	}
	return entries, treePath, nil
}

// Archive writes the module version's canonical zip and returns its
// module digest. commitHash is the full hash a resolved version bound
// (ResolveVersion); subtree is the module root within the repository.
func (r *Repo) Archive(w io.Writer, commitHash, subtree string) (string, error) {
	entries, _, err := r.moduleFileSet(commitHash, subtree)
	if err != nil {
		return "", err
	}
	files := make([]archive.File, 0, len(entries))
	for _, e := range entries {
		content, err := blobBytes(e.blob)
		if err != nil {
			return "", fmt.Errorf("reading blob at %q: %w", e.path, err)
		}
		files = append(files, archive.File{Path: e.path, Exec: e.exec, Body: bytes.NewReader(content)})
	}
	return archive.WriteZip(w, files)
}

// blobBytes materializes a loaded blob's content.
func blobBytes(blob *object.Blob) ([]byte, error) {
	rd, err := blob.Reader()
	if err != nil {
		return nil, err
	}
	defer rd.Close()
	return io.ReadAll(rd)
}

// ModuleFileBytes returns the module file's bytes exactly as in the
// module's file set, ok=false when the module is synthesized — the
// root carries no module file, and a proxy answers not-here for its
// .mod (REQ-proxy-not-found). The shared moduleFileSet walk enforces
// the whole archive discipline, so a tree Archive rejects is never
// partially served.
func (r *Repo) ModuleFileBytes(commitHash, subtree string) ([]byte, bool, error) {
	entries, _, err := r.moduleFileSet(commitHash, subtree)
	if err != nil {
		return nil, false, err
	}
	for _, e := range entries {
		if e.path != module.ModuleFileName {
			continue
		}
		data, err := blobBytes(e.blob)
		if err != nil {
			return nil, false, fmt.Errorf("reading module file blob: %w", err)
		}
		return data, true, nil
	}
	return nil, false, nil
}

// InfoJSON renders a version's info object exactly as a proxy serves
// it (REQ-proxy-endpoints): the canonical version and the commit time
// as RFC 3339 UTC.
func InfoJSON(v version.Version, c origin.Commit) ([]byte, error) {
	return json.Marshal(struct {
		Version string `json:"version"`
		Time    string `json:"time"`
	}{v.String(), c.Time.UTC().Format(time.RFC3339)})
}

// VerificationPack renders a version's provenance envelope when
// git-signed-tag evidence exists: the module's release tag is an
// annotated tag object carrying a signature and naming a commit.
// ok=false is in-spec absence — a pseudo-version (no tag names it), a
// lightweight tag, an unsigned tag object, or a tag not directly
// referencing a commit — for which a proxy answers not-here on .prov
// (REQ-proxy-not-found). Base64 uses the standard padded alphabet with
// no whitespace: the canonical wire spelling
// (REQ-proxy-prov-envelope).
func (r *Repo) VerificationPack(v version.Version, subtree string) ([]byte, bool, error) {
	if v.IsPseudo() {
		return nil, false, nil
	}
	ref, err := r.tagRef(v, subtree)
	if err != nil {
		return nil, false, err
	}
	tag, err := r.r.TagObject(ref.Hash())
	if err != nil {
		if err == plumbing.ErrObjectNotFound {
			// A lightweight tag: no tag object exists to be signed.
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("provenance for %s: reading tag object: %w", v, err)
	}
	// Either signature home counts: the canonical trailer appended after
	// the message, or the gpgsig-sha256 header of SHA-256 repositories.
	if tag.Signature == "" && tag.SignatureSHA256 == "" {
		return nil, false, nil
	}
	if tag.TargetType != plumbing.CommitObject {
		// The evidence shape is tag-over-commit; a signed tag naming
		// anything else carries no verifiable module provenance.
		return nil, false, nil
	}
	c, err := r.commitObject(tag.Target.String())
	if err != nil {
		// A signed tag referencing an absent commit is corruption, not
		// absence of evidence.
		return nil, false, fmt.Errorf("provenance for %s: %w", v, err)
	}
	// The full file-set walk, not just the treePath: a version whose
	// file set the archive contract rejects has no artifacts at all,
	// the pack included (REQ-proxy-direct-equivalence).
	_, treePath, err := r.moduleFileSet(c.Hash.String(), subtree)
	if err != nil {
		return nil, false, fmt.Errorf("provenance for %s: %w", v, err)
	}
	rawTag, err := r.rawBody(ref.Hash())
	if err != nil {
		return nil, false, fmt.Errorf("provenance for %s: %w", v, err)
	}
	rawCommit, err := r.rawBody(c.Hash)
	if err != nil {
		return nil, false, fmt.Errorf("provenance for %s: %w", v, err)
	}
	format, err := objectFormatName(c.Hash)
	if err != nil {
		return nil, false, fmt.Errorf("provenance for %s: %w", v, err)
	}
	enc := base64.StdEncoding
	type evidence struct {
		Type         string   `json:"type"`
		ObjectFormat string   `json:"objectFormat"`
		Tag          string   `json:"tag"`
		Commit       string   `json:"commit"`
		TreePath     []string `json:"treePath"`
	}
	ev := evidence{
		Type:         "git-signed-tag",
		ObjectFormat: format,
		Tag:          enc.EncodeToString(rawTag),
		Commit:       enc.EncodeToString(rawCommit),
		TreePath:     make([]string, 0, len(treePath)),
	}
	for _, t := range treePath {
		ev.TreePath = append(ev.TreePath, enc.EncodeToString(t))
	}
	body, err := json.Marshal(struct {
		FormatVersion int        `json:"formatVersion"`
		Evidence      []evidence `json:"evidence"`
	}{1, []evidence{ev}})
	if err != nil {
		return nil, false, fmt.Errorf("provenance for %s: %w", v, err)
	}
	return body, true, nil
}

// objectFormatName maps a hash to its git object format name on the
// provenance wire (REQ-proxy-prov-envelope).
func objectFormatName(h plumbing.Hash) (string, error) {
	switch len(h.String()) {
	case 40:
		return "sha1", nil
	case 64:
		return "sha256", nil
	}
	return "", fmt.Errorf("hash %q is in no known git object format", h)
}

// rawBody reads an object's raw body bytes — the content behind the
// "<kind> <len>\0" header, the byte convention the provenance envelope
// carries and archive tree binding consumes. The bytes are verbatim
// origin storage: any re-encoding would break signature verification
// over legitimately signed objects.
func (r *Repo) rawBody(h plumbing.Hash) ([]byte, error) {
	eo, err := r.r.Storer.EncodedObject(plumbing.AnyObject, h)
	if err != nil {
		return nil, fmt.Errorf("reading object %s: %w", h, err)
	}
	rd, err := eo.Reader()
	if err != nil {
		return nil, fmt.Errorf("opening object %s: %w", h, err)
	}
	defer rd.Close()
	body, err := io.ReadAll(rd)
	if err != nil {
		return nil, fmt.Errorf("reading object %s: %w", h, err)
	}
	return body, nil
}
