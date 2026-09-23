// Package compile compiles a build's protobuf files into linked
// descriptors (generation.md REQ-gen-compile). Imports resolve first
// against the toolchain's well-known sources and then against the
// build's modules, each module's file set rooted at its own include
// root; an import path two modules provide is an error — pb never
// chooses a provider by heuristic. Output order is the workspace's:
// module use order, then file path.
package compile

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/linker"
	"github.com/bufbuild/protocompile/wellknownimports"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/greatliontech/pb/internal/proto/importcheck"
	"github.com/greatliontech/pb/internal/proto/modfiles"
)

// AmbiguousError names every import path provided by more than one
// module, with its providers, in deterministic order.
type AmbiguousError struct {
	Paths []Ambiguous
}

// Ambiguous is one multiply-provided path.
type Ambiguous struct {
	Path      string
	Providers []string
}

func (e *AmbiguousError) Error() string {
	var b strings.Builder
	for i, a := range e.Paths {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s is provided by %s", a.Path, strings.Join(a.Providers, " and "))
	}
	return "ambiguous protobuf import paths: " + b.String()
}

// Result is a compiled build: the workspace modules' files, fully
// linked, in the workspace's order.
type Result struct {
	// Files holds every workspace module's file in module use order,
	// then file path order; each file's transitive imports are reachable
	// through it.
	Files linker.Files
}

// Compile compiles the workspace modules' files of a build. Import
// satisfaction is checked first (REQ-resolve-unsatisfied-imports), so
// a missing import is reported exhaustively rather than as the
// compiler's first failure.
func Compile(ctx context.Context, mods []modfiles.Module) (*Result, error) {
	var targets []string
	for _, m := range mods {
		if m.Local {
			targets = append(targets, m.Protos()...)
		}
	}
	return compileTargets(ctx, mods, -1, targets)
}

// CompileFiles compiles the given files of one module of a build,
// mods[i], every module a provider of imports and none other a
// target nor a requirer. The module's every file has its imports
// checked for satisfaction first, the files given alone are compiled:
// all of them where a base stands in for its module (check-rules.md
// REQ-break-base-materialized); an entry's anchor where a dependency
// table entry is verified (migrate.md REQ-migrate-deps), the
// repository's files resolving their imports from the named root
// though it may hold files no one compiles together.
func CompileFiles(ctx context.Context, mods []modfiles.Module, i int, files []string) (*Result, error) {
	return compileTargets(ctx, mods, i, files)
}

// compileTargets compiles the target files with every module
// providing imports, the requirers' imports — one module's, mods[i],
// or every module's for a negative i — checked for satisfaction
// first.
func compileTargets(ctx context.Context, mods []modfiles.Module, i int, targets []string) (*Result, error) {
	views, err := importcheck.Views(mods, func(m modfiles.Module, _ string) string { return moduleLabel(m) })
	if err != nil {
		return nil, err
	}
	requirers := views
	if i >= 0 {
		requirers = views[i : i+1]
	}
	if err := importcheck.CheckRequirers(views, requirers); err != nil {
		return nil, err
	}

	sources, err := providerIndex(mods)
	if err != nil {
		return nil, err
	}
	c := protocompile.Compiler{
		// The composite consults module sources first and the embedded
		// well-known set as fallback; the spec's well-known-first
		// precedence holds because a well-known path never enters the
		// index (a module's copy is no file of the build,
		// modfiles.Module.Protos), not by the wrapper.
		Resolver:       wellknownimports.WithStandardImports(byteResolver(sources)),
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	files, err := c.Compile(ctx, targets...)
	if err != nil {
		return nil, err
	}
	return &Result{Files: files}, nil
}

func moduleLabel(m modfiles.Module) string {
	if m.Local {
		return m.Path
	}
	return m.Path + "@" + m.Version
}

// Providers maps every import path the build provides to the index in
// mods of its one provider, failing on any path with two providers
// (REQ-gen-compile: pb never picks a provider by heuristic). Well-known
// paths are the toolchain's and never enter the map — resolved first,
// never looked up in modules — a module's copy of one being no file
// of the build (modfiles.Module.Protos). The one answer to which
// module a path belongs to: the compiler's sources and an export's
// tree both read it.
func Providers(mods []modfiles.Module) (map[string]int, error) {
	index := map[string]int{}
	providers := map[string][]string{}
	for i, m := range mods {
		for _, p := range m.Protos() {
			providers[p] = append(providers[p], moduleLabel(m))
			index[p] = i
		}
	}
	// Sorted key iteration makes the report deterministic by
	// construction — no post-hoc sort to forget.
	var amb []Ambiguous
	for _, p := range slices.Sorted(maps.Keys(providers)) {
		if who := providers[p]; len(who) > 1 {
			amb = append(amb, Ambiguous{Path: p, Providers: who})
		}
	}
	if len(amb) > 0 {
		return nil, &AmbiguousError{Paths: amb}
	}
	return index, nil
}

// providerIndex maps every import path to its one provider's bytes,
// over Providers.
func providerIndex(mods []modfiles.Module) (map[string][]byte, error) {
	index, err := Providers(mods)
	if err != nil {
		return nil, err
	}
	sources := make(map[string][]byte, len(index))
	for p, i := range index {
		sources[p] = mods[i].Files[p]
	}
	return sources, nil
}

// Closure returns the import closure of a compiled result (export.md,
// the import closure term): every compiled file — the workspace
// modules' under Compile, the given files' under CompileFiles — and
// every file one imports, transitively, across the build's modules;
// the well-known imports, the toolchain's, never among them and never
// walked into; sorted, each path once.
func (r *Result) Closure() []string {
	seen := map[string]bool{}
	var walk func(f protoreflect.FileDescriptor)
	walk = func(f protoreflect.FileDescriptor) {
		if seen[f.Path()] {
			return
		}
		seen[f.Path()] = true
		imports := f.Imports()
		for i := 0; i < imports.Len(); i++ {
			imp := imports.Get(i)
			if modfiles.WellKnown(imp.Path()) {
				continue
			}
			walk(imp.FileDescriptor)
		}
	}
	for _, f := range r.Files {
		walk(f)
	}
	return slices.Sorted(maps.Keys(seen))
}

// byteResolver serves module sources by import path; anything else is
// not found, so the well-known wrapper and the compiler's own error
// paths see a clean miss.
type byteResolver map[string][]byte

func (r byteResolver) FindFileByPath(p string) (protocompile.SearchResult, error) {
	b, ok := r[p]
	if !ok {
		return protocompile.SearchResult{}, protoregistry.NotFound
	}
	return protocompile.SearchResult{Source: io.Reader(bytes.NewReader(b))}, nil
}
