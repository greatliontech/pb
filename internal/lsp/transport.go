package lsp

import (
	"context"
	"io"

	"github.com/greatliontech/lsp/jsonrpc2"
)

// stdio is standard input and output as one connection. Close closes
// both: the input's close unblocks a read parked on it, which is how
// the connection ends from the server's side (REQ-lsp-lifecycle's
// exit).
type stdio struct {
	in  io.ReadCloser
	out io.WriteCloser
}

func (s *stdio) Read(p []byte) (int, error)  { return s.in.Read(p) }
func (s *stdio) Write(p []byte) (int, error) { return s.out.Write(p) }
func (s *stdio) Close() error {
	err := s.in.Close()
	if cerr := s.out.Close(); err == nil {
		err = cerr
	}
	return err
}

// Serve serves one client over standard input and output until the
// connection ends — the client's `exit`, or either side's end — and
// returns the process's exit status: 0 after `shutdown`, 1 without
// (lsp.md REQ-lsp-lifecycle). The stream is the binding's header
// framing, which answers a malformed body and goes on and ends the
// connection on a framing error (its INV-E, INV-S).
func Serve(ctx context.Context, s *Server, in io.ReadCloser, out io.WriteCloser) int {
	return s.serve(ctx, jsonrpc2.NewHeaderStream(&stdio{in: in, out: out}))
}
