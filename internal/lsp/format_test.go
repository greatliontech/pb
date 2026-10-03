package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/util"

	"github.com/greatliontech/lsp/jsonrpc2"
	"github.com/greatliontech/lsp/protocol"
	"github.com/greatliontech/lsp/uri"

	"github.com/greatliontech/pb/internal/testing/fetchtest/assemble"
)

// unformatted is a.proto's text with its layout disturbed, and
// canonical the form the formatter gives it.
const (
	unformatted = "syntax=\"proto3\";\npackage a;\n\n\nimport \"std.proto\";\n\nmessage Thing{ string BadName=1;\n  std.S s = 2; }\n\nmessage Other{}\n"
	canonical   = "syntax = \"proto3\";\npackage a;\n\nimport \"std.proto\";\n\nmessage Thing {\n  string BadName = 1;\n  std.S s = 2;\n}\n\nmessage Other {}\n"
)

// Formatting answers one edit over the whole document carrying the
// canonical form where the form differs, nothing where it does not,
// an error naming the parse failure where the document does not
// parse; the request's options are ignored and the tree is never
// written; an own file is formatted whether or not it is a build
// file — a workspace copy of a well-known import — and whether or not
// a build is loaded; a document that is no own file — outside the
// root, in no member, in a nested module, a dependency's, not
// `.proto` — or not open is answered the empty answer
// (REQ-lsp-formatting, REQ-lsp-outside).
func TestFormatting(t *testing.T) {
	files := checkTree()
	files["elsewhere/x.proto"] = "syntax = \"proto3\";\n"
	files["ws/notes/n.proto"] = "syntax = \"proto3\";\n"
	files["ws/a/google/protobuf/empty.proto"] = "syntax = \"proto3\";\npackage google.protobuf;\nmessage Empty{}\n"
	files["ws/a/nested/pb.yaml"] = ws("example.com/nested", "")
	files["ws/a/nested/x.proto"] = "syntax = \"proto3\";\n"
	fx := newFixture(t, files)
	if err := fx.ws.Symlink("../elsewhere", "ws/a/link"); err != nil {
		t.Fatal(err)
	}
	// No pin: the session loads, the build does not (unpinned).
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{Workspace: &protocol.WorkspaceClientCapabilities{TextDocumentContent: &protocol.TextDocumentContentClientCapabilities{}}})
	fx.publishes(t, "ws/pb.work")
	formatting := func(u uri.URI, opts protocol.FormattingOptions) ([]protocol.TextEdit, error) {
		t.Helper()
		return fx.server.Formatting(context.Background(), &protocol.DocumentFormattingParams{TextDocument: protocol.TextDocumentIdentifier{URI: u}, Options: opts})
	}
	// Not open: the empty answer.
	if edits, err := formatting(fx.uri("ws/a/a.proto"), protocol.FormattingOptions{}); err != nil || len(edits) != 0 {
		t.Fatalf("a document not open: %v %v", edits, err)
	}
	before := string(fx.text(t, "ws/a/a.proto"))
	fx.open(t, "ws/a/a.proto", 1, unformatted)
	fx.publishes(t, "ws/a/a.proto")
	for _, opts := range []protocol.FormattingOptions{{TabSize: 2, InsertSpaces: true}, {TabSize: 8, InsertSpaces: false}} {
		edits, err := formatting(fx.uri("ws/a/a.proto"), opts)
		if err != nil || len(edits) != 1 {
			t.Fatalf("the unformatted document: %v %v", edits, err)
		}
		end := position([]byte(unformatted), len(unformatted), utf16)
		if edits[0].Range != (protocol.Range{End: end}) || edits[0].NewText != canonical {
			t.Fatalf("the edit: %+v, want 0:0..%v carrying %q", edits[0], end, canonical)
		}
	}
	if got := string(fx.text(t, "ws/a/a.proto")); got != before {
		t.Fatal("the tree's file was written")
	}
	// Formatted already: no edit. (An outside document's change is
	// not judged: nothing is published for it.)
	fx.change(t, "ws/a/a.proto", 2, canonical)
	if edits, err := formatting(fx.uri("ws/a/a.proto"), protocol.FormattingOptions{}); err != nil || len(edits) != 0 {
		t.Fatalf("the formatted document: %v %v", edits, err)
	}
	// Not parsing: the error names the file and the failure.
	fx.change(t, "ws/a/a.proto", 3, "syntax = \"proto3\";\nmessage {\n")
	_, err := formatting(fx.uri("ws/a/a.proto"), protocol.FormattingOptions{})
	var rpcErr *jsonrpc2.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc2.Code(protocol.LSPErrorCodesRequestFailed) || !strings.Contains(rpcErr.Message, "a/a.proto:2:9") || !strings.Contains(rpcErr.Message, "does not parse") {
		t.Fatalf("the parse failure: %v", err)
	}
	// A workspace copy of a well-known import: outside the build, an
	// own file.
	fx.open(t, "ws/a/google/protobuf/empty.proto", 1, string(fx.text(t, "ws/a/google/protobuf/empty.proto")))
	fx.publishes(t, "ws/a/google/protobuf/empty.proto")
	if edits, err := formatting(fx.uri("ws/a/google/protobuf/empty.proto"), protocol.FormattingOptions{}); err != nil || len(edits) != 1 || !strings.Contains(edits[0].NewText, "message Empty {}") {
		t.Fatalf("the well-known copy: %v %v", edits, err)
	}
	// No own file: outside the root, in no member, in a nested module,
	// through a link, a dependency's file, a document that is no
	// `.proto`.
	for _, tree := range []string{"elsewhere/x.proto", "ws/notes/n.proto", "ws/a/nested/x.proto", "ws/a/link/x.proto"} {
		fx.open(t, tree, 1, "syntax=\"proto3\";\n")
		fx.publishes(t, tree)
		if edits, err := formatting(fx.uri(tree), protocol.FormattingOptions{}); err != nil || len(edits) != 0 {
			t.Fatalf("%s: %v %v", tree, edits, err)
		}
	}
	dep := uri.MustParse("pb-module://example.com/std@v1.0.0/std.proto")
	if err := fx.server.DidOpen(context.Background(), &protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: dep, LanguageID: "proto", Version: 1, Text: "syntax=\"proto3\";\n"}}); err != nil {
		t.Fatal(err)
	}
	if edits, err := formatting(dep, protocol.FormattingOptions{}); err != nil || len(edits) != 0 {
		t.Fatalf("a dependency's document: %v %v", edits, err)
	}
	if err := fx.server.DidOpen(context.Background(), &protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: fx.uri("ws/a/pb.yaml"), LanguageID: "yaml", Version: 1, Text: "module:   x"}}); err != nil {
		t.Fatal(err)
	}
	if edits, err := formatting(fx.uri("ws/a/pb.yaml"), protocol.FormattingOptions{}); err != nil || len(edits) != 0 {
		t.Fatalf("a document that is no .proto: %v %v", edits, err)
	}
}

// The own files are judged by the resolution root as read at each
// reload, a session or a build loaded or not: with the lockfile
// unreadable no session loads, yet the root reads and a.proto is
// formatted; with the workspace file unreadable no root reads and
// the empty answer stands, the last session kept for the build; the
// workspace file mended, the root reads again (REQ-lsp-formatting,
// REQ-lsp-reload).
func TestFormattingWithoutASession(t *testing.T) {
	files := checkTree()
	files["ws/pb.lock"] = "not a lockfile\n"
	fx := newFixture(t, files)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	if m := <-fx.client.messages; !strings.Contains(m, "could not be loaded") {
		t.Fatalf("the failed load: %s", m)
	}
	fx.open(t, "ws/a/a.proto", 1, unformatted)
	fx.publishes(t, "ws/a/a.proto")
	formatting := func() []protocol.TextEdit {
		t.Helper()
		edits, err := fx.server.Formatting(context.Background(), &protocol.DocumentFormattingParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri("ws/a/a.proto")}})
		if err != nil {
			t.Fatal(err)
		}
		return edits
	}
	if edits := formatting(); len(edits) != 1 || edits[0].NewText != canonical {
		t.Fatalf("formatting with no session: %v", edits)
	}
	// The workspace file breaks: the root no longer reads.
	if err := util.WriteFile(fx.ws, "ws/pb.work", []byte("use: [not a workspace file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.watched(t, "ws/pb.work", protocol.FileChangeTypeChanged)
	if m := <-fx.client.messages; !strings.Contains(m, "could not be loaded") {
		t.Fatalf("the failed reload: %s", m)
	}
	if edits := formatting(); len(edits) != 0 {
		t.Fatalf("formatting with no root: %v", edits)
	}
	// Mended: the root reads again.
	if err := util.WriteFile(fx.ws, "ws/pb.work", []byte(checkTree()["ws/pb.work"]), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.watched(t, "ws/pb.work", protocol.FileChangeTypeChanged)
	if m := <-fx.client.messages; !strings.Contains(m, "could not be loaded") {
		t.Fatalf("the lockfile's failure: %s", m)
	}
	if edits := formatting(); len(edits) != 1 {
		t.Fatalf("formatting with the root mended: %v", edits)
	}
}

// A directory replacement's file is compiled with the build but is
// no own file: with the build loaded, a.proto is formatted and the
// replacement's std.proto is answered the empty answer
// (REQ-lsp-formatting, format.md's own-file term).
func TestFormattingReplacement(t *testing.T) {
	files := checkTree()
	files["ws/pb.work"] = "use:\n  - a\n  - b\n  - house\nreplace:\n  example.com/std: ./vendor/std\n"
	// A ruleset of a replaced path names no version.
	files["ws/pb.lint.yaml"] = "rulesets:\n  - path: example.com/house\n    alias: house\n  - path: example.com/std\n    alias: std\n"
	for p, body := range stdModule() {
		files["ws/vendor/std/"+p] = body
	}
	fx := newFixture(t, files)
	// Nothing to pin: every pair is replaced by a directory.
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work")
	fx.open(t, "ws/a/a.proto", 1, unformatted)
	fx.publishes(t, "ws/a/a.proto")
	edits, err := fx.server.Formatting(context.Background(), &protocol.DocumentFormattingParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri("ws/a/a.proto")}})
	if err != nil || len(edits) != 1 || edits[0].NewText != canonical {
		t.Fatalf("formatting under a loaded build: %v %v", edits, err)
	}
	fx.open(t, "ws/vendor/std/std.proto", 1, "syntax=\"proto3\";\npackage std;\nmessage S{string v=1;}\n")
	fx.waitStanding(t, "ws/vendor/std/std.proto") // a build file checked by no rule: nothing published
	if edits, err := fx.server.Formatting(context.Background(), &protocol.DocumentFormattingParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri("ws/vendor/std/std.proto")}}); err != nil || len(edits) != 0 {
		t.Fatalf("the replacement's file: %v %v", edits, err)
	}
}

// A dependency's file read from the source store is no own file even
// where the store lies under a member's directory
// (REQ-lsp-formatting, REQ-lsp-tree-untouched).
func TestFormattingSourceStore(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.sources = filepath.Join(fx.root, "ws", "a", ".pb-sources")
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{Workspace: &protocol.WorkspaceClientCapabilities{TextDocumentContent: &protocol.TextDocumentContentClientCapabilities{}}})
	fx.publishes(t, "ws/pb.work")
	tree := "ws/a/.pb-sources/well-known@abc/google/protobuf/any.proto"
	fx.open(t, tree, 1, "syntax=\"proto3\";\npackage google.protobuf;\nmessage Any{}\n")
	fx.publishes(t, tree)
	if edits, err := fx.server.Formatting(context.Background(), &protocol.DocumentFormattingParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri(tree)}}); err != nil || len(edits) != 0 {
		t.Fatalf("the source store's file: %v %v", edits, err)
	}
}

// On the wire, no edit is the empty list, as the empty-answer term
// has it for formatting (REQ-lsp-formatting).
func TestFormattingOnTheWire(t *testing.T) {
	fx := newFixture(t, checkTree())
	var err error
	if fx.srv, err = New(Deps{WS: fx.ws, OSRoot: fx.root, Client: assemble.Client(fx.Fixture, "proxy"), Sources: fx.sources}); err != nil {
		t.Fatal(err)
	}
	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()
	status := make(chan int, 1)
	go func() { status <- Serve(context.Background(), fx.srv, toServer, fromServer) }()
	writeFrame(t, fromClient, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"workspaceFolders":[{"uri":%q,"name":"ws"}],"capabilities":{}}}`, fx.uri("ws")))
	if r := readFrame(t, toClient); !strings.Contains(r, `"id":1`) {
		t.Fatalf("initialize: %s", r)
	}
	writeFrame(t, fromClient, `{"jsonrpc":"2.0","method":"initialized","params":{}}`)
	text, _ := json.Marshal(canonical)
	writeFrame(t, fromClient, fmt.Sprintf(`{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":%q,"languageId":"proto","version":1,"text":%s}}}`, fx.uri("ws/a/a.proto"), text))
	writeFrame(t, fromClient, fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"textDocument/formatting","params":{"textDocument":{"uri":%q},"options":{"tabSize":2,"insertSpaces":true}}}`, fx.uri("ws/a/a.proto")))
	for {
		r := readFrame(t, toClient)
		if !strings.Contains(r, `"id":2`) {
			continue // a publish, a message
		}
		if !strings.Contains(r, `"result":[]`) {
			t.Fatalf("the formatted document's answer: %s", r)
		}
		break
	}
	// The connection's end ends the server, its judgements with it.
	fromClient.Close()
	toClient.Close()
	select {
	case <-status:
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not end")
	}
}
