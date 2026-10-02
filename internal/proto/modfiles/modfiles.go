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
	// Dir is the module's directory relative to the root for a module
	// read from the working tree — a workspace module, or a directory
	// replacement standing for a build-list pair — and "" for a
	// fetched module.
	Dir string
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

// Label names the module as reports and errors spell it: a build-list
// module with its selected version — and, read from a directory
// replacement, the directory, which the user can find — a workspace
// module by its path alone, which has no version.
func (m Module) Label() string {
	if m.Local {
		return m.Path
	}
	if m.Dir != "" {
		return m.Path + "@" + m.Version + " (" + workspace.Replacement{Dir: m.Dir}.String() + ")"
	}
	return m.Path + "@" + m.Version
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
// order. fsys is the working tree the root was loaded from. A
// build-list pair's files are its source's (workspace.Root.Source):
// the archive zip serves of the pair or of its pinned replacement, or
// a directory replacement's working-tree files read as a workspace
// module's are (REQ-work-replace, REQ-work-replace-dir) — zip is only
// ever asked for a source pair, never a replaced one.
func Load(ctx context.Context, fsys fs.FS, root *workspace.Root, list []mvs.Requirement, zip func(ctx context.Context, modPath string, v version.Version) ([]byte, error)) ([]Module, error) {
	var out []Module
	for _, m := range root.Modules {
		files, rules, err := WorkspaceFiles(fsys, path.Join(root.Dir, m.Dir))
		if err != nil {
			return nil, err
		}
		out = append(out, Module{Path: m.File.Module, Local: true, Dir: m.Dir, Files: files, Rules: rules})
	}
	for _, r := range list {
		src := root.Source(r.Path, r.Version)
		if src.Module != nil {
			files, rules, err := WorkspaceFiles(fsys, path.Join(root.Dir, src.Module.Dir))
			if err != nil {
				return nil, err
			}
			out = append(out, Module{Path: r.Path, Version: r.Version.String(), Dir: src.Module.Dir, Files: files, Rules: rules})
			continue
		}
		b, err := zip(ctx, src.Path, src.Version)
		if err != nil {
			return nil, err
		}
		files, rules, declared, err := UnpackArchive(b)
		if err != nil {
			return nil, fmt.Errorf("%s@%s: %w", r.Path, r.Version, err)
		}
		out = append(out, Module{Path: r.Path, Version: r.Version.String(), Files: files, Rules: rules, Synthesized: !declared})
	}
	return out, nil
}

// UnpackArchive reads a verified archive's bytes as a module's file
// set: its protobuf files and its rule files, each by archive-relative
// path, and whether the archive declares a module file.
func UnpackArchive(b []byte) (files, rules map[string][]byte, declared bool, err error) {
	all, err := archive.ZipFiles(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, nil, false, err
	}
	_, declared = all[module.ModuleFileName]
	files, rules = map[string][]byte{}, map[string][]byte{}
	for p, content := range all {
		switch {
		case module.IsProtoFile(p):
			files[p] = content
		case module.IsRuleFile(p):
			rules[p] = content
		}
	}
	return files, rules, declared, nil
}

// WorkspaceFiles walks a workspace module's directory for protobuf
// files and rule files, module-root-relative — the include root of a
// workspace module is its own directory. A nested module's files
// belong to the nested module and are skipped.
func WorkspaceFiles(fsys fs.FS, base string) (map[string][]byte, map[string][]byte, error) {
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

// WellKnownSet is the toolchain's well-known imports whole: every
// embedded google/protobuf source file by its import path, with its
// bytes — the set the language server serves and copies (lsp.md
// REQ-lsp-dependency-files), enumerated from the embedded tree itself
// so it never lags the toolchain.
func WellKnownSet() (map[string][]byte, error) {
	probe := wellKnownProbe()
	set := map[string][]byte{}
	var walk func(dir string) error
	walk = func(dir string) error {
		res, err := probe.FindFileByPath(dir)
		if err != nil {
			return fmt.Errorf("well-known imports: %s: %w", dir, err)
		}
		d, ok := res.Source.(fs.ReadDirFile)
		if !ok {
			return fmt.Errorf("well-known imports: %s is no directory of the embedded set", dir)
		}
		entries, err := d.ReadDir(-1)
		d.Close()
		if err != nil {
			return fmt.Errorf("well-known imports: %s: %w", dir, err)
		}
		for _, e := range entries {
			p := path.Join(dir, e.Name())
			if e.IsDir() {
				if err := walk(p); err != nil {
					return err
				}
				continue
			}
			if !module.IsProtoFile(p) {
				continue
			}
			res, err := probe.FindFileByPath(p)
			if err != nil {
				return fmt.Errorf("well-known imports: %s: %w", p, err)
			}
			b, err := io.ReadAll(res.Source)
			if c, ok := res.Source.(io.Closer); ok {
				c.Close()
			}
			if err != nil {
				return fmt.Errorf("well-known imports: %s: %w", p, err)
			}
			set[p] = b
		}
		return nil
	}
	if err := walk("google"); err != nil {
		return nil, err
	}
	return set, nil
}
