package lsp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"sort"

	"github.com/greatliontech/lsp/protocol"
	"github.com/greatliontech/lsp/uri"

	"github.com/greatliontech/pb/internal/check"
	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/module/workspace"
	"github.com/greatliontech/pb/internal/proto/compile"
	"github.com/greatliontech/pb/internal/proto/modfiles"
)

// loadRoot reads the resolution root of the client root, as a check
// verb finds its own (REQ-lsp-session).
func (s *Server) loadRoot() (*workspace.Root, error) {
	if !s.hasRoot {
		return nil, errors.New("the client names no directory")
	}
	return dep.LoadRoot(s.config())
}

// load loads the session of a read root as a check verb loads its
// own, read-only (REQ-lsp-session).
func (s *Server) load(root *workspace.Root) (*dep.Session, error) {
	return dep.LoadFrom(root, s.config())
}

// config is the session's configuration: the tree at the client root,
// the fixture's client, the lockfile never written.
func (s *Server) config() dep.Config {
	return dep.Config{WS: s.deps.WS, Dir: s.clientRoot, Client: s.deps.Client, ReadOnly: true}
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
	root, rootErr := s.root, s.rootErr
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
		fresh, err := (*dep.Session)(nil), rootErr
		if root != nil {
			fresh, err = s.load(root)
		}
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
	var f *buildFiles
	if j != nil {
		f = newFiles(sess.Root.Dir, j.Mods, s.wellKnown.files)
	}
	placements := s.placements(sess, docs, j, f, unpinned, judgeErr != nil)
	// The navigation index, built here on the judgement's goroutine,
	// never on the read loop, and not for a judgement a newer one has
	// already superseded: its commit below would be refused.
	s.mu.Lock()
	superseded := s.gen != gen
	s.mu.Unlock()
	var idx *index
	if !superseded && j != nil && j.Compiles() {
		idx = newIndex(j.Files, f)
	}
	if s.hold != nil {
		s.hold(gen)
	}

	s.mu.Lock()
	if s.gen != gen || s.state != serving {
		s.mu.Unlock()
		return
	}
	s.sess = sess
	s.files = f
	if idx != nil {
		s.index = idx
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
func (s *Server) placements(sess *dep.Session, docs map[uri.URI]*document, j *dep.Judgement, f *buildFiles, unpinned []dep.Pair, failed bool) placed {
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
	if len(unpinned) > 0 {
		home := sess.BuildHome()
		if _, err := s.deps.WS.Stat(sess.LockPath()); err == nil {
			home = sess.LockPath()
		}
		for _, p := range unpinned {
			add(s.fileURI(home), s.diagnostic(nil, 0, 0, protocol.DiagnosticSeverityError, "unpinned", fmt.Sprintf("%s is required and not pinned by the lockfile; run pb dep download", p)))
		}
	}
	if j == nil || failed {
		return out
	}
	if !j.Compiles() {
		for _, e := range j.Compile {
			if e.Path == "" {
				add(s.fileURI(sess.BuildHome()), s.diagnostic(nil, 0, 0, protocol.DiagnosticSeverityError, "compile", e.Message))
				continue
			}
			u, text := s.at(f, sess, e.Path)
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
	for _, finding := range j.Findings {
		sev := protocol.DiagnosticSeverityWarning
		if finding.Severity == check.SeverityError {
			sev = protocol.DiagnosticSeverityError
		}
		switch {
		case finding.Path == "":
			add(s.fileURI(sess.BuildHome()), s.diagnostic(nil, 0, 0, sev, finding.Rule, finding.Message))
		case finding.Line == 0 && dirs[finding.Path]:
			// A module's own selection locates its set and package
			// findings at the module's directory: its module file.
			add(s.fileURI(sess.ModuleFile(finding.Path)), s.diagnostic(nil, 0, 0, sev, finding.Rule, finding.Message))
		default:
			u, text := s.at(f, sess, finding.Path)
			start := 0
			if finding.Line > 0 {
				start = offsetOf(text, finding.Line, finding.Column)
			}
			add(u, s.diagnostic(text, start, tokenEnd(text, start), sev, finding.Rule, finding.Message))
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

// buildFiles is a judgement's files by include-root-relative path — each
// one's origin, by which its address is spelled, and the bytes the
// judgement read, the overlay's where a document is open — and the
// working tree's by their tree path: the one table the placements,
// the navigation index and the content requests read
// (REQ-lsp-dependency-files). A well-known path is the toolchain's:
// a module's copy provides nothing (lsp.md, the build-file term).
type buildFiles struct {
	byPath map[string]file
	byTree map[string]string // tree path → include-root-relative path
}

// file is one file of a judgement: its origin and its bytes.
type file struct {
	origin origin
	text   []byte
}

// origin is where a file came from, as REQ-lsp-dependency-files
// addresses it: a tree path for a file the build read from the
// working tree, else the pair whose bytes the file is — a module's
// source, its pinned replacement's where one applies — else the
// well-known set's.
type origin struct {
	tree      string
	modPath   string
	version   string
	wellKnown bool
}

// newFiles tabulates a judgement's modules and the well-known set;
// rootDir is the resolution root's directory within the working
// tree, from which a tree file's path is spelled.
func newFiles(rootDir string, mods []modfiles.Module, wellKnown map[string][]byte) *buildFiles {
	f := &buildFiles{byPath: map[string]file{}, byTree: map[string]string{}}
	if providers, err := compile.Providers(mods); err == nil {
		for p, i := range providers {
			m := mods[i]
			o := origin{modPath: m.SourcePath, version: m.SourceVersion}
			if m.FromTree() {
				o = origin{tree: path.Join(rootDir, m.Dir, p)}
				f.byTree[o.tree] = p
			}
			f.byPath[p] = file{origin: o, text: m.Files[p]}
		}
	}
	// No module provides a well-known path (Module.Protos leaves a
	// copy out), so the set's entries meet none.
	for p, b := range wellKnown {
		f.byPath[p] = file{origin: origin{wellKnown: true}, text: b}
	}
	return f
}

// address is a file's URI by its origin: a tree file's file URI, a
// dependency's address by the client's capability, a well-known
// file's likewise.
func (s *Server) address(o origin, p string) uri.URI {
	switch {
	case o.wellKnown:
		return s.wellKnownURI(p)
	case o.tree != "":
		return s.fileURI(o.tree)
	}
	return s.moduleURI(o.modPath, o.version, p)
}

// at is a build path's address and bytes from the table; a path no
// module of the build provides is placed at the build home with no
// bytes.
func (s *Server) at(f *buildFiles, sess *dep.Session, p string) (uri.URI, []byte) {
	if bf, ok := f.byPath[p]; ok {
		return s.address(bf.origin, p), bf.text
	}
	return s.fileURI(sess.BuildHome()), nil
}

// dependencyReason is the outside-the-build reason of a dependency's
// file, addressed or read from the source store.
const dependencyReason = "a dependency's file, read as the build read it"

// outside tells whether a `.proto` document is outside the build and
// the first reason that applies (lsp.md, the outside-the-build term).
func (s *Server) outside(sess *dep.Session, j *dep.Judgement, u uri.URI) (string, bool) {
	if sess == nil || j == nil {
		return "no build is loaded", true
	}
	if u.Scheme() == moduleScheme {
		return dependencyReason, true
	}
	_, file, reason := s.treeFile(sess.Tree, j.Mods, u)
	if reason != "" {
		return reason, true
	}
	if modfiles.WellKnown(file) {
		return "a workspace copy of a well-known import, which the build reads from the toolchain", true
	}
	return "", false
}

// treeFile places a document's file in a module of the tree — the
// build's modules, or the root's members alone — as the one
// membership rule has it (dep.Tree.FileOf): the path from the root
// and within the module, or the first reason it is no module's — a
// dependency's file read from the source store, a file outside the
// resolution root, one in no module.
func (s *Server) treeFile(tree dep.Tree, mods []modfiles.Module, u uri.URI) (rel, file, reason string) {
	if s.deps.Sources != "" && u.IsFile() {
		if _, err := relPath(s.deps.Sources, u.FsPath()); err == nil {
			return "", "", dependencyReason
		}
	}
	treePath, ok := s.treePath(u)
	if !ok {
		return "", "", "outside the resolution root " + tree.Root.Dir
	}
	rel, ok = tree.RootRel(treePath)
	if !ok {
		return "", "", "outside the resolution root " + tree.Root.Dir
	}
	_, file, ok = tree.FileOf(mods, rel)
	if !ok {
		return "", "", "in no workspace module and no directory replacement"
	}
	return rel, file, ""
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
