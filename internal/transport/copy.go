package transport

import (
	"context"
	"io"
	"net"
	"slices"
)

type closeWriter interface {
	CloseWrite() error
}

// ConnUnwrapper is implemented by connection wrappers that leave the byte
// stream untouched, such as one that only makes Close idempotent, so a copy
// may bypass the wrapper and drive the connection it wraps directly, in the
// kernel where the platform allows. A wrapper that transforms data, TLS for
// one, must not implement it.
type ConnUnwrapper interface {
	Unwrap() net.Conn
}

// PrefixedConn reads bytes a front end already pulled from a connection while
// parsing its handshake, then the connection itself. A SOCKS or HTTP client
// may send tunnel bytes right after its request without waiting for the
// reply, and a buffered handshake reader will have consumed them; this hands
// them to the transport without keeping that reader in the data path.
type PrefixedConn struct {
	prefix []byte
	conn   net.Conn
}

// WithPrefix returns a reader that yields prefix and then conn. When prefix
// is empty it returns conn itself, so the common case stays a plain
// connection that copies can recognize.
func WithPrefix(conn net.Conn, prefix []byte) io.Reader {
	if len(prefix) == 0 {
		return conn
	}
	return &PrefixedConn{prefix: slices.Clone(prefix), conn: conn}
}

func (p *PrefixedConn) Read(b []byte) (int, error) {
	if len(p.prefix) > 0 {
		n := copy(b, p.prefix)
		p.prefix = p.prefix[n:]
		if len(p.prefix) == 0 {
			p.prefix = nil
		}
		return n, nil
	}
	return p.conn.Read(b)
}

// Close closes the connection. Any prefix not yet read is dropped with it.
func (p *PrefixedConn) Close() error {
	return p.conn.Close()
}

// Abort resets the connection. Without it, aborting a session whose source is
// a PrefixedConn would fall back to Close and end the connection in an
// orderly way, hiding a truncated response from the client.
func (p *PrefixedConn) Abort() error {
	Abort(p.conn)
	return nil
}

// flushPrefix writes the unread prefix to w and returns how much it wrote.
func (p *PrefixedConn) flushPrefix(w io.Writer) (int64, error) {
	if len(p.prefix) == 0 {
		return 0, nil
	}
	n, err := w.Write(p.prefix)
	p.prefix = p.prefix[n:]
	if len(p.prefix) == 0 {
		p.prefix = nil
	}
	return int64(n), err
}

// readerOnly and writerOnly hide io.WriterTo and io.ReaderFrom so io.CopyBuffer
// runs its plain read/write loop with the buffer it was given.
type readerOnly struct{ r io.Reader }

func (r readerOnly) Read(p []byte) (int, error) { return r.r.Read(p) }

type writerOnly struct{ w io.Writer }

func (w writerOnly) Write(p []byte) (int, error) { return w.w.Write(p) }

// kernelConn follows pass-through wrappers down to a stream socket the kernel
// can move data between: a TCP connection, or a stream Unix-domain one.
func kernelConn(v any) (net.Conn, bool) {
	// Bounded so a wrapper that unwraps to itself cannot spin.
	for range 8 {
		switch c := v.(type) {
		case *net.TCPConn:
			return c, true
		case *net.UnixConn:
			if addr, ok := c.LocalAddr().(*net.UnixAddr); ok && addr.Net == "unix" {
				return c, true
			}
			return nil, false
		case ConnUnwrapper:
			inner := c.Unwrap()
			if inner == nil {
				return nil, false
			}
			v = inner
		default:
			return nil, false
		}
	}
	return nil, false
}

// copyStream moves src to dst until EOF or an error.
//
// io.CopyBuffer defers to src.WriteTo or dst.ReadFrom when either exists, and
// the net package implements both on TCP connections. Unless both ends are
// bare kernel sockets, those implementations fall back to io.Copy with a
// 32KiB buffer they allocate themselves, so a copy from or to a wrapped
// connection would silently bypass the transport buffer. This copy decides
// the path itself: a front end's buffered handshake leftovers go first; then,
// where the platform supports it and both ends are kernel stream sockets, the
// kernel moves the bytes with no user-space buffer at all; otherwise a plain
// loop over the transport buffer.
//
// A ReplyGate is waited out and then removed before that choice. Splice and
// the buffered loop both have to see the connection underneath; looking
// through the gate earlier would deliver tunnel bytes before the proxy reply.
func copyStream(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	if gate, ok := dst.(*ReplyGate); ok {
		select {
		case <-gate.ready:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		// Cancellation can coincide with Release. Do not forward withheld
		// bytes when both were ready and the gate happened to win.
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		dst = gate.w
	}
	var written int64
	if p, ok := src.(*PrefixedConn); ok {
		n, err := p.flushPrefix(dst)
		written += n
		if err != nil {
			return written, err
		}
		src = p.conn
	}
	if n, err, ok := spliceStreams(dst, src); ok {
		return written + n, err
	}
	buf := Buffer()
	defer PutBuffer(buf)
	n, err := io.CopyBuffer(writerOnly{dst}, readerOnly{src}, *buf)
	return written + n, err
}

// CopyWithContext copies from src to dst until EOF or an error, through the
// kernel when both ends allow it and otherwise with the shared transport
// buffer. If ctx is canceled before the copy completes, unblock is called
// before waiting for the copy goroutine to exit.
func CopyWithContext(ctx context.Context, unblock func(), dst io.Writer, src io.Reader, direction Direction) error {
	type copyResult struct {
		n   int64
		err error
	}
	resultCh := make(chan copyResult, 1)
	go func() {
		n, err := copyStream(ctx, dst, src)
		resultCh <- copyResult{n: n, err: err}
	}()

	select {
	case result := <-resultCh:
		GlobalStats.Add(direction, result.n)
		return result.err
	case <-ctx.Done():
		if unblock != nil {
			unblock()
		}
		result := <-resultCh
		GlobalStats.Add(direction, result.n)
		return ctx.Err()
	}
}

// CloseWriteOrClose closes the write side of v when supported. If the
// connection type does not support half-close, it falls back to Close so the
// opposite copy direction does not block forever.
func CloseWriteOrClose(v any) {
	if cw, ok := v.(closeWriter); ok {
		_ = cw.CloseWrite()
		return
	}
	if closer, ok := v.(io.Closer); ok {
		_ = closer.Close()
	}
}

// CloseAll closes every value that implements io.Closer.
//
// Identical closer values (same interface dynamic type and pointer) are only
// closed once. HTTP CONNECT passes the same conn as both src and dst into
// Proxy, and without dedupe that would close the client socket twice.
func CloseAll(values ...any) {
	seen := make([]io.Closer, 0, len(values))
	for _, value := range values {
		closer, ok := value.(io.Closer)
		if !ok || closer == nil {
			continue
		}
		dup := slices.Contains(seen, closer)
		if dup {
			continue
		}
		seen = append(seen, closer)
		_ = closer.Close()
	}
}
