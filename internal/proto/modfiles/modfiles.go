// Package modfiles loads the protobuf file sets of a resolution root's
// build: every workspace module from the working tree and every
// build-list module from its verified archive (generation.md
// REQ-gen-compile). It is the one place the "which files belong to
// which module, under which include root" question is answered — and
// its converse, which paths are no module's: the well-known imports
// (WellKnown), a module's copy of one being no file of the build
// (Module.Protos) — so tidy's import view, generation's compilation,
// the check verbs' file lists and an export's tree cannot disagree.
//
// The well-known set is probed through the same embedded resolver the
// compiler reads from (wellknownimports), so membership can never
// drift from what compilation actually serves. The set is pinned by
// the protocompile version alone: the descriptor-registry alternative
// (protocompile.WithStandardImports) was rejected because its content
// follows the linked protobuf-go runtime version and drops the
// extension declarations only source retains.
package modfiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/wellknownimports"

	"github.com/greatliontech/pb/internal/module"

	"github.com/greatliontech/pb/internal/module/archive"
	"github.com/greatliontech/pb/internal/module/mvs"
	"github.com/greatliontech/pb/internal/module/version"
	"github.com/greatliontech/pb/internal/module/workspace"
)

// Module is one module's protobuf file set: include-root-relative
// paths to source bytes. Workspace modules carry no version.
type Module struct {
	Path    string
	Version string // "" for a workspace module
	Local   bool
	Dir     string // workspace module: its directory relative to the root; "" for externals
	// Files holds every protobuf source under the include root as
	// loaded; a copy of a well-known path among them is no file of the
	// build (REQ-gen-compile: ignored in favor of the toolchain's), and
	// Protos, the one enumeration, leaves it out.
	Files map[string][]byte
	Rules map[string][]byte // the module's rule files (module.RuleFileSuffix), by the same paths
	// Synthesized marks an external module whose archive holds no
	// module file: a subtree consumed as a module
	// (module-resolution.md REQ-resolve-synthesis), declaring no
	// dependency of its own — what its files import, a workspace
	// module declaring it carries.
	Synthesized bool
}

// Protos returns the module's files of the build in sorted order: every
// path of Files but a well-known one, which the toolchain answers for
// in every compile and no module provides (REQ-gen-compile). Every
// consumer enumerating a module's files — compile targets, import
// views, provider index, check lists, an export's tree — reads this,
// so no consumer can hold a copy the others ignore.
func (m Module) Protos() []string {
	return slices.DeleteFunc(slices.Sorted(maps.Keys(m.Files)), WellKnown)
}

// errNotEmbedded is the base resolver's constant answer, so the probe
// falls through to the embedded well-known sources alone.
var errNotEmbedded = errors.New("not an embedded well-known import")

// wellKnownProbe builds the membership probe: constructed per call —
// cheap wrapper allocations, no I/O — so the package holds no mutable
// state.
func wellKnownProbe() protocompile.Resolver {
	return wellknownimports.WithStandardImports(
		protocompile.ResolverFunc(func(string) (protocompile.SearchResult, error) {
			return protocompile.SearchResult{}, errNotEmbedded
		}),
	)
}

// WellKnown reports whether path names a well-known import: one of the
// toolchain's embedded google/protobuf source files. The set is the
// protobuf installation's — google/protobuf/go_features.proto is not
// shipped with it and resolves through modules like any other import.
func WellKnown(path string) bool {
	res, err := wellKnownProbe().FindFileByPath(path)
	if err != nil {
		return false
	}
	// embed.FS.Open succeeds on directories, and the resolver returns the
	// handle unread — but a well-known import is a readable source file,
	// so membership requires the first byte (or a clean EOF) to prove it.
	var b [1]byte
	_, rerr := res.Source.Read(b[:])
	if c, ok := res.Source.(io.Closer); ok {
		c.Close()
	}
	return rerr == nil || rerr == io.EOF
}

// Load returns the build's file sets in deterministic order: workspace
// modules in the root's order, then build-list modules in build-list
// order. fsys is the working tree the root was loaded from.
func Load(ctx context.Context, fsys fs.FS, root *workspace.Root, list []mvs.Requirement, zip func(ctx context.Context, modPath string, v version.Version) ([]byte, error)) ([]Module, error) {
	var out []Module
	for _, m := range root.Modules {
		files, rules, err := workspaceFiles(fsys, path.Join(root.Dir, m.Dir))
		if err != nil {
			return nil, err
		}
		out = append(out, Module{Path: m.File.Module, Local: true, Dir: m.Dir, Files: files, Rules: rules})
	}
	for _, r := range list {
		b, err := zip(ctx, r.Path, r.Version)
		if err != nil {
			return nil, err
		}
		all, err := archive.ZipFiles(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return nil, fmt.Errorf("%s@%s: %w", r.Path, r.Version, err)
		}
		files, rules := map[string][]byte{}, map[string][]byte{}
		_, declared := all[module.ModuleFileName]
		for p, content := range all {
			switch {
			case module.IsProtoFile(p):
				files[p] = content
			case module.IsRuleFile(p):
				rules[p] = content
			}
		}
		out = append(out, Module{Path: r.Path, Version: r.Version.String(), Files: files, Rules: rules, Synthesized: !declared})
	}
	return out, nil
}

// workspaceFiles walks a workspace module's directory for protobuf
// files and rule files, module-root-relative — the include root of a
// workspace module is its own directory. A nested module's files
// belong to the nested module and are skipped.
func workspaceFiles(fsys fs.FS, base string) (map[string][]byte, map[string][]byte, error) {
	files, rules := map[string][]byte{}, map[string][]byte{}
	err := fs.WalkDir(fsys, base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != base {
				if _, err := fs.Stat(fsys, path.Join(p, module.ModuleFileName)); err == nil {
					return fs.SkipDir
				}
			}
			return nil
		}
		into := files
		switch {
		case module.IsProtoFile(p):
		case module.IsRuleFile(p):
			into = rules
		default:
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		into[strings.TrimPrefix(p, base+"/")] = b
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return files, rules, nil
}
