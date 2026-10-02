package dep

import (
	"go/ast"
	"go/types"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// Every read of a build-list pair's content goes through the resolve
// driver or the file sets' loader, which ask workspace.Root.Source
// what answers for the pair (REQ-work-replace, REQ-work-replace-dir):
// nothing else in the module uses the fetch client's content readers
// — Zip, RulesetZip, Module, Download, RulesetDownload — by call, method value or expression,
// through an embedded client, or through an interface naming one,
// in or outside a function; a use by reflection is outside any
// static rung. The driver's loader and Download read source
// pairs; Modules hands Zip to modfiles.Load, which applies the
// mapping; Breaking hands it to the breaking base, which reads a
// workspace module's own published versions, a path no replacement
// can name; prepare hands RulesetZip to lintfile.Rulesets, which
// reads each import through Root.Source, as closure (download, graph,
// why and tidy) and Update read the imports through the session's
// view of them. A ninth site is a reader the mapping does not reach.
func TestPairReadsGoThroughTheDriver(t *testing.T) {
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
	}, "github.com/greatliontech/pb/...")
	if err != nil {
		t.Fatal(err)
	}
	const client = "github.com/greatliontech/pb/internal/source/fetch"
	// The three readers are judged by the object used, whatever the
	// syntax reaching it: a call, a method value or expression, a
	// promoted method through an embedded client.
	reader := func(obj types.Object) bool {
		fn, ok := obj.(*types.Func)
		if !ok || fn.Pkg() == nil || fn.Pkg().Path() != client {
			return false
		}
		switch fn.Name() {
		case "Zip", "RulesetZip", "Module", "Download", "RulesetDownload":
		default:
			return false
		}
		recv := fn.Signature().Recv()
		if recv == nil {
			return false
		}
		ptr, ok := recv.Type().(*types.Pointer)
		if !ok {
			return false
		}
		named, ok := ptr.Elem().(*types.Named)
		return ok && named.Obj().Name() == "Client"
	}
	var sites []string
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			t.Fatalf("%s: %v", pkg.PkgPath, pkg.Errors[0])
		}
		if pkg.PkgPath == client {
			continue
		}
		for id, obj := range pkg.TypesInfo.Uses {
			if !reader(obj) {
				continue
			}
			// The site is named by its enclosing function, or as the
			// package's initialization when it sits in no function.
			where := "<init>"
			for _, f := range pkg.Syntax {
				if f.FileStart > id.Pos() || id.Pos() >= f.FileEnd {
					continue
				}
				for _, d := range f.Decls {
					if fn, ok := d.(*ast.FuncDecl); ok && fn.Pos() <= id.Pos() && id.Pos() < fn.End() {
						where = fn.Name.Name
					}
				}
			}
			sites = append(sites, strings.TrimPrefix(pkg.PkgPath, "github.com/greatliontech/pb/")+":"+where+"."+obj.Name())
		}
	}
	// An interface is the one static route past the object: a client
	// converted to an interface naming a reader with the client's
	// signature is used through the interface's method. No interface
	// type anywhere in the module — named, or spelled inline — names
	// one, so the conversion has no target.
	clientPkg := (*types.Package)(nil)
	for _, pkg := range pkgs {
		if pkg.PkgPath == client {
			clientPkg = pkg.Types
		}
	}
	if clientPkg == nil {
		t.Fatal("the fetch package was not loaded")
	}
	clientType := types.NewPointer(clientPkg.Scope().Lookup("Client").Type())
	namesReader := func(iface *types.Interface) string {
		for i := 0; i < iface.NumMethods(); i++ {
			m := iface.Method(i)
			switch m.Name() {
			case "Zip", "RulesetZip", "Module", "Download", "RulesetDownload":
			default:
				continue
			}
			if obj, _, _ := types.LookupFieldOrMethod(clientType, false, clientPkg, m.Name()); obj != nil {
				if types.Identical(obj.Type(), m.Type()) {
					return m.Name()
				}
			}
		}
		return ""
	}
	for _, pkg := range pkgs {
		if pkg.PkgPath == client {
			continue
		}
		for expr, tv := range pkg.TypesInfo.Types {
			if !tv.IsType() {
				continue
			}
			iface, ok := tv.Type.Underlying().(*types.Interface)
			if !ok {
				continue
			}
			if name := namesReader(iface); name != "" {
				sites = append(sites, strings.TrimPrefix(pkg.PkgPath, "github.com/greatliontech/pb/")+":interface@"+pkg.Fset.Position(expr.Pos()).String()+"."+name)
			}
		}
	}
	slices.Sort(sites)
	want := []string{"internal/dep:Breaking.Zip", "internal/dep:Download.RulesetDownload", "internal/dep:Modules.Zip", "internal/dep:Update.RulesetZip", "internal/dep:assembleRun.RulesetZip", "internal/dep:closure.RulesetZip", "internal/resolve:Download.Download", "internal/resolve:load.Module"}
	if !slices.Equal(sites, want) {
		t.Fatalf("fetch-client content readers used across the module: %v, want exactly %v — a build-list pair is read through the driver or modfiles.Load, never handed to the client", sites, want)
	}
}
