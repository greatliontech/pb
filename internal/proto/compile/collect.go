package compile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/ast"
	"github.com/bufbuild/protocompile/parser"
	"github.com/bufbuild/protocompile/reporter"
	"github.com/bufbuild/protocompile/wellknownimports"

	"github.com/greatliontech/pb/internal/proto/importcheck"
	"github.com/greatliontech/pb/internal/proto/modfiles"
)

// Error is one error the compiler reported, located where the
// compiler put it: the file's include-root-relative path, and its
// position — one-based line and column and the byte offset, End the
// offset past the token where the compiler knew one, else Offset —
// or none, where Line is zero and the error names the file alone; an
// error of the build as a whole, an ambiguous provider's, names no
// file.
type Error struct {
	Path                      string
	Line, Column, Offset, End int
	Message                   string
}

// Errors is every error a collecting compile reported, in file-path
// then position order, and the one error CompileAll returns for a
// build that does not compile.
type Errors struct {
	List []Error
}

func (e *Errors) Error() string {
	var b strings.Builder
	for i, err := range e.List {
		if i > 0 {
			b.WriteString("; ")
		}
		if err.Path != "" {
			b.WriteString(err.Path)
			if err.Line > 0 {
				fmt.Fprintf(&b, ":%d:%d", err.Line, err.Column)
			}
			b.WriteString(": ")
		}
		b.WriteString(err.Message)
	}
	return b.String()
}

// CompileAll compiles the workspace modules' files of a build as
// Compile does, but reports every error there is instead of the
// first (lsp.md REQ-lsp-diagnostics), in the verb's own stages so
// what the verb names is among them: every file of every module
// parsed, a file that does not parse reported at the parser's
// position; the import check's report whole, each unsatisfied import
// at the first statement importing that path in its file; the
// providers, an ambiguous one an error with no file; then the
// compiler's errors under a reporter that collects them, over the
// files whose import closure parses and is satisfied — a resolution
// failure ends the compiler's run, so a file that would hit one is
// left out and its own errors with it, what it needs fixed first
// being reported. Every other failure is the context's.
func CompileAll(ctx context.Context, mods []modfiles.Module) (*Result, error) {
	var collected Errors
	// The parse and the import read, per file over every module as
	// importcheck.Views reads them, a failure located and the file
	// kept as its module's with no imports, so an import of it is
	// satisfied and it stays off the compiler's targets.
	views := make([]importcheck.Module, 0, len(mods))
	broken := map[string]bool{}
	for _, m := range mods {
		v := importcheck.Module{Path: m.Path, Label: m.Label(), Files: map[string][]string{}}
		for _, p := range m.Protos() {
			imps, err := importcheck.Imports(p, m.Files[p])
			if err != nil {
				collected.List = append(collected.List, parseErrors(p, m.Files[p], err)...)
				broken[p] = true
				imps = nil
			}
			v.Files[p] = imps
		}
		views = append(views, v)
	}
	var ue *importcheck.UnsatisfiedError
	if err := importcheck.CheckRequirers(views, views); errors.As(err, &ue) {
		for _, u := range ue.Unsatisfied {
			collected.List = append(collected.List, importError(u.File, u.Import, fileOf(mods, u.File)))
			broken[u.File] = true
		}
	} else if err != nil {
		return nil, err
	}
	sources, err := providerIndex(mods)
	if err != nil {
		var amb *AmbiguousError
		if !errors.As(err, &amb) {
			return nil, err
		}
		collected.List = append(collected.List, Error{Message: amb.Error()})
		return nil, sorted(&collected)
	}
	imports := map[string][]string{}
	for _, v := range views {
		for file, imps := range v.Files {
			imports[file] = imps
		}
	}
	var targets []string
	memo := map[string]bool{}
	for _, m := range mods {
		if !m.Local {
			continue
		}
		for _, p := range m.Protos() {
			if !reaches(p, imports, broken, memo, map[string]bool{}) {
				targets = append(targets, p)
			}
		}
	}
	c := protocompile.Compiler{
		Resolver:       wellknownimports.WithStandardImports(byteResolver(sources)),
		SourceInfoMode: protocompile.SourceInfoStandard,
		// The syntax trees stay with the results: an editor navigates
		// them (lsp.md REQ-lsp-definition).
		RetainASTs: true,
		Reporter: reporter.NewReporter(func(err reporter.ErrorWithPos) error {
			collected.List = append(collected.List, located(err))
			return nil
		}, nil),
	}
	files, err := c.Compile(ctx, targets...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// An error the compiler returned without reporting it is
		// collected too, so the list is every error there is.
		var withPos reporter.ErrorWithPos
		switch {
		case errors.As(err, &withPos):
			if e := located(withPos); !reported(collected.List, e) {
				collected.List = append(collected.List, e)
			}
		case !errors.Is(err, reporter.ErrInvalidSource):
			collected.List = append(collected.List, Error{Message: err.Error()})
		}
	}
	if len(collected.List) == 0 {
		if err != nil {
			return nil, err
		}
		return &Result{Files: files}, nil
	}
	return nil, sorted(&collected)
}

// sorted orders the errors by file path then offset, the file-less
// ones first.
func sorted(e *Errors) *Errors {
	sort.SliceStable(e.List, func(a, b int) bool {
		x, y := e.List[a], e.List[b]
		if x.Path != y.Path {
			return x.Path < y.Path
		}
		return x.Offset < y.Offset
	})
	return e
}

// fileOf is a file's bytes by its include-root-relative path, the
// first module providing it.
func fileOf(mods []modfiles.Module, p string) []byte {
	for _, m := range mods {
		if b, ok := m.Files[p]; ok {
			return b
		}
	}
	return nil
}

// reaches reports whether file imports, transitively, a file among
// broken; memo holds the answers settled, seen guards the walk
// against cycles.
func reaches(file string, imports map[string][]string, broken, memo, seen map[string]bool) bool {
	if broken[file] {
		return true
	}
	if r, ok := memo[file]; ok {
		return r
	}
	if seen[file] {
		return false
	}
	seen[file] = true
	for _, imp := range imports[file] {
		if reaches(imp, imports, broken, memo, seen) {
			memo[file] = true
			return true
		}
	}
	memo[file] = false
	return false
}

// parseErrors is a file's parse failures located where the parser
// reports them, every one under a reporter that collects; the import
// read's error stands where the parser reports nothing located.
func parseErrors(file string, src []byte, readErr error) []Error {
	var errs []Error
	h := reporter.NewHandler(reporter.NewReporter(func(err reporter.ErrorWithPos) error {
		errs = append(errs, located(err))
		return nil
	}, nil))
	parser.Parse(file, bytes.NewReader(src), h)
	if len(errs) == 0 {
		return []Error{{Path: file, Message: readErr.Error()}}
	}
	return errs
}

// importError is an unsatisfied import's error at the first
// statement importing the path in the file, as the parser locates
// it; the file's first line where the statement cannot be found.
func importError(file, imp string, src []byte) Error {
	e := Error{Path: file, Message: fmt.Sprintf("import %q: no module of the build provides it", imp)}
	node, _ := parser.Parse(file, bytes.NewReader(src), reporter.NewHandler(reporter.NewReporter(func(reporter.ErrorWithPos) error { return nil }, nil)))
	if node == nil {
		return e
	}
	for _, decl := range node.Decls {
		in, ok := decl.(*ast.ImportNode)
		if !ok || in.Name == nil || in.Name.AsString() != imp {
			continue
		}
		info := node.NodeInfo(in.Name)
		start, end := info.Start(), info.End()
		e.Line, e.Column, e.Offset, e.End = start.Line, start.Col, start.Offset, end.Offset+1
		return e
	}
	return e
}

// located is the error as the compiler placed it: a position known
// where the line is, the message the wrapped error's.
func located(err reporter.ErrorWithPos) Error {
	pos := err.GetPosition()
	e := Error{Path: pos.Filename, Message: err.Unwrap().Error()}
	if pos.Line > 0 {
		e.Line, e.Column, e.Offset, e.End = pos.Line, pos.Col, pos.Offset, pos.Offset
		// The parser's End names the last byte; the error's is past it.
		if span, ok := err.(ast.SourceSpan); ok && span.End().Offset > pos.Offset {
			e.End = span.End().Offset + 1
		}
	}
	return e
}

// reported tells whether an error equal to e is in the list already.
func reported(list []Error, e Error) bool {
	for _, have := range list {
		if have == e {
			return true
		}
	}
	return false
}
