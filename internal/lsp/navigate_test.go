package lsp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-git/go-billy/v6/util"

	"github.com/greatliontech/lsp/protocol"
	"github.com/greatliontech/lsp/uri"
	"pgregory.net/rapid"
)

// navTree is the workspace navigation is judged over: a commented
// message, a service, an option and an enum value reference, so every
// kind of binding the spec lists has a token; c.proto, in proto2,
// holds the rest of the option surface — a file option, a message
// literal with a group, an extension and an Any, a dotted extension
// name, a field's default, an extension range's option — and an
// extension name declared at two scopes, resolved as the compiler
// resolves it: a message's own options in the enclosing scope, what
// the message holds in its own.
func navTree() map[string]string {
	files := checkTree()
	// The set rule over the message count would fire on the fuller
	// tree; navigation is judged under the field rule alone.
	files["ws/house/house.rules.yaml"] = strings.SplitAfter(houseRules, "field names are snake_case\n")[0]
	files["ws/a/a.proto"] = "syntax = \"proto3\";\npackage a;\nimport \"std.proto\";\nimport \"google/protobuf/descriptor.proto\";\n\n// Thing is a thing.\n// It has a name.\nmessage Thing {\n  string BadName = 1;\n  std.S s = 2;\n  Kind kind = 3;\n  map<string, std.S> by_name = 4;\n}\n\nenum Kind {\n  KIND_UNSPECIFIED = 0;\n  KIND_ONE = 1;\n}\n\nmessage Other {\n  option deprecated = true;\n  Kind k = 1 [deprecated = true];\n}\n\nextend google.protobuf.MessageOptions {\n  Kind kind_option = 50000;\n}\n\nmessage Third {\n  option (kind_option) = KIND_ONE;\n}\n\nservice Things {\n  rpc Get(Thing) returns (Other);\n}\n"
	// b.proto imports no descriptor.proto: its built-in option is typed
	// by the toolchain's.
	files["ws/b/b.proto"] = strings.Replace(files["ws/b/b.proto"], "import \"a.proto\";\n", "import \"a.proto\";\noption java_package = \"b\";\n", 1)
	files["ws/a/c.proto"] = "syntax = \"proto2\";\npackage a;\nimport \"a.proto\";\nimport \"google/protobuf/any.proto\";\nimport \"google/protobuf/descriptor.proto\";\noption java_package = \"a.java\";\n// *note*  \n// kept\nenum Mode {\n  MODE_A = 0;\n  MODE_B = 1;\n}\n\n/**\n * Meta is an option's message.\n *\n *     indented: kept\n */\nmessage Meta {\n  optional string label = 1;\n  optional Mode mode = 2;\n  optional google.protobuf.Any any = 3;\n  optional group Sub = 4 {\n    option deprecated = true;\n    optional Mode m = 1 [default = MODE_B];\n  }\n  optional Sub other = 5;\n  map<string, Mode> by = 6;\n}\n\nextend google.protobuf.MessageOptions {\n  optional Meta meta = 50001;\n}\n\nextend google.protobuf.ExtensionRangeOptions {\n  optional Mode range_mode = 50001;\n}\n\n// Modes:\n// * fast\n// * slow\nmessage Fourth {\n  option (meta) = {\n    mode: MODE_B\n    Sub { m: MODE_A }\n    any: { [type.googleapis.com/a.Other]: { k: KIND_ONE } }\n    by: { key: \"k\" value: MODE_A }\n  };\n  option (meta).label = \"z\";\n  optional Mode mode = 1 [default = MODE_B];\n  extensions 100 to 200 [(range_mode) = MODE_A];\n}\n\n/** Scoped is scoped. **/\nmessage Scoped {\n  extend google.protobuf.MessageOptions {\n    optional Mode kind_option = 50002;\n  }\n  option (kind_option) = KIND_ONE;\n  message Inner {\n    option (kind_option) = MODE_B;\n  }\n}\n"
	return files
}

// token locates the first occurrence of needle in a tree file, as a
// utf-16 position, offset by skip occurrences.
func (fx *fixture) token(t *testing.T, tree, needle string, skip int) protocol.Position {
	t.Helper()
	text := fx.text(t, tree)
	at := -1
	from := 0
	for i := 0; i <= skip; i++ {
		rel := strings.Index(string(text[from:]), needle)
		if rel < 0 {
			t.Fatalf("%s: %q not found (%d)", tree, needle, skip)
		}
		at = from + rel
		from = at + len(needle)
	}
	return position(text, at, utf16)
}

// Definition answers the declaration a name binds to as its name
// token's location: a message in another module's file, an enum, a
// dependency's message at its address, an option's extension, an
// import's path at the imported file's first line, a declaration's
// own name as itself; nothing bound answers null (REQ-lsp-definition,
// REQ-lsp-dependency-files).
func TestDefinition(t *testing.T) {
	for _, content := range []bool{true, false} {
		t.Run(fmt.Sprintf("content=%v", content), func(t *testing.T) {
			fx := newFixture(t, navTree())
			fx.pin(t)
			fx.start(t)
			caps := protocol.ClientCapabilities{}
			if content {
				caps.Workspace = &protocol.WorkspaceClientCapabilities{TextDocumentContent: &protocol.TextDocumentContentClientCapabilities{}}
			}
			res := fx.initialize(t, caps)
			if res.Capabilities.DefinitionProvider != protocol.Boolean(true) || res.Capabilities.HoverProvider != protocol.Boolean(true) || res.Capabilities.ReferencesProvider != protocol.Boolean(true) {
				t.Fatalf("the navigation capabilities: %#v", res.Capabilities)
			}
			fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto")
			fx.open(t, "ws/a/a.proto", 1, string(fx.text(t, "ws/a/a.proto")))
			fx.open(t, "ws/b/b.proto", 1, string(fx.text(t, "ws/b/b.proto")))
			// One wait for both: a judgement publishes its set in no
			// fixed order, and a wait for one swallows the other.
			fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto")
			define := func(tree string, pos protocol.Position) *protocol.Location {
				t.Helper()
				got, err := fx.server.Definition(context.Background(), &protocol.DefinitionParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri(tree)}, Position: pos}})
				if err != nil {
					t.Fatalf("definition: %v", err)
				}
				if got == nil {
					return nil
				}
				loc, ok := got.(*protocol.Location)
				if !ok {
					t.Fatalf("the answer's shape: %T", got)
				}
				return loc
			}
			// b.proto's field type a.Thing → a.proto's message name.
			loc := define("ws/b/b.proto", fx.token(t, "ws/b/b.proto", "a.Thing", 0))
			want := fx.token(t, "ws/a/a.proto", "Thing", 1) // the declaration, after the comment's mention
			if loc == nil || loc.URI != fx.uri("ws/a/a.proto") || loc.Range.Start != want || loc.Range.End.Character != want.Character+5 {
				t.Fatalf("the message's definition: %+v, want %v", loc, want)
			}
			// A field's enum type, an option's extension, an enum value
			// in an option, a method's input: each its declaration.
			// Each needle's name token sits past the syntax before it
			// (the paren of an option's extension, the "Get(" of an rpc).
			for needle, decl := range map[string]string{"Kind kind = 3": "Kind {", "(kind_option)": "kind_option", "KIND_ONE;": "KIND_ONE = 1", "Get(Thing)": "Thing {"} {
				pos := fx.token(t, "ws/a/a.proto", needle, 0)
				pos.Character += uint32(map[string]int{"(kind_option)": 1, "Get(Thing)": 4}[needle])
				loc := define("ws/a/a.proto", pos)
				wantPos := fx.token(t, "ws/a/a.proto", decl, 0)
				if loc == nil || loc.URI != fx.uri("ws/a/a.proto") || loc.Range.Start != wantPos {
					t.Fatalf("%s: %+v, want %v", needle, loc, wantPos)
				}
			}
			// A dependency's message at its address, the file's first line
			// for the import.
			loc = define("ws/a/a.proto", fx.token(t, "ws/a/a.proto", "std.S s", 0))
			dep := uri.MustParse("pb-module://example.com/std@v1.0.0/std.proto")
			if !content {
				dep = uri.File(fx.stdStore(t, "std.proto"))
			}
			if loc == nil || loc.URI != dep || loc.Range.Start != (protocol.Position{Line: 2, Character: 8}) {
				t.Fatalf("the dependency's definition: %+v, want %s 2:8", loc, dep)
			}
			loc = define("ws/a/a.proto", fx.token(t, "ws/a/a.proto", "\"std.proto\"", 0))
			if loc == nil || loc.URI != dep || loc.Range.Start != (protocol.Position{}) {
				t.Fatalf("the import's definition: %+v", loc)
			}
			// A well-known type's declaration, under its own authority.
			loc = define("ws/a/a.proto", fx.token(t, "ws/a/a.proto", "google.protobuf.MessageOptions", 0))
			wk := fx.srv.wellKnownURI("google/protobuf/descriptor.proto")
			if !content {
				wk = uri.File(filepath.Join(fx.srv.wellKnown.dir, "google", "protobuf", "descriptor.proto"))
			}
			if loc == nil || loc.URI != wk || loc.Range.Start.Line == 0 {
				t.Fatalf("the well-known definition: %+v, want %s", loc, wk)
			}
			// A built-in option of a file importing no descriptor.proto.
			loc = define("ws/b/b.proto", fx.token(t, "ws/b/b.proto", "java_package", 0))
			if loc == nil || loc.URI != wk {
				t.Fatalf("the built-in option's definition: %+v, want %s", loc, wk)
			}
			// A declaration's own name is itself; a keyword binds nothing.
			if loc := define("ws/a/a.proto", want); loc == nil || loc.Range.Start != want {
				t.Fatalf("the declaration over itself: %+v", loc)
			}
			if loc := define("ws/a/a.proto", fx.token(t, "ws/a/a.proto", "message Thing", 0)); loc != nil {
				t.Fatalf("a keyword bound: %+v", loc)
			}
			// A position just past a name's last character is over the
			// name; one further is not.
			past := want
			past.Character += 5
			if loc := define("ws/a/a.proto", past); loc == nil || loc.Range.Start != want {
				t.Fatalf("the position past the name: %+v", loc)
			}
			past.Character++
			if loc := define("ws/a/a.proto", past); loc != nil {
				t.Fatalf("the position past the space bound: %+v", loc)
			}
		})
	}
}

// Every kind of option binding leads to its declaration: a file
// option's plain name to the options message's field, a message
// literal's field, group — by its type name, as the text format
// spells it — extension and Any type reference, a dotted extension
// name's parts, a field's default and an extension range's option to
// their values; an extension name declared at two scopes resolves as
// the compiler resolves it (REQ-lsp-definition).
func TestDefinitionOptions(t *testing.T) {
	fx := newFixture(t, navTree())
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{Workspace: &protocol.WorkspaceClientCapabilities{TextDocumentContent: &protocol.TextDocumentContentClientCapabilities{}}})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto")
	fx.open(t, "ws/a/c.proto", 1, string(fx.text(t, "ws/a/c.proto")))
	fx.waitStanding(t, "ws/a/c.proto") // clean, it publishes nothing
	define := func(pos protocol.Position) *protocol.Location {
		t.Helper()
		got, err := fx.server.Definition(context.Background(), &protocol.DefinitionParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri("ws/a/c.proto")}, Position: pos}})
		if err != nil {
			t.Fatalf("definition: %v", err)
		}
		loc, _ := got.(*protocol.Location)
		return loc
	}
	// needle and the offset into it of the name; the declaration's
	// needle in c.proto, else in the named file.
	for _, tc := range []struct {
		needle string
		at     int
		file   string
		decl   string
	}{
		{"java_package = ", 0, "google/protobuf/descriptor.proto", "java_package = 1"},
		{"mode: MODE_B", 0, "", "mode = 2"},
		{"mode: MODE_B", 6, "", "MODE_B = 1"},
		{"Sub { m: MODE_A }", 0, "", "Sub = 4"},
		{"Sub { m: MODE_A }", 6, "", "m = 1"},
		{"Sub { m: MODE_A }", 9, "", "MODE_A = 0"},
		{"any: {", 0, "", "any = 3"},
		{"value: MODE_A", 7, "", "MODE_A = 0"},
		{"Sub other", 0, "", "Sub = 4"},
		{"[type.googleapis.com/a.Other]", 21, "ws/a/a.proto", "Other {"}, // the name past the URL prefix
		{"{ k: KIND_ONE }", 2, "ws/a/a.proto", "k = 1"},
		{"{ k: KIND_ONE }", 5, "ws/a/a.proto", "KIND_ONE = 1"},
		{"(meta).label", 1, "", "meta = 50001"},
		{"(meta).label", 7, "", "label = 1"},
		{"[default = MODE_B];\n  extensions", 11, "", "MODE_B = 1"},
		{"[(range_mode) = MODE_A]", 2, "", "range_mode = 50001"},
		{"[(range_mode) = MODE_A]", 16, "", "MODE_A = 0"},
		{"(kind_option) = KIND_ONE", 1, "ws/a/a.proto", "kind_option = 50000"},
		{"(kind_option) = MODE_B", 1, "", "kind_option = 50002"},
		{"option deprecated = true;\n    optional", 7, "google/protobuf/descriptor.proto", "deprecated = 3"},
	} {
		pos := fx.token(t, "ws/a/c.proto", tc.needle, 0)
		pos.Character += uint32(tc.at)
		loc := define(pos)
		if loc == nil {
			t.Errorf("%s+%d: nothing bound", tc.needle, tc.at)
			continue
		}
		wantURI, wantPos := fx.uri("ws/a/c.proto"), protocol.Position{}
		switch tc.file {
		case "":
			wantPos = fx.token(t, "ws/a/c.proto", tc.decl, 0)
		case "google/protobuf/descriptor.proto":
			wantURI = fx.srv.wellKnownURI(tc.file)
			text := fx.srv.wellKnown.files[tc.file]
			wantPos = position(text, strings.Index(string(text), tc.decl), utf16)
		default:
			wantURI, wantPos = fx.uri(tc.file), fx.token(t, tc.file, tc.decl, 0)
		}
		if loc.URI != wantURI || loc.Range.Start != wantPos {
			t.Errorf("%s+%d: %s@%v, want %s@%v", tc.needle, tc.at, loc.URI, loc.Range.Start, wantURI, wantPos)
		}
	}
	// A map entry's field is declared nowhere: its name binds nothing.
	pos := fx.token(t, "ws/a/c.proto", "key: ", 0)
	if loc := define(pos); loc != nil {
		t.Fatalf("the map entry's key bound: %+v", loc)
	}
	// The group's token declares its field and its message: over the
	// token, the field; the message's references, from a field typed
	// by it, include the token as its declaration when asked.
	refs := func(needle string, at int) []protocol.Location {
		t.Helper()
		pos := fx.token(t, "ws/a/c.proto", needle, 0)
		pos.Character += uint32(at)
		got, err := fx.server.References(context.Background(), &protocol.ReferenceParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri("ws/a/c.proto")}, Position: pos}, Context: protocol.ReferenceContext{IncludeDeclaration: true}})
		if err != nil {
			t.Fatalf("references: %v", err)
		}
		return got
	}
	decl := fx.token(t, "ws/a/c.proto", "Sub = 4", 0)
	if got := refs("Sub other", 0); len(got) != 2 || got[0].Range.Start != decl || got[1].Range.Start != fx.token(t, "ws/a/c.proto", "Sub other", 0) {
		t.Fatalf("the group message's references: %+v", got)
	}
	if got := refs("Sub = 4", 0); len(got) != 2 || got[0].Range.Start != decl || got[1].Range.Start != fx.token(t, "ws/a/c.proto", "Sub { m", 0) {
		t.Fatalf("the group field's references: %+v", got)
	}
	// A group's field hovers as the group it is, and a block comment
	// loses its margin, keeps its indentation.
	pos = fx.token(t, "ws/a/c.proto", "Sub = 4", 0)
	h, err := fx.server.Hover(context.Background(), &protocol.HoverParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri("ws/a/c.proto")}, Position: pos}})
	if err != nil || h == nil || h.Contents.(*protocol.MarkupContent).Value != "```protobuf\ngroup a.Meta.sub\n```" {
		t.Fatalf("the group's hover: %v %v", h, err)
	}
	// What the markers leave behind goes, what is written stays: a
	// `//` list's bullets and emphasis, a hard break's trailing space.
	for needle, want := range map[string]string{
		"Meta {":   "message a.Meta\n```\n\nMeta is an option's message.\n\n    indented: kept",
		"Scoped {": "message a.Scoped\n```\n\nScoped is scoped.",
		"Fourth {": "message a.Fourth\n```\n\nModes:\n* fast\n* slow",
		"Mode {":   "enum a.Mode\n```\n\n*note*  \nkept",
	} {
		pos = fx.token(t, "ws/a/c.proto", needle, 0)
		h, err = fx.server.Hover(context.Background(), &protocol.HoverParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri("ws/a/c.proto")}, Position: pos}})
		if err != nil || h == nil || h.Contents.(*protocol.MarkupContent).Value != "```protobuf\n"+want {
			t.Fatalf("%s's hover: %q %v", needle, h.Contents.(*protocol.MarkupContent).Value, err)
		}
	}
}

// Hover answers the declaration's kind and full name in a fenced
// protobuf block, then its leading comments with the markers
// stripped, the range the token under the position; nothing bound
// answers null (REQ-lsp-hover).
func TestHover(t *testing.T) {
	fx := newFixture(t, navTree())
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto")
	fx.open(t, "ws/b/b.proto", 1, string(fx.text(t, "ws/b/b.proto")))
	fx.publishes(t, "ws/b/b.proto")
	pos := fx.token(t, "ws/b/b.proto", "a.Thing", 0)
	h, err := fx.server.Hover(context.Background(), &protocol.HoverParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri("ws/b/b.proto")}, Position: pos}})
	if err != nil || h == nil {
		t.Fatalf("hover: %v %v", h, err)
	}
	md, ok := h.Contents.(*protocol.MarkupContent)
	if !ok || md.Kind != protocol.MarkupKindMarkdown || md.Value != "```protobuf\nmessage a.Thing\n```\n\nThing is a thing.\nIt has a name." {
		t.Fatalf("the hover's contents: %#v", h.Contents)
	}
	if h.Range == nil || h.Range.Start != pos || h.Range.End.Character != pos.Character+7 {
		t.Fatalf("the hover's range: %+v, want %v..+7", h.Range, pos)
	}
	// A qualified name's range is the name as written.
	pos = fx.token(t, "ws/a/a.proto", "std.S s", 0)
	fx.open(t, "ws/a/a.proto", 1, string(fx.text(t, "ws/a/a.proto")))
	fx.publishes(t, "ws/a/a.proto")
	h, err = fx.server.Hover(context.Background(), &protocol.HoverParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri("ws/a/a.proto")}, Position: pos}})
	if err != nil || h == nil || h.Range == nil || h.Range.Start != pos || h.Range.End.Character != pos.Character+5 {
		t.Fatalf("the qualified name's range: %v %v", h, err)
	}
	// A declaration without comments: the block alone.
	pos = fx.token(t, "ws/b/b.proto", "Use {", 0)
	h, err = fx.server.Hover(context.Background(), &protocol.HoverParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri("ws/b/b.proto")}, Position: pos}})
	if err != nil || h == nil || h.Contents.(*protocol.MarkupContent).Value != "```protobuf\nmessage b.Use\n```" {
		t.Fatalf("the uncommented hover: %v %v", h, err)
	}
	h, err = fx.server.Hover(context.Background(), &protocol.HoverParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri("ws/b/b.proto")}, Position: protocol.Position{}}})
	if err != nil || h != nil {
		t.Fatalf("a hover over nothing bound: %v %v", h, err)
	}
}

// References answers every reference to the bound declaration across
// the build's files, the declaration itself where asked, in URI then
// position order; over a dependency's declaration the references
// among the build files; nothing bound answers the empty list
// (REQ-lsp-references).
func TestReferences(t *testing.T) {
	fx := newFixture(t, navTree())
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{Workspace: &protocol.WorkspaceClientCapabilities{TextDocumentContent: &protocol.TextDocumentContentClientCapabilities{}}})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto")
	fx.open(t, "ws/a/a.proto", 1, string(fx.text(t, "ws/a/a.proto")))
	fx.publishes(t, "ws/a/a.proto")
	refs := func(tree string, pos protocol.Position, include bool) []protocol.Location {
		t.Helper()
		got, err := fx.server.References(context.Background(), &protocol.ReferenceParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri(tree)}, Position: pos}, Context: protocol.ReferenceContext{IncludeDeclaration: include}})
		if err != nil {
			t.Fatalf("references: %v", err)
		}
		return got
	}
	spell := func(locs []protocol.Location) string {
		var parts []string
		for _, l := range locs {
			parts = append(parts, fmt.Sprintf("%s@%d:%d", strings.TrimPrefix(string(l.URI), string(fx.uri("ws"))+"/"), l.Range.Start.Line, l.Range.Start.Character))
		}
		return strings.Join(parts, " ")
	}
	// Thing: referenced by its own field types? no — by b.proto's field
	// and the rpc's input; the declaration where asked.
	decl := fx.token(t, "ws/a/a.proto", "Thing {", 0)
	got := refs("ws/a/a.proto", decl, false)
	rpc := fx.token(t, "ws/a/a.proto", "Get(Thing)", 0)
	want := fmt.Sprintf("a/a.proto@%d:%d b/b.proto@%d:%d", rpc.Line, rpc.Character+4, fx.token(t, "ws/b/b.proto", "a.Thing", 0).Line, fx.token(t, "ws/b/b.proto", "a.Thing", 0).Character)
	if spell(got) != want {
		t.Fatalf("Thing's references: %s, want %s", spell(got), want)
	}
	got = refs("ws/a/a.proto", decl, true)
	if len(got) != 3 || got[0].Range.Start != decl {
		t.Fatalf("Thing's references with the declaration: %s", spell(got))
	}
	// Kind: two field types, an option's value is its enum value's, not
	// the enum's; from a reference, the same answer.
	kindRef := fx.token(t, "ws/a/a.proto", "Kind kind = 3", 0)
	got = refs("ws/a/a.proto", kindRef, false)
	if len(got) != 3 {
		t.Fatalf("Kind's references: %s", spell(got))
	}
	// A dependency's declaration, referenced from the build files.
	got = refs("ws/a/a.proto", fx.token(t, "ws/a/a.proto", "std.S s", 0), true)
	if len(got) != 3 || got[0].URI != fx.uri("ws/a/a.proto") || got[1].URI != fx.uri("ws/a/a.proto") || !strings.HasPrefix(string(got[2].URI), "pb-module://") || got[2].Range.Start != (protocol.Position{Line: 2, Character: 8}) {
		t.Fatalf("std.S's references: %s", spell(got))
	}
	if got := refs("ws/a/a.proto", protocol.Position{}, true); len(got) != 0 {
		t.Fatalf("references of nothing bound: %s", spell(got))
	}
}

// Where the build does not compile, navigation answers from the last
// build that compiled for a document whose contents are those that
// build read, and the empty answer for one that changed; a document
// outside the build and a dependency's copy answer empty
// (REQ-lsp-definition, REQ-lsp-outside).
func TestNavigationWhileBroken(t *testing.T) {
	fx := newFixture(t, navTree())
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto")
	fx.open(t, "ws/a/a.proto", 1, string(fx.text(t, "ws/a/a.proto")))
	fx.open(t, "ws/b/b.proto", 1, string(fx.text(t, "ws/b/b.proto")))
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto")
	define := func(tree string, pos protocol.Position) protocol.DefinitionResult {
		t.Helper()
		got, err := fx.server.Definition(context.Background(), &protocol.DefinitionParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri(tree)}, Position: pos}})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// a.proto breaks: b.proto, unchanged, still navigates from the last
	// compiling build; a.proto, changed, does not.
	fx.change(t, "ws/a/a.proto", 2, strings.Replace(string(fx.text(t, "ws/a/a.proto")), "string BadName = 1;", "string BadName = 1", 1))
	p := fx.publishes(t, "ws/a/a.proto")[fx.uri("ws/a/a.proto")]
	if len(p.Diagnostics) != 1 || p.Diagnostics[0].Code != protocol.String("compile") {
		t.Fatalf("the broken build: %s", render(p))
	}
	if got := define("ws/b/b.proto", fx.token(t, "ws/b/b.proto", "a.Thing", 0)); got == nil {
		t.Fatal("the unchanged document lost navigation under a broken build")
	}
	if got := define("ws/a/a.proto", fx.token(t, "ws/a/a.proto", "std.S s", 0)); got != nil {
		t.Fatalf("the changed document navigated under a broken build: %+v", got)
	}
	// Mended, both navigate again.
	fx.change(t, "ws/a/a.proto", 3, string(fx.text(t, "ws/a/a.proto")))
	fx.publishes(t, "ws/a/a.proto")
	if got := define("ws/a/a.proto", fx.token(t, "ws/a/a.proto", "std.S s", 0)); got == nil {
		t.Fatal("the mended document does not navigate")
	}
	// Outside the build: empty.
	fx.open(t, "elsewhere/x.proto", 1, "syntax = \"proto3\";\nimport \"a.proto\";\n")
	fx.publishes(t, "elsewhere/x.proto")
	if got := define("elsewhere/x.proto", protocol.Position{Line: 1, Character: 8}); got != nil {
		t.Fatalf("an outside document navigated: %+v", got)
	}
	refs, err := fx.server.References(context.Background(), &protocol.ReferenceParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri("elsewhere/x.proto")}, Position: protocol.Position{Line: 1, Character: 8}}})
	if err != nil || len(refs) != 0 {
		t.Fatalf("an outside document's references: %v %v", refs, err)
	}
}

// Positions on the wire are converted under the encoding the client
// offered: with utf-8 units a token after a multi-byte character is
// found at its byte column (REQ-lsp-positions).
func TestNavigationUnderUTF8(t *testing.T) {
	files := navTree()
	files["ws/b/b.proto"] = "syntax = \"proto3\";\npackage b;\nimport \"a.proto\";\nmessage Use {\n  /* éééééééééé 𐐀 */ a.Thing thing = 1;\n}\n"
	fx := newFixture(t, files)
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{General: &protocol.GeneralClientCapabilities{PositionEncodings: []protocol.PositionEncodingKind{protocol.PositionEncodingKindUTF8}}})
	fx.publishes(t, "ws/a/a.proto")
	fx.open(t, "ws/b/b.proto", 1, files["ws/b/b.proto"])
	fx.waitStanding(t, "ws/b/b.proto")
	text := []byte(files["ws/b/b.proto"])
	pos := position(text, strings.Index(files["ws/b/b.proto"], "a.Thing"), utf8e)
	define := func(pos protocol.Position) *protocol.Location {
		t.Helper()
		got, err := fx.server.Definition(context.Background(), &protocol.DefinitionParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri("ws/b/b.proto")}, Position: pos}})
		if err != nil {
			t.Fatalf("definition: %v", err)
		}
		loc, _ := got.(*protocol.Location)
		return loc
	}
	want := fx.token(t, "ws/a/a.proto", "Thing {", 0)
	if loc := define(pos); loc == nil || loc.URI != fx.uri("ws/a/a.proto") || loc.Range.Start != want {
		t.Fatalf("definition under utf-8: %+v, want %v", loc, want)
	}
	// The same column counted in utf-16 units lands past the name:
	// the fixture tells the encodings apart.
	utf16Pos := position(text, strings.Index(files["ws/b/b.proto"], "a.Thing"), utf16)
	if utf16Pos == pos {
		t.Fatal("the fixture does not tell the encodings apart")
	}
	if loc := define(utf16Pos); loc != nil {
		t.Fatalf("the utf-16 column bound under utf-8: %+v", loc)
	}
}

// offsetAt inverts position under every encoding: for any text with
// the protocol's three line endings, multi-byte and supplementary
// characters, every offset not inside a character round-trips
// (REQ-lsp-positions).
func TestOffsetAtRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		pieces := rapid.SliceOfN(rapid.SampledFrom([]string{"a", "é", "𐐀", "\n", "\r\n", "\r", "\t", " "}), 0, 24).Draw(rt, "pieces")
		text := []byte(strings.Join(pieces, ""))
		enc := rapid.SampledFrom([]encoding{utf16, utf8e, utf32}).Draw(rt, "enc")
		for off := 0; off <= len(text); off++ {
			// Inside a character, or inside the `\r\n` pair — one line
			// ending — is no position.
			if off < len(text) && (!utf8.RuneStart(text[off]) || off > 0 && text[off] == '\n' && text[off-1] == '\r') {
				continue
			}
			pos := position(text, off, enc)
			if got := offsetAt(text, pos, enc); got != off {
				rt.Fatalf("%q under %v: offset %d → %v → %d", text, enc, off, pos, got)
			}
		}
	})
}

// Every span of the index names what it says: a declaration's token
// spells the declaration's own name, a reference's token ends with
// the target's name or spells a field it binds, an import's token
// spells the path — over every file of a build with every kind of
// binding (the index's entailed invariant).
func TestIndexSpansName(t *testing.T) {
	fx := newFixture(t, navTree())
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto")
	fx.srv.mu.Lock()
	idx := fx.srv.index
	fx.srv.mu.Unlock()
	if idx == nil {
		t.Fatal("no index kept")
	}
	seen := 0
	for path, fi := range idx.spans {
		text := idx.build.byPath[path].text
		for _, sp := range fi.spans {
			seen++
			tok := string(text[sp.start:sp.end])
			switch {
			case sp.importPath != "":
				if strings.Trim(tok, "\"'") != sp.importPath {
					t.Errorf("%s: the import token %q, the path %s", path, tok, sp.importPath)
				}
			default:
				d := idx.decls[sp.target]
				if d == nil {
					t.Errorf("%s: %q binds %s, which no file declares", path, tok, sp.target)
					continue
				}
				name := string(sp.target.Name())
				// A group's field is named by its lowercased group.
				if d.kind == "group" {
					tok = strings.ToLower(tok)
				}
				if sp.decl && tok != name {
					t.Errorf("%s: the declaration token %q, the name %s", path, tok, name)
				}
				if last := tok[strings.LastIndexAny(tok, "./")+1:]; !sp.decl && last != name {
					t.Errorf("%s: the reference token %q, the target %s", path, tok, sp.target)
				}
			}
		}
	}
	if seen < 30 {
		t.Fatalf("the index holds %d spans; the fixture should give far more", seen)
	}
	// The toolchain's descriptor file is indexed too.
	if _, ok := idx.spans["google/protobuf/descriptor.proto"]; !ok {
		t.Fatal("the well-known file is not indexed")
	}
}

// An address navigation handed out is served by the content request
// while the index stands, whatever the last judgement's modules: the
// module file drops the dependency, the build no longer compiles,
// the unchanged document still navigates to the dependency's address
// from the last build that compiled, and its content is the bytes
// that build read (REQ-lsp-dependency-files, REQ-lsp-definition).
func TestDependencyContentOutlivesTheBuild(t *testing.T) {
	fx := newFixture(t, navTree())
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{Workspace: &protocol.WorkspaceClientCapabilities{TextDocumentContent: &protocol.TextDocumentContentClientCapabilities{}}})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto")
	fx.open(t, "ws/a/a.proto", 1, string(fx.text(t, "ws/a/a.proto")))
	fx.publishes(t, "ws/a/a.proto")
	define := func() uri.URI {
		t.Helper()
		got, err := fx.server.Definition(context.Background(), &protocol.DefinitionParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri("ws/a/a.proto")}, Position: fx.token(t, "ws/a/a.proto", "std.S s", 0)}})
		if err != nil || got == nil {
			t.Fatalf("definition: %v %v", got, err)
		}
		return got.(*protocol.Location).URI
	}
	dep := define()
	// The module file drops the dependency: the build loads without
	// it and no longer compiles.
	if err := util.WriteFile(fx.ws, "ws/a/pb.yaml", []byte(ws("example.com/a", "")), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.watched(t, "ws/a/pb.yaml", protocol.FileChangeTypeChanged)
	if p := fx.publishes(t, "ws/a/a.proto")[fx.uri("ws/a/a.proto")]; len(p.Diagnostics) == 0 || p.Diagnostics[0].Code != protocol.String("compile") {
		t.Fatalf("the build without the dependency: %s", render(p))
	}
	if got := define(); got != dep {
		t.Fatalf("the address after the build went: %s, want %s", got, dep)
	}
	res, err := fx.server.TextDocumentContent(context.Background(), &protocol.TextDocumentContentParams{URI: dep})
	if err != nil || !strings.Contains(res.Text, "message S") {
		t.Fatalf("the dependency's content after the build went: %v %v", res, err)
	}
}
