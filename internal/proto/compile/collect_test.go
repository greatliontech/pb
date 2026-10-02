package compile

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/greatliontech/pb/internal/proto/modfiles"
)

// CompileAll collects every error the compiler reports over the
// build — a syntax error in one file, an unsatisfied import in
// another at its import statement, an unknown type in a dependency's
// file — each located, and compiles a sound build as Compile does
// (lsp.md REQ-lsp-diagnostics).
func TestCompileAllCollects(t *testing.T) {
	ctx := context.Background()
	mods := []modfiles.Module{
		{Path: "example.com/a", Local: true, Dir: "a", Files: map[string][]byte{
			"a.proto": []byte("syntax = \"proto3\";\npackage a;\nmessage A {\n  string x = 1\n}\n"),
			"b.proto": []byte("syntax = \"proto3\";\npackage a;\nimport \"missing.proto\";\nimport \"d.proto\";\nmessage B {}\n"),
			"c.proto": []byte("syntax = \"proto3\";\npackage a;\nimport \"d.proto\";\nmessage C {\n  d.D d = 1;\n}\n"),
		}},
		{Path: "example.com/d", Version: "v1.0.0", Files: map[string][]byte{
			"d.proto": []byte("syntax = \"proto3\";\npackage d;\nmessage D {\n  Nope n = 1;\n}\n"),
		}},
	}
	_, err := CompileAll(ctx, mods)
	var errs *Errors
	if !errors.As(err, &errs) {
		t.Fatalf("CompileAll: %v", err)
	}
	seen := map[string]Error{}
	for _, e := range errs.List {
		seen[e.Path] = e
	}
	if len(errs.List) != 3 {
		t.Fatalf("errors collected: %v", errs)
	}
	if e := seen["a.proto"]; e.Line != 5 || e.Offset == 0 {
		t.Fatalf("the syntax error: %+v", e)
	}
	if e := seen["b.proto"]; e.Line != 3 || e.Column != 8 || e.End <= e.Offset || !strings.Contains(e.Message, "missing.proto") {
		t.Fatalf("the unsatisfied import, at its statement with a span: %+v", e)
	}
	if e := seen["d.proto"]; e.Line != 4 || e.Column != 3 {
		t.Fatalf("the dependency's error, reached through the file whose imports are satisfied: %+v", e)
	}
	// The same build under the verb's compile stops at its first.
	if _, err := Compile(ctx, mods); err == nil {
		t.Fatal("Compile accepted the build")
	}

	mods[0].Files["a.proto"] = []byte("syntax = \"proto3\";\npackage a;\nmessage A {\n  string x = 1;\n}\n")
	mods[0].Files["b.proto"] = []byte("syntax = \"proto3\";\npackage a;\nimport \"d.proto\";\nmessage B {\n  d.D d = 1;\n}\n")
	mods[1].Files["d.proto"] = []byte("syntax = \"proto3\";\npackage d;\nmessage D {}\n")
	res, err := CompileAll(ctx, mods)
	if err != nil || len(res.Files) != 3 {
		t.Fatalf("a sound build: %v, %v", res, err)
	}

	// Ambiguous providers fail before anything compiles, an error
	// naming no file; an unsatisfied import found first stays named
	// beside it, as the verb names it (REQ-lsp-parity).
	mods = append(mods, modfiles.Module{Path: "example.com/e", Version: "v1.0.0", Files: map[string][]byte{"d.proto": []byte("syntax = \"proto3\";\n")}})
	mods[0].Files["b.proto"] = []byte("syntax = \"proto3\";\npackage a;\nimport \"gone.proto\";\nmessage B {}\n")
	_, err = CompileAll(ctx, mods)
	if !errors.As(err, &errs) || len(errs.List) != 2 || errs.List[0].Path != "" || !strings.Contains(errs.List[0].Message, "provided by") || errs.List[1].Path != "b.proto" || errs.List[1].Line != 3 {
		t.Fatalf("ambiguous providers beside an unsatisfied import: %v", err)
	}
	// A file that does not parse in a dependency no target imports is
	// reported all the same, as the verb's import read fails on it.
	mods = mods[:2]
	mods[0].Files["b.proto"] = []byte("syntax = \"proto3\";\npackage a;\nmessage B {}\n")
	mods[1].Files["junk.proto"] = []byte("syntax = \"proto3\";\nmessage {\n")
	_, err = CompileAll(ctx, mods)
	if !errors.As(err, &errs) || len(errs.List) != 1 || errs.List[0].Path != "junk.proto" || errs.List[0].Line != 2 {
		t.Fatalf("an unparsable dependency file no target imports: %v", err)
	}
	if _, err := Compile(ctx, mods); err == nil || !strings.Contains(err.Error(), "junk.proto") {
		t.Fatalf("the verb's compile over the same build: %v", err)
	}
	// Every syntax error of a file is reported, not its first alone.
	mods[1].Files["junk.proto"] = []byte("syntax = \"proto3\";\nmessage A {\n  string x = 1\n}\nmessage B {\n  string y = 2\n}\n")
	_, err = CompileAll(ctx, mods)
	if !errors.As(err, &errs) || len(errs.List) != 2 || errs.List[0].Line != 4 || errs.List[1].Line != 7 {
		t.Fatalf("a file's every syntax error: %v", err)
	}
}
