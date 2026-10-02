// Package lsp is pb's language server (docs/specs/lsp.md): the verbs'
// engines behind the Language Server Protocol. The session is loaded
// read-only as a check verb loads its own, open documents are
// overlaid on the tree, and every change is judged by dep.Judge — the
// lint verb's own assembly, compile and evaluation — the judgement
// published as diagnostics over the publish set. The binding is
// go.lsp.dev's; the server implements the protocol's Server interface
// for the methods it serves and refuses the rest as unimplemented.
package lsp

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-git/go-billy/v6"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/greatliontech/pb/internal/check/lintfile"
	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/module"
	"github.com/greatliontech/pb/internal/module/workspace"
	"github.com/greatliontech/pb/internal/proto/modfiles"
	"github.com/greatliontech/pb/internal/provenance/trust"
	"github.com/greatliontech/pb/internal/source/fetch"
)

// Deps is what the server is built over: the working tree — opened
// at the client root's volume once initialize names it (OpenTree),
// or given rooted at OSRoot — the fetch-verify client every session
// is loaded with, the dependency source store's directory on the
// host, and the logger (standard error, under the verb).
type Deps struct {
	WS       billy.Filesystem
	OSRoot   string
	OpenTree func(osRoot string) billy.Filesystem
	Client   *fetch.Client
	Sources  string
	Logger   *slog.Logger
}

// state is the connection's place in the protocol's lifecycle
// (REQ-lsp-lifecycle).
type state int

const (
	uninitialized state = iota // before initialize
	serving                    // initialize answered
	shutdown                   // shutdown answered
)

// Server is one language server: one connection, one client root.
type Server struct {
	protocol.UnimplementedServer

	deps   Deps
	conn   jsonrpc2.Conn
	client protocol.Client

	mu    sync.Mutex
	state state

	// What initialize settled.
	enc        encoding
	content    bool   // the client offers workspace/textDocumentContent
	watch      bool   // the client registers watched files dynamically
	clientRoot string // the client root as a tree path; "" for none
	hasRoot    bool

	docs          map[uri.URI]*document
	published     map[uri.URI]publishState // the client's state per file
	gen           uint64                   // the change the current judgement is for
	cancel        context.CancelFunc       // the running judgement's
	ended         chan struct{}            // closed when the running judgement has ended
	running       sync.WaitGroup           // the judgements started and not yet ended
	reloadPending bool                     // a reload asked for and not yet committed by a judgement
	sess          *dep.Session             // the last session loaded; nil before the first and after a failed initialize
	mods          []modfiles.Module        // the last committed judgement's modules, which dependency content is served from
	standing      map[uri.URI]bool         // per document, whether the last judgement found it a build file; absent until one has
	wellKnown     *wellKnownCopy           // the toolchain's set, digested once

	// hold, when set, is called by a judgement once computed and
	// before it is published: the tests' seam for a judgement held
	// while a later change supersedes it.
	hold func(gen uint64)
}

// document is a file the client has opened, at the client's version;
// the text is held for a `.proto` document alone, the contents of any
// other feeding no judgement (REQ-lsp-outside).
type document struct {
	version int32
	text    []byte
	proto   bool
}

// publishState is what the client holds for a file: the diagnostics
// of its last non-empty publish, as marshalled, and the version the
// publish carried.
type publishState struct {
	list    []byte
	version int32
	open    bool
}

// New is a server over deps, before any connection; the toolchain's
// well-known set is read and digested here, once.
func New(deps Deps) (*Server, error) {
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	wk, err := wellKnownSet(deps.Sources)
	if err != nil {
		return nil, err
	}
	return &Server{deps: deps, docs: map[uri.URI]*document{}, published: map[uri.URI]publishState{}, standing: map[uri.URI]bool{}, wellKnown: wk}, nil
}

// serve runs the connection over stream to its end and returns the
// exit status (REQ-lsp-lifecycle).
func (s *Server) serve(ctx context.Context, stream jsonrpc2.Stream) int {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	conn := jsonrpc2.NewConn(stream, jsonrpc2.WithCodec(codec{}))
	s.mu.Lock()
	s.conn, s.client = conn, protocol.ClientDispatcher(conn)
	s.mu.Unlock()
	conn.Go(ctx, s.handler())
	<-conn.Done()
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	st := s.state
	s.state = shutdown
	s.mu.Unlock()
	// A judgement in flight ends with the connection, before serve
	// returns: nothing of the server outlives it.
	s.running.Wait()
	if st == shutdown {
		return 0
	}
	return 1
}

// handler is the connection's handler chain: the lifecycle's guard,
// then the protocol's dispatch to the server's methods, a
// notification's error — a method the dispatch does not know,
// `$/cancelRequest` among them, or params that do not decode —
// logged and dropped, since the connection would otherwise end on it
// (REQ-lsp-lifecycle; a call's error is its answer, a method unknown
// refused by Request). A cancellation is read only once the request
// it names has been answered, handlers running in wire order on the
// read loop, so its work has always finished; the binding's own
// cancel handler is not used, deriving as it does a context from
// the pooled request context, which the connection recycles under
// it. The work a change starts runs on its own goroutine (judge),
// and a request the server makes of the client is made from one
// too, so the loop is never held.
func (s *Server) handler() jsonrpc2.Handler {
	routed := protocol.ServerHandler(s, nil)
	dispatch := func(ctx context.Context, req *jsonrpc2.Request) (any, error) {
		result, err := routed(ctx, req)
		if err != nil && !req.IsCall() {
			s.deps.Logger.Warn("a notification was dropped", "method", req.Method(), "error", err)
			return nil, nil
		}
		return result, err
	}
	return func(ctx context.Context, req *jsonrpc2.Request) (any, error) {
		s.mu.Lock()
		st := s.state
		s.mu.Unlock()
		switch req.Method() {
		case protocol.MethodExit:
		case protocol.MethodInitialize:
			if st != uninitialized {
				return nil, jsonrpc2.ErrInvalidRequest
			}
		default:
			switch st {
			case uninitialized:
				if req.IsCall() {
					return nil, jsonrpc2.NewError(jsonrpc2.Code(protocol.ErrorCodesServerNotInitialized), "server not initialized")
				}
				return nil, nil
			case shutdown:
				if req.IsCall() {
					return nil, jsonrpc2.ErrInvalidRequest
				}
				return nil, nil
			}
		}
		return dispatch(ctx, req)
	}
}

// Request refuses a call of a method the server does not serve.
func (s *Server) Request(ctx context.Context, method string, params any) (any, error) {
	return nil, jsonrpc2.ErrMethodNotFound
}

// Initialize settles the connection: the client root, the position
// encoding, what the client offers (REQ-lsp-session's load follows
// on `initialized`, REQ-lsp-lifecycle).
func (s *Server) Initialize(ctx context.Context, params *protocol.InitializeParams) (*protocol.InitializeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clientRoot, s.hasRoot = s.clientRootOf(params)
	if params.Capabilities.General != nil {
		s.enc = selectEncoding(params.Capabilities.General.PositionEncodings)
	}
	if w := params.Capabilities.Workspace; w != nil {
		s.content = w.TextDocumentContent != nil
		s.watch = w.DidChangeWatchedFiles != nil && w.DidChangeWatchedFiles.DynamicRegistration != nil && *w.DidChangeWatchedFiles.DynamicRegistration
	}
	s.state = serving
	yes := true
	caps := protocol.ServerCapabilities{
		PositionEncoding: s.enc.kind(),
		TextDocumentSync: &protocol.TextDocumentSyncOptions{
			OpenClose: &yes,
			Change:    ptr(protocol.TextDocumentSyncKindFull),
			Save:      protocol.Boolean(true),
		},
	}
	if s.content {
		caps.Workspace = &protocol.WorkspaceOptions{TextDocumentContent: &protocol.TextDocumentContentOptions{Schemes: []string{moduleScheme}}}
	}
	return &protocol.InitializeResult{Capabilities: caps, ServerInfo: protocol.ServerInfo{Name: "pb"}}, nil
}

// clientRootOf is the client root as a tree path (lsp.md, the client
// root term): the first workspace folder, else the root URI, a path
// under the tree's host root; none, or one the tree does not hold,
// names no directory.
func (s *Server) clientRootOf(params *protocol.InitializeParams) (string, bool) {
	var u uri.URI
	if folders, ok := params.WorkspaceFolders.Get(); ok && len(folders) > 0 {
		u = folders[0].URI
	} else if params.RootURI != nil {
		u = *params.RootURI
	} else {
		return "", false
	}
	if s.deps.WS == nil {
		if !u.IsFile() {
			return "", false
		}
		// The tree is rooted at the client root's own volume, which
		// need not be the process's: a workspace on another volume
		// than the directory the editor started pb in.
		s.deps.OSRoot = filepath.VolumeName(filepath.Clean(u.FsPath())) + string(filepath.Separator)
		s.deps.WS = s.deps.OpenTree(s.deps.OSRoot)
	}
	return s.treePath(u)
}

// treePath is the tree path of a file URI: its host path relative to
// the tree's root, slash-separated; false for a URI of another
// scheme or a path outside the tree.
func (s *Server) treePath(u uri.URI) (string, bool) {
	if !u.IsFile() {
		return "", false
	}
	p := filepath.Clean(u.FsPath())
	root := filepath.Clean(s.deps.OSRoot)
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return "", true
	}
	return filepath.ToSlash(rel), true
}

// fileURI is the file URI of a tree path.
func (s *Server) fileURI(tree string) uri.URI {
	return uri.File(filepath.Join(s.deps.OSRoot, filepath.FromSlash(tree)))
}

// Initialized is the client's go: the watchers registered where the
// client offers it (REQ-lsp-reload), the session loaded and judged
// (REQ-lsp-diagnostics).
func (s *Server) Initialized(ctx context.Context, _ *protocol.InitializedParams) error {
	s.mu.Lock()
	watch := s.watch
	s.mu.Unlock()
	if watch {
		go s.registerWatchers()
	}
	s.change(true)
	return nil
}

// registerWatchers asks the client to report changes to the files
// the resolution reads (REQ-lsp-reload).
func (s *Server) registerWatchers() {
	var watchers []protocol.FileSystemWatcher
	for _, g := range watchedGlobs {
		watchers = append(watchers, protocol.FileSystemWatcher{GlobPattern: protocol.Pattern(g)})
	}
	opts, err := protocol.Marshal(protocol.DidChangeWatchedFilesRegistrationOptions{Watchers: watchers})
	if err != nil {
		s.deps.Logger.Error("the watcher registration could not be encoded", "error", err)
		return
	}
	err = s.client.RegisterCapability(context.Background(), &protocol.RegistrationParams{Registrations: []protocol.Registration{{
		ID: "pb-watched-files", Method: protocol.MethodWorkspaceDidChangeWatchedFiles, RegisterOptions: protocol.LSPAny(opts),
	}}})
	if err != nil {
		s.deps.Logger.Error("the client refused the watcher registration", "error", err)
	}
}

// Shutdown ends every judgement in flight — cancelled and waited for
// — and answers; what follows is refused by the handler's guard
// (REQ-lsp-lifecycle).
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.state = shutdown
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	ended := s.ended
	s.mu.Unlock()
	if ended != nil {
		<-ended
	}
	return nil
}

// Exit ends the connection, whenever it comes; serve then reports the
// status (REQ-lsp-lifecycle).
func (s *Server) Exit(ctx context.Context) error {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	// Close waits for the handlers to drain, this one among them.
	go conn.Close()
	return nil
}

// DidOpen holds the document; a `.proto` document changes what the
// judgement reads or places on (REQ-lsp-overlay, REQ-lsp-outside).
func (s *Server) DidOpen(ctx context.Context, params *protocol.DidOpenTextDocumentParams) error {
	d := params.TextDocument
	proto := isProto(d.URI)
	s.mu.Lock()
	doc := &document{version: d.Version, proto: proto}
	if proto {
		doc.text = []byte(d.Text)
	}
	s.docs[d.URI] = doc
	s.mu.Unlock()
	if proto {
		s.change(false)
	}
	return nil
}

// DidChange takes the document's new contents, whole (the sync is
// Full); a build file's change is judged, an outside document's
// changes nothing (REQ-lsp-diagnostics).
func (s *Server) DidChange(ctx context.Context, params *protocol.DidChangeTextDocumentParams) error {
	s.mu.Lock()
	doc := s.docs[params.TextDocument.URI]
	if doc == nil {
		s.mu.Unlock()
		return nil
	}
	doc.version = params.TextDocument.Version
	if doc.proto {
		for _, c := range params.ContentChanges {
			switch c := c.(type) {
			case *protocol.TextDocumentContentChangeWholeDocument:
				doc.text = []byte(c.Text)
			case *protocol.TextDocumentContentChangePartial:
				// Not offered (Full sync); a client sending one anyway
				// is read for its text as a whole document.
				doc.text = []byte(c.Text)
			}
		}
	}
	// A document no judgement has placed yet is judged: its standing
	// is unknown until one has.
	st, known := s.standing[params.TextDocument.URI]
	judge := doc.proto && (!known || st)
	s.mu.Unlock()
	if judge {
		s.change(false)
	}
	return nil
}

// DidClose lets the tree's file speak again; a `.proto` document's
// close is judged (REQ-lsp-overlay).
func (s *Server) DidClose(ctx context.Context, params *protocol.DidCloseTextDocumentParams) error {
	s.mu.Lock()
	doc := s.docs[params.TextDocument.URI]
	delete(s.docs, params.TextDocument.URI)
	delete(s.standing, params.TextDocument.URI)
	s.mu.Unlock()
	if doc != nil && doc.proto {
		s.change(false)
	}
	return nil
}

// DidSave reloads the session: the saved file may be one the
// resolution reads (REQ-lsp-reload).
func (s *Server) DidSave(ctx context.Context, params *protocol.DidSaveTextDocumentParams) error {
	s.change(true)
	return nil
}

// DidChangeWatchedFiles reloads the session (REQ-lsp-reload).
func (s *Server) DidChangeWatchedFiles(ctx context.Context, params *protocol.DidChangeWatchedFilesParams) error {
	s.change(true)
	return nil
}

// TextDocumentContent serves a dependency's file under the
// `pb-module` scheme with the bytes the build read
// (REQ-lsp-dependency-files).
func (s *Server) TextDocumentContent(ctx context.Context, params *protocol.TextDocumentContentParams) (*protocol.TextDocumentContentResult, error) {
	s.mu.Lock()
	mods := s.mods
	s.mu.Unlock()
	b, err := moduleContent(mods, params.URI)
	if err != nil {
		return nil, err
	}
	return &protocol.TextDocumentContentResult{Text: string(b)}, nil
}

// change records a change to what a judgement reads or places on and
// starts the judgement for it, the running one — now superseded —
// cancelled and waited for first: judgements run one at a time, the
// session's client being one object a load rewires, so no two loads
// or reads of it overlap; reload has the session loaded again first,
// and a reload a later change supersedes is carried to the judgement
// that commits (REQ-lsp-diagnostics, REQ-lsp-reload).
func (s *Server) change(reload bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != serving {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.reloadPending = s.reloadPending || reload
	reload = s.reloadPending
	if reload {
		// What the build reads may have changed under every document:
		// each one's standing is judged afresh.
		s.standing = map[uri.URI]bool{}
	}
	s.gen++
	gen := s.gen
	previous := s.ended
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan struct{})
	s.cancel, s.ended = cancel, ended
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		defer close(ended)
		if previous != nil {
			<-previous
		}
		s.judge(ctx, gen, reload)
	}()
}

// isProto reports whether the URI names a `.proto` file.
func isProto(u uri.URI) bool { return strings.HasSuffix(u.Path(), ".proto") }

func ptr[T any](v T) *T { return &v }

// watchedGlobs names the files the resolution reads, for the client's
// watcher (REQ-lsp-reload): the workspace, module, lint, lock and
// trust files, rule files and protobuf files.
var watchedGlobs = []string{
	"**/" + workspace.FileName, "**/" + module.ModuleFileName, "**/" + lintfile.FileName, "**/" + workspace.LockFileName, "**/" + trust.FileName,
	"**/*" + module.RuleFileSuffix, "**/*" + module.ProtoFileSuffix,
}
