package transport

import (
	"context"
	"io"
	"sync"
)

// ReplyGate is an io.Writer that withholds bytes until Release.
//
// HTTP CONNECT and SOCKS5 write their success reply on the client connection
// after the egress dial returns, while the transport copies as soon as that
// dial succeeds. A client that sent tunnel bytes with its request can
// otherwise have the origin's answer written ahead of the proxy reply.
// Release must be called, including on the failure path; Close and Abort do
// so the copy cannot stay blocked when the session ends first.
type ReplyGate struct {
	w     io.Writer
	ready chan struct{}
	once  sync.Once
}

// NewReplyGate returns a writer that blocks until Release, Close, or Abort.
func NewReplyGate(w io.Writer) *ReplyGate {
	return &ReplyGate{w: w, ready: make(chan struct{})}
}

// Release lets withheld writes proceed. It is safe to call more than once.
func (g *ReplyGate) Release() {
	g.once.Do(func() { close(g.ready) })
}

func (g *ReplyGate) Write(p []byte) (int, error) {
	return g.WriteContext(context.Background(), p)
}

// WriteContext waits for the proxy reply or session cancellation before
// writing. RPC receivers use this with their copy context: cancelling the
// RPC stream alone cannot unblock a receiver waiting on the reply gate.
// Once released, a blocked underlying Write must still be unblocked by its
// owner, just like a write without a gate.
func (g *ReplyGate) WriteContext(ctx context.Context, p []byte) (int, error) {
	select {
	case <-g.ready:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	// Cancellation can coincide with Release. Do not forward withheld bytes
	// when both select cases were ready and the gate happened to win.
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return g.w.Write(p)
}

// Close releases withheld writes and closes the underlying writer when it
// implements io.Closer. Session teardown closes the transport destination,
// and without the release a copy blocked in Write would not return.
func (g *ReplyGate) Close() error {
	g.Release()
	if c, ok := g.w.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// Abort releases withheld writes and resets the underlying connection, so a
// failed response still reaches the client as a reset when the destination
// passed to Proxy is the gate rather than the connection itself.
func (g *ReplyGate) Abort() error {
	g.Release()
	Abort(g.w)
	return nil
}
