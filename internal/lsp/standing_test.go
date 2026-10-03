package lsp

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"
	"github.com/greatliontech/lsp/protocol"
	"github.com/greatliontech/lsp/uri"

	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/testing/fetchtest"
	"github.com/greatliontech/pb/internal/testing/fetchtest/assemble"
)

// A file that does not parse in a dependency no target imports fails
// the verb's import read, so the server reports it as a compile
// error on the dependency's address and publishes no lint finding
// (REQ-lsp-parity, REQ-lsp-diagnostics).
func TestUnparsableDependencyFile(t *testing.T) {
	fx := newFixture(t, checkTree())
	junk := stdModule()
	junk["junk.proto"] = "syntax = \"proto3\";\nmessage {\n"
	zip, _ := fetchtest.ModuleZip(t, junk)
	fx.Endpoint("example.com/std", "v1.0.0", "zip", string(zip))
	fx.pin(t)
	s, err := dep.Load(dep.Config{WS: fx.ws, Dir: "ws", Client: assemble.Client(fx.Fixture, "proxy")})
	if err != nil {
		t.Fatal(err)
	}
	if err := dep.Lint(context.Background(), s, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "junk.proto") {
		t.Fatalf("the verb over a build that does not parse: %v", err)
	}
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{Workspace: &protocol.WorkspaceClientCapabilities{TextDocumentContent: &protocol.TextDocumentContentClientCapabilities{}}})
	want := uri.MustParse("pb-module://example.com/std@v1.0.0/junk.proto")
	p := fx.publishFor(t, string(want), func(p *protocol.PublishDiagnosticsParams) bool {
		for _, d := range p.Diagnostics {
			if d.Code != protocol.String("compile") {
				t.Fatalf("a diagnostic beside the compile's errors: %s", render(p))
			}
		}
		return p.URI == want
	})
	if len(p.Diagnostics) != 1 || p.Diagnostics[0].Range.Start.Line != 1 {
		t.Fatalf("the unparsable file's diagnostic: %s", render(p))
	}
}

// A document under a nested module's directory — one holding a
// module file, not in the workspace's use list — is no file of the
// outer module: outside the build, and never overlaid into it
// (REQ-lsp-overlay, REQ-lsp-outside).
func TestNestedModuleDocument(t *testing.T) {
	files := checkTree()
	files["ws/a/examples/pb.yaml"] = ws("example.com/ax", "")
	files["ws/a/examples/e.proto"] = "syntax = \"proto3\";\npackage ax;\nmessage E {\n  string Nope = 1;\n}\n"
	fx := newFixture(t, files)
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work")
	fx.open(t, "ws/a/examples/e.proto", 1, files["ws/a/examples/e.proto"])
	p := fx.publishes(t, "ws/a/examples/e.proto")[fx.uri("ws/a/examples/e.proto")]
	if len(p.Diagnostics) != 1 || p.Diagnostics[0].Severity != protocol.DiagnosticSeverityInformation || !strings.Contains(string(p.Diagnostics[0].Message.(protocol.String)), "in no workspace module") {
		t.Fatalf("the nested module's document: %s", render(p))
	}
	// Nothing else moved: the build did not take the file in.
	fx.none(t, "ws/pb.work")
	fx.none(t, "ws/a/a.proto")
}

// A reload that fails keeps serving the last build loaded: the
// failure is reported, the build's diagnostics stand, and a document
// changed after it is judged against the kept build
// (REQ-lsp-reload, REQ-lsp-session).
func TestFailedReloadKeepsTheBuild(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work")
	if err := util.WriteFile(fx.ws, "ws/b/pb.yaml", []byte("module: [not a module file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.watched(t, "ws/b/pb.yaml", protocol.FileChangeTypeChanged)
	fx.message(t, "could not be loaded")
	fx.none(t, "ws/a/a.proto")
	text := string(fx.text(t, "ws/a/a.proto"))
	fx.open(t, "ws/a/a.proto", 1, strings.Replace(text, "BadName", "fine", 1))
	p := fx.publishes(t, "ws/a/a.proto")[fx.uri("ws/a/a.proto")]
	if v, _ := p.Version.Get(); v != 1 || len(p.Diagnostics) != 0 {
		t.Fatalf("the document judged against the kept build: %s", render(p))
	}
}

// A change arriving while a document's first judgement runs — its
// standing not yet known — is judged, so the client ends with the
// latest version's diagnostics (REQ-lsp-diagnostics, REQ-lsp-fresh).
func TestChangeDuringFirstJudgement(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work")
	text := string(fx.text(t, "ws/a/a.proto"))
	h := fx.holdNext()
	fx.open(t, "ws/a/a.proto", 1, text)
	h.held(t, "the judgement")
	next := fx.gen() + 1
	fx.change(t, "ws/a/a.proto", 2, strings.Replace(text, "BadName", "fine", 1))
	fx.waitGen(t, next)
	close(h.release)
	p := fx.publishFor(t, "version 2", func(p *protocol.PublishDiagnosticsParams) bool {
		v, _ := p.Version.Get()
		return p.URI == fx.uri("ws/a/a.proto") && v == 2
	})
	if len(p.Diagnostics) != 0 {
		t.Fatalf("version 2's publish: %s", render(p))
	}
}

// A reload a later change supersedes is carried to the judgement
// that commits: the unpinned diagnostic withdraws and the build is
// judged once pinned, though a keystroke cut the reload short
// (REQ-lsp-reload).
func TestReloadSupersededByChange(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/pb.work")
	text := string(fx.text(t, "ws/a/a.proto"))
	fx.open(t, "ws/a/a.proto", 1, text)
	fx.publishes(t, "ws/a/a.proto") // outside: no build is loaded
	fx.pin(t)
	h := fx.holdNext()
	fx.watched(t, "ws/pb.lock", protocol.FileChangeTypeCreated)
	h.held(t, "the judgement")
	next := fx.gen() + 1
	fx.change(t, "ws/a/a.proto", 2, text+"\n")
	fx.waitGen(t, next)
	close(h.release)
	got := fx.publishes(t, "ws/pb.work", "ws/a/a.proto")
	if ds := got[fx.uri("ws/pb.work")].Diagnostics; len(ds) != 1 || ds[0].Code != protocol.String("house:MESSAGE_COUNT") {
		t.Fatalf("the build home after the superseded reload: %s", render(got[fx.uri("ws/pb.work")]))
	}
	if p := got[fx.uri("ws/a/a.proto")]; len(p.Diagnostics) != 1 || p.Diagnostics[0].Code != protocol.String("house:FIELD_NAMES") {
		t.Fatalf("the document after the superseded reload: %s", render(p))
	}
}

// Shutdown ends the judgement in flight before answering: a
// judgement held past its last check publishes nothing after the
// answer (REQ-lsp-lifecycle).
func TestShutdownWaitsForTheJudgement(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work")
	h := fx.holdNext()
	fx.open(t, "ws/a/a.proto", 1, string(fx.text(t, "ws/a/a.proto")))
	h.held(t, "the judgement")
	answered := make(chan error, 1)
	go func() { answered <- fx.server.Shutdown(context.Background()) }()
	select {
	case err := <-answered:
		t.Fatalf("shutdown answered with a judgement in flight: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(h.release)
	select {
	case err := <-answered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(waitFor):
		t.Fatal("shutdown did not answer once the judgement ended")
	}
	fx.none(t, "ws/a/a.proto")
}

// The connection's end ends the server only once the judgement in
// flight has ended: nothing of the server outlives Serve
// (REQ-lsp-transport, REQ-lsp-lifecycle).
func TestConnectionEndWaitsForTheJudgement(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work")
	h := fx.holdNext()
	fx.open(t, "ws/a/a.proto", 1, string(fx.text(t, "ws/a/a.proto")))
	h.held(t, "the judgement")
	fx.conn.Close()
	select {
	case fx.exit = <-fx.status:
		fx.ended = true
		t.Fatalf("the server ended with a judgement in flight: %d", fx.exit)
	case <-time.After(300 * time.Millisecond):
	}
	close(h.release)
	if status := fx.wait(t); status != 1 {
		t.Fatalf("the server's status after the connection's end: %d", status)
	}
}

// A second `initialized` from a client offering the watcher
// registration asks no second registration and ends with the
// connection like the first: one registration, Serve returning
// (REQ-lsp-reload, REQ-lsp-transport).
func TestInitializedTwiceRegistersOnce(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{Workspace: &protocol.WorkspaceClientCapabilities{DidChangeWatchedFiles: &protocol.DidChangeWatchedFilesClientCapabilities{DynamicRegistration: ptr(true)}}})
	if err := fx.server.Initialized(context.Background(), &protocol.InitializedParams{}); err != nil {
		t.Fatal(err)
	}
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work")
	<-fx.client.registered
	// A second registration would follow the second initialized
	// within the window; none does.
	select {
	case r := <-fx.client.registered:
		t.Fatalf("a second registration: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
	fx.conn.Close()
	if status := fx.wait(t); status != 1 {
		t.Fatalf("the server's status: %d", status)
	}
}

// The tree is rooted at the client root's volume, opened once
// initialize names the root, not at the process's working
// directory's (the client root term).
func TestClientRootVolume(t *testing.T) {
	var opened string
	ws := memfs.New()
	srv, err := New(Deps{OpenTree: func(root string) billy.Filesystem {
		opened = root
		return ws
	}})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.VolumeName(os.TempDir()) + string(filepath.Separator)
	dir := filepath.Join(root, "elsewhere", "ws")
	if _, err := srv.Initialize(context.Background(), &protocol.InitializeParams{
		WorkspaceFoldersInitializeParams: protocol.WorkspaceFoldersInitializeParams{WorkspaceFolders: protocol.NewNullable([]protocol.WorkspaceFolder{{URI: uri.File(dir), Name: "ws"}})},
	}); err != nil {
		t.Fatal(err)
	}
	// A volume's letter is spelled as the URI canonicalizes it, lower
	// case; the host names the same volume either way.
	if !strings.EqualFold(opened, root) || !strings.EqualFold(srv.deps.OSRoot, root) || srv.clientRoot != "elsewhere/ws" || !srv.hasRoot {
		t.Fatalf("the tree opened at %q, the client root %q under %q", opened, srv.clientRoot, srv.deps.OSRoot)
	}
}

// A notification never ends the connection: one the server does not
// serve — a cancellation, a custom method — one whose params do not
// decode, a request method sent as one, initialize sent again as one,
// each dropped and the connection going on; a call of an unknown
// method is refused (REQ-lsp-lifecycle).
func TestNotificationNeverEndsTheConnection(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work")
	ctx := context.Background()
	for _, n := range []struct {
		method string
		params any
	}{
		{protocol.MethodCancelRequest, &protocol.CancelParams{ID: protocol.Integer(7)}},
		{"$/custom", map[string]int{"x": 1}},
		{protocol.MethodTextDocumentDidChange, map[string]string{"textDocument": "not a document"}},
		// A request method sent as a notification, and initialize again
		// as one: neither can be answered, both are dropped.
		{protocol.MethodTextDocumentHover, map[string]any{"textDocument": map[string]string{"uri": string(fx.uri("ws/a/a.proto"))}, "position": map[string]int{"line": 0, "character": 0}}},
		{protocol.MethodInitialize, map[string]any{"capabilities": map[string]any{}}},
	} {
		if err := fx.conn.Notify(ctx, n.method, n.params); err != nil {
			t.Fatalf("%s: %v", n.method, err)
		}
	}
	var out any
	if _, err := fx.conn.Call(ctx, "$/unknown", map[string]int{}, &out); err == nil || !strings.Contains(err.Error(), "method not found") {
		t.Fatalf("a call of an unknown method: %v", err)
	}
	// The connection went on: a change is still judged.
	fx.open(t, "ws/a/a.proto", 1, strings.Replace(string(fx.text(t, "ws/a/a.proto")), "BadName", "fine", 1))
	if p := fx.publishes(t, "ws/a/a.proto")[fx.uri("ws/a/a.proto")]; len(p.Diagnostics) != 0 {
		t.Fatalf("after the dropped notifications: %s", render(p))
	}
}

// A cancellation has no effect: a request cancelled while its answer
// is outstanding is answered in wire order with its result, never
// RequestCancelled, and the connection goes on (REQ-lsp-lifecycle).
func TestCancellationHasNoEffect(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work")
	fx.open(t, "ws/a/a.proto", 1, string(fx.text(t, "ws/a/a.proto")))
	fx.publishes(t, "ws/a/a.proto")
	ctx := context.Background()
	// The hover over Thing's declaration, which answers with content.
	pos := fx.token(t, "ws/a/a.proto", "Thing {", 0)
	params := &protocol.HoverParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri("ws/a/a.proto")}, Position: pos}}
	pending, err := fx.conn.Send(ctx, protocol.MethodTextDocumentHover, params)
	if err != nil {
		t.Fatalf("the request: %v", err)
	}
	n, ok := pending.ID().Number()
	if !ok {
		t.Fatalf("the request's id is no number: %v", pending.ID())
	}
	// The cancellation reaches the wire before the answer is read.
	if err := fx.conn.Notify(ctx, protocol.MethodCancelRequest, &protocol.CancelParams{ID: protocol.Integer(n)}); err != nil {
		t.Fatalf("the cancellation: %v", err)
	}
	var h *protocol.Hover
	if err := pending.Await(ctx, &h); err != nil {
		t.Fatalf("the cancelled request's answer: %v", err)
	}
	if h == nil || !strings.Contains(fmt.Sprint(h.Contents), "Thing") {
		t.Fatalf("the cancelled request's answer is not the hover: %v", h)
	}
	// The connection goes on.
	var again *protocol.Hover
	if _, err := fx.conn.Call(ctx, protocol.MethodTextDocumentHover, params, &again); err != nil || again == nil {
		t.Fatalf("the request after the cancellation: %v %v", again, err)
	}
}

// A finding a module's own selection locates at the module's
// directory — a set rule's — lands on that module's module file at
// its first line (the placement term, REQ-lsp-diagnostics).
func TestModuleFilePlacement(t *testing.T) {
	files := checkTree()
	files["ws/pb.lint.yaml"] = "rulesets:\n  - path: example.com/house\n    alias: house\nmodules:\n  a:\n    enable: [MESSAGE_COUNT]\n"
	fx := newFixture(t, files)
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/b/b.proto")
	fx.open(t, "ws/a/a.proto", 1, string(fx.text(t, "ws/a/a.proto"))+"\nmessage Third {}\n")
	p := fx.publishes(t, "ws/a/pb.yaml")[fx.uri("ws/a/pb.yaml")]
	if len(p.Diagnostics) != 1 || p.Diagnostics[0].Code != protocol.String("house:MESSAGE_COUNT") || p.Diagnostics[0].Range.Start != (protocol.Position{}) {
		t.Fatalf("the module's set finding: %s", render(p))
	}
	if _, ok := p.Version.Get(); ok {
		t.Fatal("a closed module file's publish carried a version")
	}
}

// A client root that loads no session from the start — a malformed
// workspace file — leaves the server serving with no build: the
// failure reported, a document outside the build, the lifecycle
// intact; a later reload tries again (REQ-lsp-session).
func TestNoSessionAtStart(t *testing.T) {
	files := checkTree()
	files["ws/pb.work"] = "use: [not a workspace file\n"
	fx := newFixture(t, files)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.message(t, "could not be loaded")
	fx.open(t, "ws/a/a.proto", 1, string(fx.text(t, "ws/a/a.proto")))
	p := fx.publishes(t, "ws/a/a.proto")[fx.uri("ws/a/a.proto")]
	if len(p.Diagnostics) != 1 || !strings.Contains(string(p.Diagnostics[0].Message.(protocol.String)), "no build is loaded") {
		t.Fatalf("the document with no build loaded: %s", render(p))
	}
	// Another failed reload while still no session has loaded.
	fx.watched(t, "ws/pb.work", protocol.FileChangeTypeChanged)
	fx.message(t, "could not be loaded")
	// Mended and reloaded, the build is judged.
	if err := util.WriteFile(fx.ws, "ws/pb.work", []byte(checkTree()["ws/pb.work"]), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.pin(t)
	fx.watched(t, "ws/pb.work", protocol.FileChangeTypeChanged)
	got := fx.publishes(t, "ws/a/a.proto", "ws/pb.work")
	if ds := got[fx.uri("ws/a/a.proto")].Diagnostics; len(ds) != 1 || ds[0].Code != protocol.String("house:FIELD_NAMES") {
		t.Fatalf("the document once the root loads: %s", render(got[fx.uri("ws/a/a.proto")]))
	}
	if err := fx.server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
