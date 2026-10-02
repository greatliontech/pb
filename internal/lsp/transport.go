package lsp

import (
	"context"
	"errors"
	"io"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

// codec marshals the protocol's payloads with the protocol package's
// union-aware encoders, as its own connections do.
type codec struct{}

func (codec) Marshal(v any) ([]byte, error)      { return protocol.Marshal(v) }
func (codec) Unmarshal(data []byte, v any) error { return protocol.Unmarshal(data, v) }

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

// frameStream is a header stream read frame by frame: the body of the
// next message without decoding it.
type frameStream interface {
	jsonrpc2.Stream
	ReadFrame(ctx context.Context) ([]byte, int64, error)
}

// robust is the transport's contract over a header stream (lsp.md
// REQ-lsp-transport): a frame whose body is no JSON-RPC message is
// answered with the protocol's parse or invalid-request error and
// the connection goes on, where the connection itself would end on
// it; a framing error, or the stream's end, ends the connection as
// before. It reads through Read alone, never frame by frame, so the
// connection takes the decoded message from it.
type robust struct {
	inner frameStream
}

// NewStream is the server's stream over a connection: the protocol's
// header framing, robust to a malformed body.
func NewStream(conn io.ReadWriteCloser) jsonrpc2.Stream {
	return &robust{inner: jsonrpc2.NewHeaderStream(conn).(frameStream)}
}

func (r *robust) Read(ctx context.Context) (jsonrpc2.Message, int64, error) {
	for {
		frame, n, err := r.inner.ReadFrame(ctx)
		if err != nil {
			return nil, n, err
		}
		msg, err := jsonrpc2.DecodeMessage(frame)
		if err == nil {
			return msg, n, nil
		}
		// The answer carries the request's id where the body is JSON
		// that names one, as the protocol has it; null otherwise.
		answer, id := jsonrpc2.ErrParse, jsonrpc2.ID{}
		if errors.Is(err, jsonrpc2.ErrInvalidRequest) {
			answer = jsonrpc2.ErrInvalidRequest
			if view, verr := jsonrpc2.ScanMessageView(frame); verr == nil {
				if named, ok := view.ID.ID(); ok {
					id = named
				}
			}
		}
		if _, werr := r.inner.Write(ctx, jsonrpc2.NewResponse(id, nil, answer)); werr != nil {
			return nil, n, werr
		}
	}
}

func (r *robust) Write(ctx context.Context, msg jsonrpc2.Message) (int64, error) {
	return r.inner.Write(ctx, msg)
}

func (r *robust) Close() error { return r.inner.Close() }

// Serve serves one client over standard input and output until the
// connection ends — the client's `exit`, or either side's end — and
// returns the process's exit status: 0 after `shutdown`, 1 without
// (lsp.md REQ-lsp-lifecycle).
func Serve(ctx context.Context, s *Server, in io.ReadCloser, out io.WriteCloser) int {
	return s.serve(ctx, NewStream(&stdio{in: in, out: out}))
}
