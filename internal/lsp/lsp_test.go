package lsp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/helper/iofs"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/util"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/testing/fetchtest"
	"github.com/greatliontech/pb/internal/testing/fetchtest/assemble"
	"github.com/greatliontech/pb/internal/testing/scratchtest"
)

// fixture is a server over an in-memory tree, served in-process to a
// recording client over a channel stream pair.
type fixture struct {
	*fetchtest.Fixture
	ws      billy.Filesystem
	root    string // the host root the tree is rooted at
	sources string
	srv     *Server
	server  protocol.Server // the dispatcher to the server
	client  *recorder
	conn    jsonrpc2.Conn
	status  chan int
	ended   bool
	exit    int

	mu   sync.Mutex
	held map[uint64]*holding // judgements held at the seam, by generation
}

// holding is a judgement held at the seam: holding closes once it
// holds, release lets it go on.
type holding struct {
	holding, release chan struct{}
}

// recorder is the client: every publish and message recorded, a
// registration accepted.
type recorder struct {
	protocol.UnimplementedClient
	publishes  chan *protocol.PublishDiagnosticsParams
	messages   chan string
	registered chan *protocol.RegistrationParams
}

func (r *recorder) PublishDiagnostics(_ context.Context, p *protocol.PublishDiagnosticsParams) error {
	r.publishes <- p
	return nil
}

func (r *recorder) ShowMessage(_ context.Context, p *protocol.ShowMessageParams) error {
	r.messages <- p.Message
	return nil
}

func (r *recorder) RegisterCapability(_ context.Context, p *protocol.RegistrationParams) error {
	r.registered <- p
	return nil
}

// ws spells a module file.
func ws(module, deps string) string {
	if deps == "" {
		return "module: " + module + "\n"
	}
	return "module: " + module + "\ndeps:\n" + deps
}

// houseRules is the workspace ruleset: a field-name rule, a set rule
// over the message count.
const houseRules = "celEnv: 1\nrules:\n" +
	"  - id: FIELD_NAMES\n    kind: lint\n    target: field\n    severity: error\n    tags: [naming]\n    cel: case(field.name, 'snake') == field.name\n    message: field names are snake_case\n" +
	"  - id: MESSAGE_COUNT\n    kind: lint\n    target: set\n    severity: warning\n    cel: messages(files).size() < 3\n    message: too many messages\n"

// checkTree is the workspace the suite judges: two modules under a
// workspace file, a workspace ruleset, an external ruleset and a
// dependency served by the fixture.
func checkTree() map[string]string {
	return map[string]string{
		"ws/pb.work":                "use:\n  - a\n  - b\n  - house\n",
		"ws/pb.lint.yaml":           "rulesets:\n  - path: example.com/house\n    alias: house\n  - path: example.com/std\n    version: v1.0.0\n    alias: std\n",
		"ws/a/pb.yaml":              ws("example.com/a", "  example.com/std: v1.0.0\n"),
		"ws/a/a.proto":              "syntax = \"proto3\";\npackage a;\nimport \"std.proto\";\n\nmessage Thing {\n  string BadName = 1;\n  std.S s = 2;\n}\n\nmessage Other {}\n",
		"ws/b/pb.yaml":              ws("example.com/b", "  example.com/a: v0.0.1\n"),
		"ws/b/b.proto":              "syntax = \"proto3\";\npackage b;\nimport \"a.proto\";\nmessage Use {\n  a.Thing thing = 1;\n  string Loud = 2;\n}\n",
		"ws/house/pb.yaml":          ws("example.com/house", ""),
		"ws/house/house.rules.yaml": houseRules,
	}
}

// stdModule is the dependency the fixture serves: a module with a
// ruleset and a protobuf file.
func stdModule() map[string]string {
	return map[string]string{
		"pb.yaml":        ws("example.com/std", ""),
		"std.proto":      "syntax = \"proto3\";\npackage std;\nmessage S {\n  string v = 1;\n}\n",
		"std.rules.yaml": "celEnv: 1\nrules:\n  - id: PACKAGE_DEFINED\n    kind: lint\n    target: file\n    severity: error\n    cel: file.package != ''\n    message: files declare a package\n",
	}
}

// newFixture writes the tree and serves the dependency; the server
// is started by start.
func newFixture(t *testing.T, files map[string]string) *fixture {
	t.Helper()
	fx := &fixture{Fixture: fetchtest.New(t), ws: memfs.New(), held: map[uint64]*holding{}}
	for p, body := range files {
		if err := util.WriteFile(fx.ws, p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	zip, _ := fetchtest.ModuleZip(t, stdModule())
	fx.Endpoint("example.com/std", "v1.0.0", "zip", string(zip))
	fx.root = filepath.VolumeName(os.TempDir()) + string(filepath.Separator) + "pbtest"
	fx.sources = scratchtest.Dir(t)
	return fx
}

// pin pins the tree's requirements as a verb would: the lint verb run
// over a read-write session, the lockfile then written.
func (fx *fixture) pin(t *testing.T) {
	t.Helper()
	s, err := dep.Load(dep.Config{WS: fx.ws, Dir: "ws", Client: assemble.Client(fx.Fixture, "proxy")})
	if err != nil {
		t.Fatal(err)
	}
	// The verb pins the build list and the rulesets before it compiles
	// (REQ-lock-first-use): a build that does not compile is pinned
	// all the same.
	_ = dep.Lint(context.Background(), s, io.Discard, io.Discard)
	if _, err := fx.ws.Stat("ws/pb.lock"); err != nil {
		t.Fatalf("the lint verb pinned nothing: %v", err)
	}
}

// start serves the fixture's server to the recorder.
func (fx *fixture) start(t *testing.T) {
	t.Helper()
	var err error
	fx.srv, err = New(Deps{WS: fx.ws, OSRoot: fx.root, Client: assemble.Client(fx.Fixture, "proxy"), Sources: fx.sources, Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	if err != nil {
		t.Fatal(err)
	}
	fx.srv.hold = func(gen uint64) {
		fx.mu.Lock()
		h := fx.held[gen]
		fx.mu.Unlock()
		if h != nil {
			close(h.holding)
			<-h.release
		}
	}
	fx.client = &recorder{publishes: make(chan *protocol.PublishDiagnosticsParams, 256), messages: make(chan string, 16), registered: make(chan *protocol.RegistrationParams, 4)}
	left, right := jsonrpc2.NewChannelStreamPair(16)
	fx.status = make(chan int, 1)
	go func() { fx.status <- fx.srv.serve(context.Background(), left) }()
	ctx := context.Background()
	_, fx.conn, fx.server = protocol.NewClient(ctx, fx.client, right)
	// The connection's end ends the server, its judgements with it,
	// before the fixture's transport goes.
	t.Cleanup(func() {
		fx.conn.Close()
		fx.wait(t)
	})
}

// gen is the server's current generation.
func (fx *fixture) gen() uint64 {
	fx.srv.mu.Lock()
	defer fx.srv.mu.Unlock()
	return fx.srv.gen
}

// waitGen waits until the server has recorded the change of the
// given generation.
func (fx *fixture) waitGen(t *testing.T, gen uint64) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		fx.srv.mu.Lock()
		got := fx.srv.gen
		fx.srv.mu.Unlock()
		if got >= gen {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server's generation is %d, want %d", got, gen)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// wait waits for the server's end, once.
func (fx *fixture) wait(t *testing.T) int {
	t.Helper()
	if fx.ended {
		return fx.exit
	}
	select {
	case fx.exit = <-fx.status:
		fx.ended = true
	case <-time.After(30 * time.Second):
		t.Error("the server did not end with the connection")
	}
	return fx.exit
}

// initialize runs the lifecycle's start: initialize with the client
// root, then initialized.
func (fx *fixture) initialize(t *testing.T, caps protocol.ClientCapabilities) *protocol.InitializeResult {
	t.Helper()
	res, err := fx.server.Initialize(context.Background(), &protocol.InitializeParams{
		WorkspaceFoldersInitializeParams: protocol.WorkspaceFoldersInitializeParams{WorkspaceFolders: protocol.NewNullable([]protocol.WorkspaceFolder{{URI: fx.uri("ws"), Name: "ws"}})},
		Capabilities:                     caps,
	})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := fx.server.Initialized(context.Background(), &protocol.InitializedParams{}); err != nil {
		t.Fatalf("initialized: %v", err)
	}
	return res
}

// uri is the file URI of a tree path.
func (fx *fixture) uri(tree string) uri.URI {
	return uri.File(filepath.Join(fx.root, filepath.FromSlash(tree)))
}

// text is the tree's file.
func (fx *fixture) text(t *testing.T, tree string) []byte {
	t.Helper()
	b, err := util.ReadFile(fx.ws, tree)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// publishes collects the next judgement's publishes: every publish
// until the given set of URIs has each been seen once, within the
// deadline.
func (fx *fixture) publishes(t *testing.T, want ...string) map[uri.URI]*protocol.PublishDiagnosticsParams {
	t.Helper()
	got := map[uri.URI]*protocol.PublishDiagnosticsParams{}
	pending := map[uri.URI]bool{}
	for _, w := range want {
		pending[fx.uri(w)] = true
	}
	deadline := time.After(20 * time.Second)
	for len(pending) > 0 {
		select {
		case p := <-fx.client.publishes:
			got[p.URI] = p
			delete(pending, p.URI)
		case m := <-fx.client.messages:
			t.Fatalf("the server reported: %s", m)
		case <-deadline:
			t.Fatalf("no publish for %v within the deadline; got %v", keys(pending), keysOf(got))
		}
	}
	return got
}

// none asserts no publish for the URI arrives for a while.
func (fx *fixture) none(t *testing.T, tree string) {
	t.Helper()
	u := fx.uri(tree)
	timer := time.After(500 * time.Millisecond)
	for {
		select {
		case p := <-fx.client.publishes:
			if p.URI == u {
				t.Fatalf("a publish for %s arrived: %s", tree, render(p))
			}
		case <-timer:
			return
		}
	}
}

func keys(m map[uri.URI]bool) []string {
	var out []string
	for u := range m {
		out = append(out, string(u))
	}
	sort.Strings(out)
	return out
}

func keysOf(m map[uri.URI]*protocol.PublishDiagnosticsParams) []string {
	var out []string
	for u := range m {
		out = append(out, string(u))
	}
	sort.Strings(out)
	return out
}

// render spells a publish for a failure message.
func render(p *protocol.PublishDiagnosticsParams) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s", p.URI)
	if v, ok := p.Version.Get(); ok {
		fmt.Fprintf(&b, "@%d", v)
	}
	for _, d := range p.Diagnostics {
		fmt.Fprintf(&b, "\n  %d:%d-%d:%d %v %v %v", d.Range.Start.Line, d.Range.Start.Character, d.Range.End.Line, d.Range.End.Character, d.Severity, d.Code, d.Message)
	}
	return b.String()
}

// line spells a diagnostic as pb lint would its finding, under
// utf-16 columns over the text: `line:col: severity code: message`.
func line(d protocol.Diagnostic, text []byte) string {
	off := offsetAt(text, d.Range.Start)
	l, c := pbPosition(text, off)
	sev := "warning"
	if d.Severity == protocol.DiagnosticSeverityError {
		sev = "error"
	}
	return fmt.Sprintf("%d:%d: %s %s: %s", l, c, sev, d.Code.(protocol.String), d.Message.(protocol.String))
}

// offsetAt is the byte offset of a utf-16 protocol position in text,
// counted here as the protocol counts — lines ending at \n, \r\n or
// a lone \r, columns in utf-16 units — independently of the server's
// conversion, so the parity witness checks both directions.
func offsetAt(text []byte, pos protocol.Position) int {
	line, units := uint32(0), uint32(0)
	for i := 0; i < len(text); {
		if line == pos.Line && units == pos.Character {
			return i
		}
		switch {
		case text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n':
			line, units = line+1, 0
			i += 2
			continue
		case text[i] == '\n' || text[i] == '\r':
			line, units = line+1, 0
			i++
			continue
		}
		r, n := utf8.DecodeRune(text[i:])
		if r >= 0x10000 {
			units += 2
		} else {
			units++
		}
		i += n
	}
	return len(text)
}

// pbPosition is pb's one-based line and code-point column of an
// offset, lines at `\n`, a tab one point.
func pbPosition(text []byte, off int) (int, int) {
	l, c := 1, 1
	for i := 0; i < off && i < len(text); i++ {
		if text[i] == '\n' {
			l, c = l+1, 1
			continue
		}
		if text[i]&0xC0 != 0x80 {
			c++
		}
	}
	return l, c
}

// digest hashes every file of the tree, so a server's run can be
// shown to have written nothing (REQ-lsp-tree-untouched).
func (fx *fixture) digest(t *testing.T) string {
	t.Helper()
	h := sha256.New()
	fsys := iofs.New(fx.ws)
	var paths []string
	if err := fs.WalkDir(fsys, "ws", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	for _, p := range paths {
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(h, "%s\n%d\n", p, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// open opens a .proto document at a version with the given text.
func (fx *fixture) open(t *testing.T, tree string, version int32, text string) {
	t.Helper()
	if err := fx.server.DidOpen(context.Background(), &protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: fx.uri(tree), LanguageID: "proto", Version: version, Text: text}}); err != nil {
		t.Fatal(err)
	}
}

// change replaces a document's text at a version.
func (fx *fixture) change(t *testing.T, tree string, version int32, text string) {
	t.Helper()
	if err := fx.server.DidChange(context.Background(), &protocol.DidChangeTextDocumentParams{
		TextDocument:   protocol.VersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: fx.uri(tree)}, Version: version},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{&protocol.TextDocumentContentChangeWholeDocument{Text: text}},
	}); err != nil {
		t.Fatal(err)
	}
}

func (fx *fixture) close(t *testing.T, tree string) {
	t.Helper()
	if err := fx.server.DidClose(context.Background(), &protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: fx.uri(tree)}}); err != nil {
		t.Fatal(err)
	}
}

func (fx *fixture) watched(t *testing.T, tree string, typ protocol.FileChangeType) {
	t.Helper()
	if err := fx.server.DidChangeWatchedFiles(context.Background(), &protocol.DidChangeWatchedFilesParams{Changes: []protocol.FileEvent{{URI: fx.uri(tree), Type: typ}}}); err != nil {
		t.Fatal(err)
	}
}

// lintOutput is what pb lint prints over the tree, through a
// read-write session of its own.
func (fx *fixture) lintOutput(t *testing.T) string {
	t.Helper()
	s, err := dep.Load(dep.Config{WS: fx.ws, Dir: "ws", Client: assemble.Client(fx.Fixture, "proxy")})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := dep.Lint(context.Background(), s, &out, io.Discard); err != nil && err != dep.ErrFindings {
		t.Fatalf("pb lint: %v", err)
	}
	return out.String()
}

// The server's diagnostics are pb lint's output for the same tree,
// one to one, each on the file the finding names at the same line
// and column under conversion, the file-less finding on the build
// home (REQ-lsp-parity, REQ-lsp-diagnostics).
func TestParity(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	verb := fx.lintOutput(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	got := fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work")
	modules := map[string]string{"a.proto": "ws/a/a.proto", "b.proto": "ws/b/b.proto"}
	var server []string
	for p, tree := range modules {
		text := fx.text(t, tree)
		for _, d := range got[fx.uri(tree)].Diagnostics {
			server = append(server, p+":"+line(d, text))
		}
	}
	for _, d := range got[fx.uri("ws/pb.work")].Diagnostics {
		server = append(server, fmt.Sprintf("%s %s: %s", map[protocol.DiagnosticSeverity]string{1: "error", 2: "warning"}[d.Severity], d.Code.(protocol.String), d.Message.(protocol.String)))
	}
	sort.Strings(server)
	want := strings.Split(strings.TrimSpace(verb), "\n")
	sort.Strings(want)
	if strings.Join(server, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the server's diagnostics:\n%s\npb lint's findings:\n%s", strings.Join(server, "\n"), strings.Join(want, "\n"))
	}
	if len(want) < 3 {
		t.Fatalf("the fixture judges too little to witness parity: %q", verb)
	}
	// Every diagnostic is pb's, coded by the rule, ranged over the
	// token at the finding's position.
	for _, d := range got[fx.uri("ws/a/a.proto")].Diagnostics {
		if s, _ := d.Source.Get(); s != "pb" || d.Range.End.Character <= d.Range.Start.Character {
			t.Fatalf("a diagnostic's source or range: %s", render(got[fx.uri("ws/a/a.proto")]))
		}
	}
	if _, ok := got[fx.uri("ws/a/a.proto")].Version.Get(); ok {
		t.Fatal("a closed file's publish carried a version")
	}
}

// An open document is judged in place of the tree's file, at the
// client's version, and nothing of it reaches the tree; a document
// closed leaves the tree's file to speak (REQ-lsp-overlay,
// REQ-lsp-tree-untouched).
func TestOverlay(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	fx.start(t)
	before := fx.digest(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work")
	fixed := strings.Replace(string(fx.text(t, "ws/a/a.proto")), "BadName", "good_name", 1)
	fx.open(t, "ws/a/a.proto", 1, fixed)
	got := fx.publishes(t, "ws/a/a.proto")
	p := got[fx.uri("ws/a/a.proto")]
	if v, ok := p.Version.Get(); !ok || v != 1 || len(p.Diagnostics) != 0 {
		t.Fatalf("the overlaid document's publish: %s", render(p))
	}
	fx.change(t, "ws/a/a.proto", 2, strings.Replace(fixed, "good_name", "Worse", 1))
	p = fx.publishes(t, "ws/a/a.proto")[fx.uri("ws/a/a.proto")]
	if v, _ := p.Version.Get(); v != 2 || len(p.Diagnostics) != 1 || !strings.Contains(line(p.Diagnostics[0], []byte(strings.Replace(fixed, "good_name", "Worse", 1))), "6:3: error house:FIELD_NAMES") {
		t.Fatalf("the changed document's publish: %s", render(p))
	}
	fx.close(t, "ws/a/a.proto")
	p = fx.publishes(t, "ws/a/a.proto")[fx.uri("ws/a/a.proto")]
	if _, ok := p.Version.Get(); ok || len(p.Diagnostics) != 1 {
		t.Fatalf("the tree's file after the close: %s", render(p))
	}
	if after := fx.digest(t); after != before {
		t.Fatal("the server wrote to the tree")
	}
}

// A requirement the lockfile does not pin leaves the server with no
// build: the unpinned diagnostic on the build home — no lockfile
// exists — naming the pair and pb dep download, no finding anywhere,
// nothing written; pinned by a verb and reloaded, the diagnostic
// withdraws and the build is judged (REQ-lsp-unpinned,
// REQ-lsp-session, REQ-lsp-tree-untouched).
func TestUnpinned(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.start(t)
	before := fx.digest(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	got := fx.publishes(t, "ws/pb.work")
	p := got[fx.uri("ws/pb.work")]
	if len(p.Diagnostics) != 1 || p.Diagnostics[0].Code != protocol.String("unpinned") || !strings.Contains(string(p.Diagnostics[0].Message.(protocol.String)), "example.com/std@v1.0.0") || !strings.Contains(string(p.Diagnostics[0].Message.(protocol.String)), "pb dep download") {
		t.Fatalf("the unpinned diagnostic: %s", render(p))
	}
	fx.none(t, "ws/a/a.proto")
	if _, err := fx.ws.Stat("ws/pb.lock"); err == nil {
		t.Fatal("the server wrote a lockfile")
	}
	if after := fx.digest(t); after != before {
		t.Fatal("the server wrote to the tree")
	}
	// A .proto document is outside the build meanwhile.
	fx.open(t, "ws/a/a.proto", 1, string(fx.text(t, "ws/a/a.proto")))
	p = fx.publishes(t, "ws/a/a.proto")[fx.uri("ws/a/a.proto")]
	if len(p.Diagnostics) != 1 || p.Diagnostics[0].Severity != protocol.DiagnosticSeverityInformation || !strings.Contains(string(p.Diagnostics[0].Message.(protocol.String)), "no build is loaded") {
		t.Fatalf("the document while no build is loaded: %s", render(p))
	}
	fx.pin(t)
	fx.watched(t, "ws/pb.lock", protocol.FileChangeTypeCreated)
	got = fx.publishes(t, "ws/pb.work", "ws/a/a.proto", "ws/b/b.proto")
	if len(got[fx.uri("ws/pb.work")].Diagnostics) != 1 || got[fx.uri("ws/pb.work")].Diagnostics[0].Code != protocol.String("house:MESSAGE_COUNT") {
		t.Fatalf("the build home after pinning: %s", render(got[fx.uri("ws/pb.work")]))
	}
	if ds := got[fx.uri("ws/a/a.proto")].Diagnostics; len(ds) != 1 || ds[0].Code != protocol.String("house:FIELD_NAMES") {
		t.Fatalf("the document after pinning: %s", render(got[fx.uri("ws/a/a.proto")]))
	}
}

// A judgement a later change superseded is dropped unpublished: the
// client never holds diagnostics of an older version than the last
// it reported (REQ-lsp-fresh).
func TestSupersession(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work")
	text := string(fx.text(t, "ws/a/a.proto"))
	fx.open(t, "ws/a/a.proto", 1, text)
	fx.publishes(t, "ws/a/a.proto")
	// The judgement of version 2 is held once computed, past its
	// context's last check; version 3's change then supersedes it, and
	// released, it reaches the publish with a generation that moved.
	h := &holding{holding: make(chan struct{}), release: make(chan struct{})}
	fx.mu.Lock()
	fx.held[fx.gen()+1] = h
	fx.mu.Unlock()
	fx.change(t, "ws/a/a.proto", 2, strings.Replace(text, "BadName", "AlsoBad", 1))
	select {
	case <-h.holding:
	case <-time.After(20 * time.Second):
		t.Fatal("the judgement of version 2 was not held")
	}
	// A notification carries no acknowledgement: the held judgement is
	// released once the server has taken version 3's change.
	next := fx.gen() + 1
	fx.change(t, "ws/a/a.proto", 3, strings.Replace(text, "BadName", "fine", 1))
	fx.waitGen(t, next)
	close(h.release)
	p := fx.publishes(t, "ws/a/a.proto")[fx.uri("ws/a/a.proto")]
	if v, _ := p.Version.Get(); v != 3 || len(p.Diagnostics) != 0 {
		t.Fatalf("the publish after the two changes: %s", render(p))
	}
	fx.none(t, "ws/a/a.proto")
}

// A diagnostic once published is withdrawn by the judgement that no
// longer makes it, whatever the file became: a closed file's finding
// survives a judgement that places it again unpublished, and goes
// with an empty publish when the reloaded lint file ignores it
// (the publish set, REQ-lsp-diagnostics, REQ-lsp-reload).
func TestPublishSetWithdraws(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	got := fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work")
	if len(got[fx.uri("ws/a/a.proto")].Diagnostics) != 1 {
		t.Fatalf("the first judgement: %s", render(got[fx.uri("ws/a/a.proto")]))
	}
	// A change elsewhere places the same finding on a.proto: no
	// publish for it.
	fx.open(t, "ws/b/b.proto", 1, string(fx.text(t, "ws/b/b.proto")))
	fx.publishes(t, "ws/b/b.proto")
	fx.none(t, "ws/a/a.proto")
	// The lint file now ignores a.proto: the reload withdraws.
	lint := string(fx.text(t, "ws/pb.lint.yaml")) + "ignore:\n  - paths: [a.proto]\n"
	if err := util.WriteFile(fx.ws, "ws/pb.lint.yaml", []byte(lint), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.watched(t, "ws/pb.lint.yaml", protocol.FileChangeTypeChanged)
	p := fx.publishes(t, "ws/a/a.proto")[fx.uri("ws/a/a.proto")]
	if len(p.Diagnostics) != 0 {
		t.Fatalf("the withdrawal: %s", render(p))
	}
	if _, ok := p.Version.Get(); ok {
		t.Fatal("a closed file's withdrawal carried a version")
	}
}

// A .proto document outside the build receives one informational
// diagnostic naming the first reason that applies, its change is
// judged by nothing, and a document of another kind is held and
// judged by nothing (REQ-lsp-outside).
func TestOutside(t *testing.T) {
	files := checkTree()
	files["elsewhere/x.proto"] = "syntax = \"proto3\";\n"
	files["ws/a/google/protobuf/empty.proto"] = "syntax = \"proto3\";\npackage google.protobuf;\nmessage Empty {}\n"
	files["ws/notes/n.proto"] = "syntax = \"proto3\";\n"
	fx := newFixture(t, files)
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work")
	for tree, reason := range map[string]string{
		"elsewhere/x.proto":                "outside the resolution root",
		"ws/a/google/protobuf/empty.proto": "a workspace copy of a well-known import",
		"ws/notes/n.proto":                 "in no workspace module and no directory replacement",
	} {
		fx.open(t, tree, 1, string(fx.text(t, tree)))
		p := fx.publishes(t, tree)[fx.uri(tree)]
		if len(p.Diagnostics) != 1 || p.Diagnostics[0].Severity != protocol.DiagnosticSeverityInformation || !strings.Contains(string(p.Diagnostics[0].Message.(protocol.String)), reason) {
			t.Fatalf("%s: %s", tree, render(p))
		}
		if v, _ := p.Version.Get(); v != 1 {
			t.Fatalf("%s: the publish's version: %s", tree, render(p))
		}
	}
	fx.change(t, "elsewhere/x.proto", 2, "syntax = \"proto3\";\n\n")
	fx.none(t, "elsewhere/x.proto")
	if err := fx.server.DidOpen(context.Background(), &protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: fx.uri("ws/a/pb.yaml"), LanguageID: "yaml", Version: 1, Text: "module: nonsense"}}); err != nil {
		t.Fatal(err)
	}
	fx.none(t, "ws/a/pb.yaml")
}

// A build that does not compile publishes every error the compiler
// reports, each at its placement — an import no module satisfies at
// the import statement — and no lint finding anywhere
// (REQ-lsp-diagnostics).
func TestCompileErrors(t *testing.T) {
	files := checkTree()
	files["ws/b/b.proto"] = "syntax = \"proto3\";\npackage b;\nimport \"a.proto\";\nimport \"missing.proto\";\nmessage Use {\n  a.Thing thing = 1;\n  string Loud = 2;\n}\n"
	fx := newFixture(t, files)
	fx.pin(t)
	fx.start(t)
	fx.initialize(t, protocol.ClientCapabilities{})
	fx.open(t, "ws/a/a.proto", 1, "syntax = \"proto3\";\npackage a;\nmessage Thing {\n  string BadName = 1\n}\n")
	got := fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto")
	a := got[fx.uri("ws/a/a.proto")]
	b := got[fx.uri("ws/b/b.proto")]
	if len(a.Diagnostics) != 1 || a.Diagnostics[0].Code != protocol.String("compile") || a.Diagnostics[0].Range.Start.Line != 4 {
		t.Fatalf("the syntax error: %s", render(a))
	}
	if len(b.Diagnostics) != 1 || b.Diagnostics[0].Code != protocol.String("compile") || b.Diagnostics[0].Range.Start.Line != 3 || !strings.Contains(string(b.Diagnostics[0].Message.(protocol.String)), "missing.proto") {
		t.Fatalf("the unsatisfied import: %s", render(b))
	}
	for _, p := range got {
		for _, d := range p.Diagnostics {
			if d.Code == protocol.String("house:FIELD_NAMES") {
				t.Fatalf("a lint finding beside the compile's errors: %s", render(p))
			}
		}
	}
	fx.none(t, "ws/pb.work")
}

// A dependency's file is addressed by the client's capability: under
// the pb-module scheme, served with the bytes the build read, where
// the client offers workspace/textDocumentContent; else as a file of
// the dependency source store, copied there, the well-known set
// beside it under its digest (REQ-lsp-dependency-files).
func TestDependencyAddress(t *testing.T) {
	broken := stdModule()
	broken["std.proto"] = "syntax = \"proto3\";\npackage std;\nimport \"nowhere.proto\";\nmessage S {\n  string v = 1;\n}\n"
	for _, content := range []bool{true, false} {
		t.Run(fmt.Sprintf("content=%v", content), func(t *testing.T) {
			fx := newFixture(t, checkTree())
			zip, _ := fetchtest.ModuleZip(t, broken)
			fx.Endpoint("example.com/std", "v1.0.0", "zip", string(zip))
			fx.pin(t)
			fx.start(t)
			caps := protocol.ClientCapabilities{}
			if content {
				caps.Workspace = &protocol.WorkspaceClientCapabilities{TextDocumentContent: &protocol.TextDocumentContentClientCapabilities{}}
			}
			res := fx.initialize(t, caps)
			want := uri.MustParse("pb-module://example.com/std@v1.0.0/std.proto")
			if !content {
				want = uri.File(filepath.Join(fx.sources, "example.com", "std@v1.0.0", "std.proto"))
			}
			deadline := time.After(20 * time.Second)
			var p *protocol.PublishDiagnosticsParams
			for p == nil {
				select {
				case got := <-fx.client.publishes:
					if got.URI == want {
						p = got
					}
				case m := <-fx.client.messages:
					t.Fatalf("the server reported: %s", m)
				case <-deadline:
					t.Fatalf("no publish for %s", want)
				}
			}
			if len(p.Diagnostics) != 1 || p.Diagnostics[0].Range.Start.Line != 2 || !strings.Contains(string(p.Diagnostics[0].Message.(protocol.String)), "nowhere.proto") {
				t.Fatalf("the dependency file's diagnostic: %s", render(p))
			}
			if content {
				if res.Capabilities.Workspace == nil || res.Capabilities.Workspace.TextDocumentContent == nil {
					t.Fatal("the content scheme was not declared")
				}
				got, err := fx.server.TextDocumentContent(context.Background(), &protocol.TextDocumentContentParams{URI: want})
				if err != nil || got.Text != broken["std.proto"] {
					t.Fatalf("the content served: %q, %v", got, err)
				}
				if _, err := fx.server.TextDocumentContent(context.Background(), &protocol.TextDocumentContentParams{URI: "pb-module://example.com/std@v1.0.0/none.proto"}); err == nil || !strings.Contains(err.Error(), "none.proto") {
					t.Fatalf("a path no module provides: %v", err)
				}
				wk, err := fx.server.TextDocumentContent(context.Background(), &protocol.TextDocumentContentParams{URI: "pb-module://well-known/google/protobuf/empty.proto"})
				if err != nil || !strings.Contains(wk.Text, "message Empty") {
					t.Fatalf("the well-known file served: %v", err)
				}
				if _, err := os.Stat(filepath.Join(fx.sources, "example.com")); !os.IsNotExist(err) {
					t.Fatal("the source store was filled under the content scheme")
				}
				return
			}
			if res.Capabilities.Workspace != nil {
				t.Fatal("the content scheme was declared to a client not offering the request")
			}
			if b, err := os.ReadFile(want.FsPath()); err != nil || string(b) != broken["std.proto"] {
				t.Fatalf("the copy: %q, %v", b, err)
			}
			// The copy opened as a document is outside the build: a
			// dependency's file, read as the build read it.
			if err := fx.server.DidOpen(context.Background(), &protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: want, LanguageID: "proto", Version: 1, Text: broken["std.proto"]}}); err != nil {
				t.Fatal(err)
			}
			deadline = time.After(20 * time.Second)
			for seen := false; !seen; {
				select {
				case got := <-fx.client.publishes:
					if got.URI != want {
						continue
					}
					if v, _ := got.Version.Get(); v != 1 || len(got.Diagnostics) != 2 {
						t.Fatalf("the copy as a document: %s", render(got))
					}
					var reason bool
					for _, d := range got.Diagnostics {
						reason = reason || d.Severity == protocol.DiagnosticSeverityInformation && strings.Contains(string(d.Message.(protocol.String)), "a dependency's file")
					}
					if !reason {
						t.Fatalf("the copy as a document, its reason: %s", render(got))
					}
					seen = true
				case <-deadline:
					t.Fatal("no publish for the copy opened as a document")
				}
			}
			entries, err := os.ReadDir(fx.sources)
			if err != nil {
				t.Fatal(err)
			}
			var wkDir string
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "well-known@") {
					wkDir = e.Name()
				}
			}
			if wkDir == "" || len(strings.TrimPrefix(wkDir, "well-known@")) != 64 {
				t.Fatalf("the well-known copy: %v", entries)
			}
			if _, err := os.Stat(filepath.Join(fx.sources, wkDir, "google", "protobuf", "any.proto")); err != nil {
				t.Fatalf("the well-known copy's files: %v", err)
			}
			// A copy that differs is replaced at the next load.
			if err := os.WriteFile(want.FsPath(), []byte("tampered"), 0o644); err != nil {
				t.Fatal(err)
			}
			fx.watched(t, "ws/pb.lock", protocol.FileChangeTypeChanged)
			deadline = time.After(20 * time.Second)
			for {
				b, err := os.ReadFile(want.FsPath())
				if err == nil && string(b) == broken["std.proto"] {
					break
				}
				select {
				case <-deadline:
					t.Fatalf("the tampered copy was not replaced: %q", b)
				case <-time.After(50 * time.Millisecond):
				}
			}
		})
	}
}

// The lifecycle: a request before initialize is refused as not
// initialized and a notification dropped; after shutdown a request
// is invalid; exit ends the connection with status 0 after shutdown
// and 1 without; initialize settles the encoding and the
// capabilities (REQ-lsp-lifecycle, the position encoding term).
func TestLifecycle(t *testing.T) {
	fx := newFixture(t, checkTree())
	fx.pin(t)
	fx.start(t)
	var jerr *jsonrpc2.Error
	if err := fx.server.Shutdown(context.Background()); err == nil || !errorsAs(err, &jerr) || jerr.Code != jsonrpc2.Code(protocol.ErrorCodesServerNotInitialized) {
		t.Fatalf("a request before initialize: %v", err)
	}
	fx.open(t, "ws/a/a.proto", 1, "")
	fx.none(t, "ws/a/a.proto")
	res, err := fx.server.Initialize(context.Background(), &protocol.InitializeParams{
		WorkspaceFoldersInitializeParams: protocol.WorkspaceFoldersInitializeParams{WorkspaceFolders: protocol.NewNullable([]protocol.WorkspaceFolder{{URI: fx.uri("ws"), Name: "ws"}})},
		Capabilities: protocol.ClientCapabilities{
			General:   &protocol.GeneralClientCapabilities{PositionEncodings: []protocol.PositionEncodingKind{protocol.PositionEncodingKindUTF16, protocol.PositionEncodingKindUTF8}},
			Workspace: &protocol.WorkspaceClientCapabilities{DidChangeWatchedFiles: &protocol.DidChangeWatchedFilesClientCapabilities{DynamicRegistration: ptr(true)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Capabilities.PositionEncoding != protocol.PositionEncodingKindUTF8 {
		t.Fatalf("the encoding selected: %v", res.Capabilities.PositionEncoding)
	}
	sync, ok := res.Capabilities.TextDocumentSync.(*protocol.TextDocumentSyncOptions)
	if !ok || sync.OpenClose == nil || !*sync.OpenClose || sync.Change == nil || *sync.Change != protocol.TextDocumentSyncKindFull || sync.Save != protocol.Boolean(true) {
		t.Fatalf("the sync capability: %#v", res.Capabilities.TextDocumentSync)
	}
	if res.Capabilities.DefinitionProvider != nil || res.Capabilities.HoverProvider != nil || res.Capabilities.ReferencesProvider != nil || res.Capabilities.DocumentFormattingProvider != nil {
		t.Fatal("a capability this server does not serve was declared")
	}
	if _, err := fx.server.Initialize(context.Background(), &protocol.InitializeParams{}); err == nil {
		t.Fatal("a second initialize was accepted")
	}
	if err := fx.server.Initialized(context.Background(), &protocol.InitializedParams{}); err != nil {
		t.Fatal(err)
	}
	select {
	case reg := <-fx.client.registered:
		if len(reg.Registrations) != 1 || reg.Registrations[0].Method != protocol.MethodWorkspaceDidChangeWatchedFiles || !strings.Contains(string(reg.Registrations[0].RegisterOptions), "**/pb.lock") {
			t.Fatalf("the registration: %#v", reg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no watcher registration")
	}
	fx.publishes(t, "ws/a/a.proto", "ws/b/b.proto", "ws/pb.work")
	if err := fx.server.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := fx.server.Shutdown(context.Background()); err == nil || !errorsAs(err, &jerr) || jerr.Code != jsonrpc2.Code(protocol.ErrorCodesInvalidRequest) {
		t.Fatalf("a request after shutdown: %v", err)
	}
	if err := fx.server.Exit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := fx.wait(t); status != 0 {
		t.Fatalf("status after shutdown and exit: %d", status)
	}

	// Exit without shutdown: status 1.
	fx = newFixture(t, checkTree())
	fx.start(t)
	if err := fx.server.Exit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := fx.wait(t); status != 1 {
		t.Fatalf("status after exit alone: %d", status)
	}
}

func errorsAs(err error, target **jsonrpc2.Error) bool {
	for err != nil {
		if e, ok := err.(*jsonrpc2.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// A frame whose body is no JSON-RPC message is answered with the
// protocol's error and the connection goes on; the stream's end
// ends the server, status 1 without a shutdown (REQ-lsp-transport).
func TestTransportMalformedFrame(t *testing.T) {
	fx := newFixture(t, checkTree())
	var err error
	if fx.srv, err = New(Deps{WS: fx.ws, OSRoot: fx.root, Client: assemble.Client(fx.Fixture, "proxy"), Sources: fx.sources}); err != nil {
		t.Fatal(err)
	}
	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()
	status := make(chan int, 1)
	go func() { status <- Serve(context.Background(), fx.srv, toServer, fromServer) }()
	write := func(body string) {
		if _, err := fmt.Fprintf(fromClient, "Content-Length: %d\r\n\r\n%s", len(body), body); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		var length int
		for {
			var l []byte
			b := make([]byte, 1)
			for {
				if _, err := toClient.Read(b); err != nil {
					t.Fatal(err)
				}
				l = append(l, b[0])
				if strings.HasSuffix(string(l), "\r\n") {
					break
				}
			}
			h := strings.TrimSuffix(string(l), "\r\n")
			if h == "" {
				break
			}
			if v, ok := strings.CutPrefix(h, "Content-Length: "); ok {
				length, _ = strconv.Atoi(v)
			}
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(toClient, body); err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	write("{not json")
	if r := read(); !strings.Contains(r, `"code":-32700`) {
		t.Fatalf("the parse error's answer: %s", r)
	}
	write(`{"jsonrpc":"2.0","id":1,"method":"shutdown"}`)
	if r := read(); !strings.Contains(r, `"id":1`) || !strings.Contains(r, `-32002`) {
		t.Fatalf("the connection after the malformed frame: %s", r)
	}
	fromClient.Close()
	select {
	case st := <-status:
		if st != 1 {
			t.Fatalf("status after the stream's end: %d", st)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not end with the stream")
	}
}
