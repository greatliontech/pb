// Package breaking materializes the comparison base of a module under
// check (check-rules.md §Breaking-change alignment): the lint file's
// one base form — a git reference, a tagged version, or the version
// the lockfile pins — yields the base module's files, which the
// verb compiles in place of the module under check's and pairs with
// the checked schema. A reference is read from the repository the
// workspace root lies in, at the module's directory as it lies now;
// a version is acquired through the same client as any dependency,
// so it is verified and pinned on the way.
package breaking

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/greatliontech/pb/internal/check/lintfile"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/lockfile"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/proto/modfiles"
)

// ErrBase is wrapped when the base cannot be materialized: the form
// and the cause are named (REQ-break-base-materialized); nothing
// degrades to an empty base.
var ErrBase = errors.New("breaking base")

// Sources is what materializing draws on: the repository the
// workspace root lies in, opened on demand with the root's directory
// within it (an error where the root lies in none); the client's
// verified archive of a module at a version; the pin store.
type Sources struct {
	Repo func() (*git.Repository, string, error)
	Zip  func(ctx context.Context, modPath string, v version.Version) ([]byte, error)
	Lock *lockfile.File
}

// Module is the module under check as the base is drawn for it: its
// path, and its directory relative to the workspace root.
type Module struct {
	Path string
	Dir  string
}

// Base is a materialized base: the module's protobuf files by
// module-relative path, and the label a finding names it by.
type Base struct {
	Files map[string][]byte
	Label string
}

// Materialize yields the base of the module under check in the lint
// file's form (REQ-break-base).
func Materialize(ctx context.Context, form lintfile.Base, m Module, src Sources) (*Base, error) {
	switch form.Form {
	case lintfile.BaseRef:
		return fromRef(form.Value, m, src)
	case lintfile.BaseVersion:
		v, err := version.Parse(form.Value)
		if err != nil {
			return nil, fmt.Errorf("%w %s: %v", ErrBase, form, err)
		}
		return fromVersion(ctx, form, m, v, src)
	case lintfile.BasePinned:
		v, err := pinned(src.Lock, m.Path)
		if err != nil {
			return nil, fmt.Errorf("%w pinned: %v", ErrBase, err)
		}
		return fromVersion(ctx, lintfile.Base{Form: lintfile.BasePinned, Value: v.String()}, m, v, src)
	}
	return nil, fmt.Errorf("%w: %q is no form", ErrBase, form.Form)
}

// pinned is the highest version the pin store holds the module at;
// no pin, or a pin whose version does not parse, is an error naming
// the module and the pin.
func pinned(lock *lockfile.File, modPath string) (version.Version, error) {
	var best version.Version
	found := false
	if lock != nil {
		for _, p := range lock.Modules {
			if p.Path != modPath {
				continue
			}
			v, err := version.Parse(p.Version)
			if err != nil {
				return best, fmt.Errorf("the lockfile pins %s at %q, which is no version: %v", modPath, p.Version, err)
			}
			if !found || version.Compare(v, best) > 0 {
				best, found = v, true
			}
		}
	}
	if !found {
		return best, fmt.Errorf("the lockfile pins no version of %s", modPath)
	}
	return best, nil
}

// fromVersion acquires the module at the version through the client,
// verified and pinned as any dependency, and takes its protobuf files.
func fromVersion(ctx context.Context, form lintfile.Base, m Module, v version.Version, src Sources) (*Base, error) {
	b, err := src.Zip(ctx, m.Path, v)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %v", ErrBase, form, err)
	}
	files, _, _, err := modfiles.UnpackArchive(b)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %s@%s: %v", ErrBase, form, m.Path, v, err)
	}
	return &Base{Files: files, Label: m.Path + "@" + v.String()}, nil
}

// fromRef reads the module's directory from the repository at the
// reference: every protobuf file beneath it, a nested module's
// directory excluded as the working tree's walk excludes it — any
// entry named as a module file marks one. A symbolic link in the
// tree is no file of the base: a repository stores the link, not
// what it points at.
func fromRef(ref string, m Module, src Sources) (*Base, error) {
	if src.Repo == nil {
		return nil, fmt.Errorf("%w ref %s: no repository source is wired", ErrBase, ref)
	}
	repo, rootDir, err := src.Repo()
	if err != nil {
		return nil, fmt.Errorf("%w ref %s: %v", ErrBase, ref, err)
	}
	hash, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return nil, fmt.Errorf("%w ref %s: the repository resolves no such reference: %v", ErrBase, ref, err)
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return nil, fmt.Errorf("%w ref %s: %s is no commit: %v", ErrBase, ref, hash, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("%w ref %s: %v", ErrBase, ref, err)
	}
	dir := path.Join(rootDir, m.Dir)
	if dir != "." && dir != "" {
		sub, err := tree.Tree(dir)
		if err != nil {
			return nil, fmt.Errorf("%w ref %s: the repository at %s has no directory %s", ErrBase, ref, hash, dir)
		}
		tree = sub
	}
	files := map[string][]byte{}
	if err := collect(tree, "", true, files); err != nil {
		return nil, fmt.Errorf("%w ref %s: %v", ErrBase, ref, err)
	}
	return &Base{Files: files, Label: m.Path + "@" + ref}, nil
}

// collect walks a tree for protobuf files, skipping a subtree that
// holds an entry named as a module file — a nested module's — but
// never the root.
func collect(tree *object.Tree, prefix string, root bool, files map[string][]byte) error {
	if !root {
		for _, e := range tree.Entries {
			if e.Name == module.ModuleFileName {
				return nil
			}
		}
	}
	for _, e := range tree.Entries {
		p := path.Join(prefix, e.Name)
		switch {
		case e.Mode == filemode.Dir:
			sub, err := tree.Tree(e.Name)
			if err != nil {
				return err
			}
			if err := collect(sub, p, false, files); err != nil {
				return err
			}
		case e.Mode.IsRegular() && module.IsProtoFile(e.Name):
			f, err := tree.File(e.Name)
			if err != nil {
				return err
			}
			r, err := f.Reader()
			if err != nil {
				return err
			}
			b, err := io.ReadAll(r)
			r.Close()
			if err != nil {
				return err
			}
			files[p] = b
		}
	}
	return nil
}
