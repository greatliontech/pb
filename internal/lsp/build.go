package lsp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"sort"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/proto/compile"
	"github.com/greatliontech/pb/internal/proto/modfiles"
)

// load loads the session of the client root as a check verb loads
// its own, read-only (REQ-lsp-session).
func (s *Server) load() (*dep.Session, error) {
	if !s.hasRoot {
		return nil, errors.New("the client names no directory")
	}
	return dep.Load(dep.Config{WS: s.deps.WS, Dir: s.clientRoot, Client: s.deps.Client, ReadOnly: true})
}

// judge is one judgement for the change gen: the session loaded again
// where reload says so — a reload that fails keeps the last session
// loaded (REQ-lsp-reload) — the build judged with the documents
// overlaid, the publish set computed and published; unless a later
// change superseded it, in which case nothing of it reaches the
// client (REQ-lsp-diagnostics, REQ-lsp-fresh).
func (s *Server) judge(ctx context.Context, gen uint64, reload bool) {
	s.mu.Lock()
	sess := s.sess
	docs := make(map[uri.URI]*document, len(s.docs))
	for u, d := range s.docs {
		docs[u] = &document{version: d.version, text: d.text, proto: d.proto}
	}
	content := s.content
	s.mu.Unlock()

	// The session is loaded on a reload alone — the first judgement is
	// one (initialized) — so a start that loaded none serves with no
	// build until the next reload, reporting the failure once per try.
	var loadErr error
	loaded := false
	if reload {
		fresh, err := s.load()
		if err == nil {
			sess, loaded = fresh, true
		} else {
			loadErr = err
			if sess != nil {
				// The kept session's client may have been rewired by a
				// load a superseded judgement made: wired back.
				sess.Wire()
			}
		}
	}

	var j *dep.Judgement
	var unpinned []dep.Pair
	var judgeErr error
	if sess != nil {
		unpinned, judgeErr = sess.Unpinned()
		if judgeErr == nil && len(unpinned) == 0 {
			j, judgeErr = dep.Judge(ctx, sess, s.overlay(sess, docs))
			if pair, ok := dep.UnpinnedIn(judgeErr); ok {
				unpinned, judgeErr, j = []dep.Pair{pair}, nil, nil
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	// The dependency source store is filled at the session's load,
	// where the client addresses dependency files by file URI
	// (REQ-lsp-dependency-files).
	if j != nil && loaded && !content {
		if err := s.copySources(j.Mods); err != nil {
			s.deps.Logger.Error("the dependency source store could not be filled", "error", err)
			s.showMessage("the dependency source store could not be filled: " + err.Error())
		}
	}
	placements := s.placements(sess, docs, j, unpinned, judgeErr != nil)
	if s.hold != nil {
		s.hold(gen)
	}

	s.mu.Lock()
	if s.gen != gen || s.state != serving {
		s.mu.Unlock()
		return
	}
	s.sess = sess
	s.mods = nil
	if j != nil {
		s.mods = j.Mods
	}
	if reload {
		s.reloadPending = false
	}
	for u, d := range docs {
		if d.proto {
			s.standing[u] = placements.standing[u]
		}
	}
	publishes := s.publishSet(placements.lists, docs)
	s.mu.Unlock()

	switch {
	case loadErr != nil:
		s.showMessage("the session could not be loaded: " + loadErr.Error())
	case judgeErr != nil:
		s.showMessage("the build could not be judged: " + judgeErr.Error())
	}
	for _, p := range publishes {
		if err := s.client.PublishDiagnostics(context.Background(), p); err != nil {
			s.deps.Logger.Error("publishing diagnostics", "uri", p.URI, "error", err)
		}
	}
}

// showMessage reports a failure to the client (REQ-lsp-session).
func (s *Server) showMessage(msg string) {
	if err := s.client.ShowMessage(context.Background(), &protocol.ShowMessageParams{Type: protocol.MessageTypeError, Message: msg}); err != nil {
		s.deps.Logger.Error("showing a message", "error", err)
	}
}

// overlay is the documents under the resolution root, by
// root-relative path (REQ-lsp-overlay); the judgement's assembly
// keeps those a module of the build owns.
func (s *Server) overlay(sess *dep.Session, docs map[uri.URI]*document) dep.Overlay {
	o := dep.Overlay{}
	for u, d := range docs {
		if !d.proto {
			continue
		}
		tree, ok := s.treePath(u)
		if !ok {
			continue
		}
		rel, ok := sess.RootRel(tree)
		if !ok {
			continue
		}
		o[rel] = d.text
	}
	return o
}

// placed is a judgement's placements: the diagnostics per file URI,
// and per document whether the judgement found it a build file.
type placed struct {
	lists    map[uri.URI][]protocol.Diagnostic
	standing map[uri.URI]bool
}

// placements places the judgement's diagnostics (lsp.md, the
// placement term): the build's own — the compile's errors or the
// lint's findings — each at its placement; the unpinned diagnostics
// on the lockfile, else the build home; and every outside `.proto`
// document's on itself.
func (s *Server) placements(sess *dep.Session, docs map[uri.URI]*document, j *dep.Judgement, unpinned []dep.Pair, failed bool) placed {
	out := placed{lists: map[uri.URI][]protocol.Diagnostic{}, standing: map[uri.URI]bool{}}
	add := func(u uri.URI, d protocol.Diagnostic) { out.lists[u] = append(out.lists[u], d) }

	// Each document's standing and, where outside, its diagnostic.
	for u, d := range docs {
		if !d.proto {
			continue
		}
		reason, outside := s.outside(sess, j, u)
		out.standing[u] = !outside
		if outside {
			add(u, protocol.Diagnostic{
				Range:    rangeAt(d.text, 0, 0, s.enc),
				Severity: protocol.DiagnosticSeverityInformation,
				Source:   protocol.NewOptional("pb"),
				Message:  protocol.String("outside the build: " + reason),
			})
		}
	}
	if sess == nil {
		return out
	}
	for _, p := range unpinned {
		home := sess.BuildHome()
		if _, err := s.deps.WS.Stat(sess.LockPath()); err == nil {
			home = sess.LockPath()
		}
		add(s.fileURI(home), protocol.Diagnostic{
			Range:    protocol.Range{},
			Severity: protocol.DiagnosticSeverityError,
			Code:     protocol.String("unpinned"),
			Source:   protocol.NewOptional("pb"),
			Message:  protocol.String(fmt.Sprintf("%s is required and not pinned by the lockfile; run pb dep download", p)),
		})
	}
	if j == nil || failed {
		return out
	}
	texts := s.texts(sess, j.Mods, docs)
	if !j.Compiles() {
		for _, e := range j.Compile {
			if e.Path == "" {
				add(s.fileURI(sess.BuildHome()), s.diagnostic(nil, 0, 0, protocol.DiagnosticSeverityError, "compile", e.Message))
				continue
			}
			u, text := texts.at(e.Path)
			start, end := 0, 0
			if e.Line > 0 {
				start, end = e.Offset, e.End
				if end <= start {
					end = tokenEnd(text, start)
				}
			}
			add(u, s.diagnostic(text, start, end, protocol.DiagnosticSeverityError, "compile", e.Message))
		}
		return out
	}
	dirs := map[string]bool{}
	for _, m := range j.Mods {
		if m.Local {
			dirs[m.Dir] = true
		}
	}
	for _, f := range j.Findings {
		sev := protocol.DiagnosticSeverityWarning
		if f.Severity == check.SeverityError {
			sev = protocol.DiagnosticSeverityError
		}
		switch {
		case f.Path == "":
			add(s.fileURI(sess.BuildHome()), s.diagnostic(nil, 0, 0, sev, f.Rule, f.Message))
		case f.Line == 0 && dirs[f.Path]:
			// A module's own selection locates its set and package
			// findings at the module's directory: its module file.
			add(s.fileURI(sess.ModuleFile(f.Path)), s.diagnostic(nil, 0, 0, sev, f.Rule, f.Message))
		default:
			u, text := texts.at(f.Path)
			start := 0
			if f.Line > 0 {
				start = offsetOf(text, f.Line, f.Column)
			}
			add(u, s.diagnostic(text, start, tokenEnd(text, start), sev, f.Rule, f.Message))
		}
	}
	return out
}

// diagnostic is one diagnostic of source pb at a byte range of text.
func (s *Server) diagnostic(text []byte, start, end int, sev protocol.DiagnosticSeverity, code, msg string) protocol.Diagnostic {
	return protocol.Diagnostic{
		Range:    rangeAt(text, start, end, s.enc),
		Severity: sev,
		Code:     protocol.String(code),
		Source:   protocol.NewOptional("pb"),
		Message:  protocol.String(msg),
	}
}

// texts resolves a build's include-root-relative path to the file's
// address and the bytes the judgement read: a tree file's at its
// file URI, the document's text where one is open; a dependency's at
// its dependency address with the bytes the build read; a well-known
// file's likewise.
type texts struct {
	s     *Server
	sess  *dep.Session
	mods  []modfiles.Module
	index map[string]int
	docs  map[uri.URI]*document
}

func (s *Server) texts(sess *dep.Session, mods []modfiles.Module, docs map[uri.URI]*document) *texts {
	index, err := compile.Providers(mods)
	if err != nil {
		index = map[string]int{}
	}
	return &texts{s: s, sess: sess, mods: mods, index: index, docs: docs}
}

func (t *texts) at(p string) (uri.URI, []byte) {
	i, ok := t.index[p]
	if !ok {
		// A well-known file, or one no module of the build provides.
		if modfiles.WellKnown(p) {
			return t.s.wellKnownURI(p), t.s.wellKnown.files[p]
		}
		return t.s.fileURI(t.sess.BuildHome()), nil
	}
	m := t.mods[i]
	if m.Local || m.Dir != "" {
		tree := path.Join(t.sess.Root.Dir, m.Dir, p)
		u := t.s.fileURI(tree)
		if d := t.docs[u]; d != nil && d.proto {
			return u, d.text
		}
		return u, m.Files[p]
	}
	return t.s.moduleURI(m, p), m.Files[p]
}

// outside tells whether a `.proto` document is outside the build and
// the first reason that applies (lsp.md, the outside-the-build term).
func (s *Server) outside(sess *dep.Session, j *dep.Judgement, u uri.URI) (string, bool) {
	if sess == nil || j == nil {
		return "no build is loaded", true
	}
	if u.Scheme() == moduleScheme {
		return "a dependency's file, read as the build read it", true
	}
	if s.deps.Sources != "" && u.IsFile() {
		if _, err := relPath(s.deps.Sources, u.FsPath()); err == nil {
			return "a dependency's file, read as the build read it", true
		}
	}
	tree, ok := s.treePath(u)
	if !ok {
		return "outside the resolution root " + sess.Root.Dir, true
	}
	rel, ok := sess.RootRel(tree)
	if !ok {
		return "outside the resolution root " + sess.Root.Dir, true
	}
	_, file, ok := sess.FileOf(j.Mods, rel)
	if !ok {
		return "in no workspace module and no directory replacement", true
	}
	if modfiles.WellKnown(file) {
		return "a workspace copy of a well-known import, which the build reads from the toolchain", true
	}
	return "", false
}

// publishSet is the judgement's publishes over the publish set
// (lsp.md, the publish set term): every file the judgement places
// on, and every file whose last publish carried a non-empty list,
// the latter with an empty list where nothing lands now; a member
// whose list equals its last publish and whose document's version
// has not moved is not published again. The client's state is
// updated as the publishes are made. Called under the lock.
func (s *Server) publishSet(lists map[uri.URI][]protocol.Diagnostic, docs map[uri.URI]*document) []*protocol.PublishDiagnosticsParams {
	members := map[uri.URI]bool{}
	for u := range lists {
		members[u] = true
	}
	for u := range s.published {
		members[u] = true
	}
	ordered := make([]uri.URI, 0, len(members))
	for u := range members {
		ordered = append(ordered, u)
	}
	sort.Slice(ordered, func(a, b int) bool { return ordered[a] < ordered[b] })
	var out []*protocol.PublishDiagnosticsParams
	for _, u := range ordered {
		list := lists[u]
		if list == nil {
			list = []protocol.Diagnostic{}
		}
		encoded, err := protocol.Marshal(list)
		if err != nil {
			s.deps.Logger.Error("encoding diagnostics", "uri", u, "error", err)
			continue
		}
		last, had := s.published[u]
		doc := docs[u]
		open := doc != nil
		var version int32
		if open {
			version = doc.version
		}
		if had && bytes.Equal(last.list, encoded) && last.open == open && last.version == version {
			continue
		}
		p := &protocol.PublishDiagnosticsParams{URI: u, Diagnostics: list}
		if open {
			p.Version = protocol.NewOptional(version)
		}
		out = append(out, p)
		if len(list) == 0 {
			delete(s.published, u)
		} else {
			s.published[u] = publishState{list: encoded, version: version, open: open}
		}
	}
	return out
}
