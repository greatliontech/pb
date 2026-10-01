package migrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/util"
	"github.com/greatliontech/pb/internal/module/modfile"
	"github.com/greatliontech/pb/internal/proto/importcheck"
	"github.com/greatliontech/pb/internal/proto/modfiles"
	"github.com/greatliontech/pb/internal/rootpath"
)

// BundledImports is the bundled imports table (REQ-migrate-imports):
// an import path buf's toolchain satisfies beyond the well-known
// imports, which pb's toolchain does not ship (`module-resolution.md`,
// the well-known imports term), to the module path providing it, the
// file's origin. The spec's table is the same, held to this one by
// TestBundledImportsMatchSpec; the provider's layout is verified by
// TestDependencyLayouts, live.
var BundledImports = map[string]string{
	"google/protobuf/go_features.proto": "github.com/protocolbuffers/protobuf-go/src",
}

// moduleFile is one of a module's own files: its path in the tree,
// and its name relative to the configuration's directory.
type moduleFile struct{ path, name string }

// moduleFiles is a declared module's own files as buf reads them: every
// regular `.proto` file under its directory, those under an `excludes`
// entry left out and, where `includes` names directories, those under
// none of them — the entries spelled relative to the file declaring
// them, a `v2` configuration's `buf.yaml` at the configuration's
// directory, a `v1` module's own `buf.yaml` at its directory — in path
// order; none where the directory is absent.
func moduleFiles(ws billy.Filesystem, cfgDir string, d declared) ([]moduleFile, error) {
	narrow := func(spelled []string) ([]string, error) {
		var out []string
		for _, e := range spelled {
			c, err := rootpath.Clean(e, "the module's directory")
			if err != nil {
				return nil, fmt.Errorf("%s: %w", d.in, err)
			}
			if d.version == "v1" {
				c = path.Join(d.dir, c)
			}
			out = append(out, c)
		}
		return out, nil
	}
	excludes, err := narrow(d.mod.Excludes)
	if err != nil {
		return nil, err
	}
	includes, err := narrow(d.mod.Includes)
	if err != nil {
		return nil, err
	}
	under := func(rel string, dirs []string) bool {
		for _, dir := range dirs {
			if rel == dir || rootpath.Contains(dir, rel) {
				return true
			}
		}
		return false
	}
	dir := path.Join(cfgDir, d.dir)
	var protos []moduleFile
	err = util.Walk(ws, dir, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || !strings.HasSuffix(p, ".proto") {
			return nil
		}
		name := p
		if cfgDir != "." {
			name = strings.TrimPrefix(p, cfgDir+"/")
		}
		if under(name, excludes) || len(includes) > 0 && !under(name, includes) {
			return nil
		}
		protos = append(protos, moduleFile{p, name})
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("walking %s: %w", dir, err)
	}
	sort.Slice(protos, func(i, j int) bool { return protos[i].path < protos[j].path })
	return protos, nil
}

// pathKeyed is every module path a replacement may be keyed by for
// Imports' sake — a bundled import's provider, a workspace module —
// its target the path itself, a version alone (Deps).
func pathKeyed(l *Layout) map[string]bool {
	keys := map[string]bool{}
	for _, p := range BundledImports {
		keys[p] = true
	}
	for _, f := range l.Modules {
		keys[f.Module] = true
	}
	return keys
}

// Imports declares what the modules' own files import that buf
// satisfied with no declaration (REQ-migrate-imports): a sibling
// workspace module's file — buf's modules import one another freely,
// while pb's tidy keeps the version a module declares for a sibling,
// the one consumers require — declares the sibling at the version a
// replacement keyed by the path gives, else the layout declares the
// path at already, else discovery names; a bundled import declares
// its provider the same way. Each module's declarations are recorded on
// the layout, a mapped fact per module and provider naming the first
// file importing it; a provider whose version is not discovered, or a
// file whose imports cannot be read, an unmapped fact. Every other
// import — well-known, the module's own, a declared dependency's, or
// one no module provides, which the tidy reports — is the
// migration's business nowhere; one two siblings provide is an
// unmapped fact, pb never picking a provider. A replacement keyed by
// a path no file imports fails naming it.
func Imports(ctx context.Context, ws billy.Filesystem, cfgDir string, src *Source, d Discovery, repl Replacements, l *Layout) ([]Fact, error) {
	if src == nil || l == nil {
		return nil, fmt.Errorf("no buf configuration read")
	}
	decls, err := src.decls()
	if err != nil {
		return nil, err
	}
	if l.Versions == nil {
		l.Versions = map[string]string{}
	}
	var facts []Fact
	used := map[string]bool{} // path-keyed replacements a declaration drew on
	version := func(p string) (string, string, error) {
		if r, ok := repl.Deps[p]; ok && r.Version != "" {
			used[p] = true
			l.Versions[p] = r.Version
			return r.Version, "--dep", nil
		}
		if v, ok := l.Versions[p]; ok {
			return v, "declared already", nil
		}
		latest, err := d.Latest(ctx, p)
		if err != nil {
			return "", "", err
		}
		l.Versions[p] = latest.String()
		return l.Versions[p], "discovered", nil
	}
	// Each module's own files, as buf reads it — its excludes and
	// includes judging them — walked once: a sibling provides an
	// import only through them.
	files := map[string][]moduleFile{} // module dir -> its files
	provides := map[string]map[string]bool{}
	for _, decl := range decls {
		protos, err := moduleFiles(ws, cfgDir, decl)
		if err != nil {
			return nil, err
		}
		files[decl.dir] = protos
		set := map[string]bool{}
		for _, f := range protos {
			set[f.name] = true
		}
		provides[decl.dir] = set
	}
	// declare records the provider of one import of a module's file,
	// where the import names a sibling's file or a bundled import, once
	// per module and provider, its fact under the first file naming it.
	declare := func(decl declared, mod *modfile.File, done map[string]bool, name, imp string) {
		if modfiles.WellKnown(imp) {
			return
		}
		provider, why := "", ""
		key := name + " import " + imp
		if bp, ok := BundledImports[imp]; ok {
			provider, why = bp, "buf bundles the import, pb's toolchain ships it not"
		} else {
			var providers []string
			for _, other := range decls {
				if other.dir != decl.dir && provides[other.dir][path.Join(other.dir, imp)] {
					providers = append(providers, l.Modules[src.Rooted(other.dir)].Module)
				}
			}
			if len(providers) > 1 {
				// Two siblings providing one path: pb never picks a
				// provider (generation.md REQ-gen-compile), so nothing
				// is declared and the compile names them.
				if !done[imp] {
					done[imp] = true
					facts = append(facts, unmapped(key, "provided by "+strings.Join(providers, " and ")+": pb never picks a provider, declare the one meant"))
				}
				return
			}
			if len(providers) == 1 {
				provider, why = providers[0], "a workspace module: buf's modules import one another freely, pb declares the version consumers require"
			}
		}
		if provider == "" || done[provider] {
			return
		}
		done[provider] = true
		v, from, err := version(provider)
		if err != nil {
			facts = append(facts, unmapped(key, "no version discovered for "+provider+" ("+oneLine(err.Error())+"): pass --dep "+provider+"="+provider+"@<version>"))
			return
		}
		if mod.Deps == nil {
			mod.Deps = map[string]string{}
		}
		mod.Deps[provider] = v
		facts = append(facts, mapped(key, "deps: "+provider+"@"+v+" ("+why+", "+from+")"))
	}
	for _, decl := range decls {
		mod := l.Modules[src.Rooted(decl.dir)]
		declared := map[string]bool{} // provider path -> declared by this module here
		for _, f := range files[decl.dir] {
			// A file unread — unreadable, or not parsing — declares
			// nothing here: its imports are unread, an unmapped fact,
			// the tidy and the compile naming what they find later.
			b, err := util.ReadFile(ws, f.path)
			if err == nil {
				var imports []string
				if imports, err = importcheck.Imports(f.name, b); err == nil {
					for _, imp := range imports {
						declare(decl, mod, declared, f.name, imp)
					}
					continue
				}
			}
			facts = append(facts, unmapped(f.name, "its imports unread: "+oneLine(err.Error())))
		}
	}
	// A replacement keyed by a path nothing imports names what the
	// configuration never declares.
	var unused []string
	for p := range pathKeyed(l) {
		if _, ok := repl.Deps[p]; ok && !used[p] {
			unused = append(unused, p)
		}
	}
	sort.Strings(unused)
	if len(unused) > 0 {
		return nil, fmt.Errorf("--dep %s: no file of the configuration imports what it provides", strings.Join(unused, ", --dep "))
	}
	return facts, nil
}
