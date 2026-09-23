// Package importcheck checks protobuf import satisfaction across a build
// list (REQ-resolve-unsatisfied-imports): every import of every module's
// files must be a well-known import — the toolchain's embedded
// google/protobuf sources, modfiles.WellKnown — or name a file some
// build-list module provides; anything else fails, naming the importing
// module, the importing file, and the unsatisfied import path.
package importcheck

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/bufbuild/protocompile/ast"
	"github.com/bufbuild/protocompile/parser"
	"github.com/bufbuild/protocompile/reporter"
	"github.com/greatliontech/pb/internal/proto/modfiles"
)

// Imports returns the import paths a protobuf source file declares —
// plain, public, and weak alike, since each must resolve to compile.
// filename labels parse diagnostics only.
func Imports(filename string, src []byte) ([]string, error) {
	// A nil reporter fails fast: Parse's returned error is the first
	// positioned syntax error itself, so no separate handler.Error()
	// consultation exists to diverge from it.
	f, err := parser.Parse(filename, bytes.NewReader(src), reporter.NewHandler(nil))
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filename, err)
	}
	var out []string
	for _, decl := range f.Decls {
		if imp, ok := decl.(*ast.ImportNode); ok {
			out = append(out, imp.Name.AsString())
		}
	}
	return out, nil
}

// Views projects a build's modules onto the checker's view: each
// module's proto files with the imports each declares. A file whose
// imports cannot be read fails with the label the caller gives it (a
// path a user can find) wrapping the parse error; the files are read
// in sorted order, so which of several malformed files is named is a
// function of the file set alone. Compilation and tidy both check a
// build this way; one projection keeps their views identical.
func Views(mods []modfiles.Module, label func(m modfiles.Module, file string) string) ([]Module, error) {
	views := make([]Module, 0, len(mods))
	for _, m := range mods {
		files := map[string][]string{}
		for _, p := range m.Protos() {
			imports, err := Imports(p, m.Files[p])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", label(m, p), err)
			}
			files[p] = imports
		}
		views = append(views, Module{Path: m.Path, Files: files})
	}
	return views, nil
}

// Module is one build-list member's import-relevant view: its module
// path and its file set's include-root-relative protobuf files, each
// with the imports it declares.
type Module struct {
	Path  string
	Files map[string][]string
}

// Unsatisfied is one import no module in the build list satisfies.
type Unsatisfied struct {
	Module string
	File   string
	Import string
}

// UnsatisfiedError carries every unsatisfied import of a build list, in
// deterministic order.
type UnsatisfiedError struct {
	Unsatisfied []Unsatisfied
}

func (e *UnsatisfiedError) Error() string {
	var b strings.Builder
	for i, u := range e.Unsatisfied {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "module %s: file %s imports %q, which no module in the build list satisfies",
			u.Module, u.File, u.Import)
	}
	return b.String()
}

// Check verifies every import across the build list — the resolution
// root included, so a module's own files satisfy its imports. An import
// is satisfied by the well-known set (checked first: well-known paths
// are the toolchain's, never looked up in modules) or by any module
// providing the file; the failure reports every unsatisfied import
// sorted by module, file, then import, independent of input order —
// REQ-resolve-unsatisfied-imports pins the report as exhaustive and
// deterministically ordered.
func Check(modules []Module) error { return CheckRequirers(modules, modules) }

// CheckRequirers verifies the requirers' imports against every
// module's files: what a module compiled alone, the rest resolving
// its imports, needs (check-rules.md REQ-break-base-materialized).
func CheckRequirers(modules, requirers []Module) error {
	provided := make(map[string]bool)
	for _, m := range modules {
		for f := range m.Files {
			provided[f] = true
		}
	}
	var missing []Unsatisfied
	for _, m := range requirers {
		for f, imports := range m.Files {
			for _, imp := range imports {
				if modfiles.WellKnown(imp) || provided[imp] {
					continue
				}
				missing = append(missing, Unsatisfied{Module: m.Path, File: f, Import: imp})
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	slices.SortFunc(missing, func(a, b Unsatisfied) int {
		if c := strings.Compare(a.Module, b.Module); c != 0 {
			return c
		}
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		return strings.Compare(a.Import, b.Import)
	})
	return &UnsatisfiedError{Unsatisfied: slices.Compact(missing)}
}
