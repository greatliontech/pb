// Package modfiles loads the protobuf file sets of a resolution root's
// build: every workspace module from the working tree and every
// build-list module from its verified archive (generation.md
// REQ-gen-compile). It is the one place the "which files belong to
// which module, under which include root" question is answered, so
// tidy's import view and generation's compilation cannot disagree.
package modfiles

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/greatliontech/pb/internal/archive"
	"github.com/greatliontech/pb/internal/modfile"
	"github.com/greatliontech/pb/internal/mvs"
	"github.com/greatliontech/pb/internal/workspace"
)

// Module is one module's protobuf file set: include-root-relative
// paths to source bytes. Workspace modules carry no version.
type Module struct {
	Path    string
	Version string // "" for a workspace module
	Local   bool
	Dir     string // workspace module: its directory relative to the root; "" for externals
	Files   map[string][]byte
}

// Protos returns the module's file paths in sorted order.
func (m Module) Protos() []string {
	return slices.Sorted(maps.Keys(m.Files))
}

// Load returns the build's file sets in deterministic order: workspace
// modules in the root's order, then build-list modules in build-list
// order. fsys is the working tree the root was loaded from.
func Load(ctx context.Context, fsys fs.FS, root *workspace.Root, list []mvs.Requirement, zip func(ctx context.Context, modPath, version string) ([]byte, error)) ([]Module, error) {
	var out []Module
	for _, m := range root.Modules {
		files, err := workspaceFiles(fsys, path.Join(root.Dir, m.Dir))
		if err != nil {
			return nil, err
		}
		out = append(out, Module{Path: m.File.Module, Local: true, Dir: m.Dir, Files: files})
	}
	for _, r := range list {
		b, err := zip(ctx, r.Path, r.Version.String())
		if err != nil {
			return nil, err
		}
		all, err := archive.ZipFiles(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return nil, fmt.Errorf("%s@%s: %w", r.Path, r.Version, err)
		}
		files := map[string][]byte{}
		for p, content := range all {
			if strings.HasSuffix(p, ".proto") {
				files[p] = content
			}
		}
		out = append(out, Module{Path: r.Path, Version: r.Version.String(), Files: files})
	}
	return out, nil
}

// workspaceFiles walks a workspace module's directory for protobuf
// files, module-root-relative — the include root of a workspace module
// is its own directory. A nested module's files belong to the nested
// module and are skipped.
func workspaceFiles(fsys fs.FS, base string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(fsys, base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != base {
				if _, err := fs.Stat(fsys, path.Join(p, modfile.ModuleFileName)); err == nil {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(p, ".proto") {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(p, base+"/")] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
