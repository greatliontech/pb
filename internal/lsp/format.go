package lsp

import (
	"bytes"
	"context"

	"github.com/greatliontech/lsp/jsonrpc2"
	"github.com/greatliontech/lsp/protocol"
	"github.com/greatliontech/lsp/uri"

	"github.com/greatliontech/pb/internal/dep"
	"github.com/greatliontech/pb/internal/format"
	"github.com/greatliontech/pb/internal/module/workspace"
	"github.com/greatliontech/pb/internal/proto/modfiles"
)

// Formatting answers the edits that take an own file's document to
// its canonical form (REQ-lsp-formatting): one edit over the whole
// text where the form differs, none where it does not; an error
// naming the parse failure where the document does not parse, as
// `pb format` refuses it. The own files are judged by the resolution
// root as last read — its workspace members, a session or a build
// loaded or not; a document of no own file, or held while no root
// reads, is answered the empty answer. The request's options are
// ignored: the form has none.
func (s *Server) Formatting(ctx context.Context, params *protocol.DocumentFormattingParams) ([]protocol.TextEdit, error) {
	s.mu.Lock()
	doc := s.docs[params.TextDocument.URI]
	root := s.root
	enc := s.enc
	s.mu.Unlock()
	if doc == nil || !doc.proto || root == nil {
		return nil, nil
	}
	rel, ok := s.ownFile(root, params.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	canonical, err := format.Format(rel, doc.text)
	if err != nil {
		return nil, jsonrpc2.NewError(jsonrpc2.Code(protocol.LSPErrorCodesRequestFailed), err.Error())
	}
	if bytes.Equal(canonical, doc.text) {
		return nil, nil
	}
	return []protocol.TextEdit{{Range: rangeAt(doc.text, 0, len(doc.text), enc), NewText: string(canonical)}}, nil
}

// ownFile tells whether a `.proto` document — the kind the server
// holds text for — is an own file of the root (format.md's term): a
// file of the working tree under a workspace member's directory, no
// nested module's and through no link — the one membership rule,
// the judgement's overlay's, over the members alone, so a directory
// replacement's file is none — and names it by its path from the
// root's directory, as `pb format` names it.
func (s *Server) ownFile(root *workspace.Root, u uri.URI) (string, bool) {
	rel, _, reason := s.treeFile(dep.Tree{WS: s.deps.WS, Root: root}, modfiles.Members(root), u)
	return rel, reason == ""
}
