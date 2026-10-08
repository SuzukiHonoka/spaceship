package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCopyWithContextCompletes(t *testing.T) {
	var dst bytes.Buffer
	if err := CopyWithContext(context.Background(), nil, &dst, strings.NewReader("payload"), DirectionOut); err != nil {
		t.Fatal(err)
	}
	if got := dst.String(); got != "payload" {
		t.Fatalf("copied data = %q, want payload", got)
	}
}

type blockingReader struct {
	started chan struct{}
	done    chan struct{}
	once    sync.Once
}

func (r *blockingReader) Read([]byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.done
	return 0, io.ErrClosedPipe
}

func TestCopyWithContextCancellationUnblocksCopy(t *testing.T) {
	reader := &blockingReader{started: make(chan struct{}), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- CopyWithContext(ctx, func() { close(reader.done) }, io.Discard, reader, DirectionIn)
	}()

	<-reader.started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("CopyWithContext() error = %v, want context.Canceled", err)
	}
}

type closeWriteRecorder struct {
	closeWriteCalls int
	closeCalls      int
}

func (r *closeWriteRecorder) CloseWrite() error {
	r.closeWriteCalls++
	return nil
}

func (r *closeWriteRecorder) Close() error {
	r.closeCalls++
	return nil
}

type closeRecorder struct{ calls int }

func (r *closeRecorder) Close() error {
	r.calls++
	return nil
}

// readSizeRecorder records the size of every read request it receives.
type readSizeRecorder struct {
	r     io.Reader
	sizes []int
}

func (r *readSizeRecorder) Read(p []byte) (int, error) {
	r.sizes = append(r.sizes, len(p))
	return r.r.Read(p)
}

// writerToTrap fails the test if io.CopyBuffer takes its WriterTo shortcut
// instead of the buffered loop.
type writerToTrap struct {
	t *testing.T
	readSizeRecorder
}

func (w *writerToTrap) WriteTo(io.Writer) (int64, error) {
	w.t.Fatal("copy used WriterTo instead of the transport buffer")
	return 0, nil
}

func TestCopyWithContextUsesTransportBuffer(t *testing.T) {
	payload := bytes.Repeat([]byte{0x5a}, 3*GetBufferSize()+17)
	src := &writerToTrap{t: t, readSizeRecorder: readSizeRecorder{r: bytes.NewReader(payload)}}
	var dst bytes.Buffer
	if err := CopyWithContext(context.Background(), nil, &dst, src, DirectionOut); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dst.Bytes(), payload) {
		t.Fatalf("copied %d bytes, want %d", dst.Len(), len(payload))
	}
	for _, size := range src.sizes {
		if size != GetBufferSize() {
			t.Fatalf("read requested %d bytes, want the %d-byte transport buffer", size, GetBufferSize())
		}
	}
}

func TestWithPrefixReturnsConnWhenEmpty(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	if got := WithPrefix(a, nil); got != a {
		t.Fatalf("WithPrefix(conn, nil) = %T, want the connection itself", got)
	}
}

func TestPrefixedConnReadsPrefixThenConn(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	prefix := []byte("prefix")
	src := WithPrefix(server, prefix)
	prefix[0] = 'X' // the caller may reuse its buffer afterwards

	go func() {
		_, _ = client.Write([]byte("-then-conn"))
		_ = client.Close()
	}()
	got, err := io.ReadAll(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "prefix-then-conn" {
		t.Fatalf("read %q, want %q", got, "prefix-then-conn")
	}
}

func TestCopyWithContextWritesPrefixBeforeConn(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	src := WithPrefix(server, []byte("prefix"))

	go func() {
		_, _ = client.Write([]byte("-then-conn"))
		_ = client.Close()
	}()
	var dst bytes.Buffer
	if err := CopyWithContext(context.Background(), nil, &dst, src, DirectionOut); err != nil {
		t.Fatal(err)
	}
	if dst.String() != "prefix-then-conn" {
		t.Fatalf("copied %q, want %q", dst.String(), "prefix-then-conn")
	}
}

type abortRecorder struct {
	net.Conn
	aborted, closed int
}

func (r *abortRecorder) Abort() error { r.aborted++; return nil }
func (r *abortRecorder) Close() error { r.closed++; return nil }

func TestPrefixedConnAbortResetsConn(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	conn := &abortRecorder{Conn: a}
	src := WithPrefix(conn, []byte("x"))

	// AbortAll visits src first; a plain Close there would end the connection
	// in an orderly way before dst had a chance to reset it.
	AbortAll(src, b)
	if conn.aborted != 1 || conn.closed != 0 {
		t.Fatalf("abort/close calls = (%d, %d), want (1, 0)", conn.aborted, conn.closed)
	}
	CloseAll(src)
	if conn.closed != 1 {
		t.Fatalf("close calls = %d, want 1", conn.closed)
	}
}

type unwrapConn struct {
	net.Conn
	inner net.Conn
}

func (c unwrapConn) Unwrap() net.Conn { return c.inner }

func TestKernelConnFollowsUnwrappers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.Close()
		}
	}()
	tcp, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tcp.Close() }()

	if got, ok := kernelConn(tcp); !ok || got != tcp {
		t.Fatalf("kernelConn(*net.TCPConn) = (%v, %v), want the connection", got, ok)
	}
	wrapped := unwrapConn{Conn: tcp, inner: unwrapConn{Conn: tcp, inner: tcp}}
	if got, ok := kernelConn(wrapped); !ok || got != tcp {
		t.Fatalf("kernelConn(wrapped) = (%v, %v), want the TCP connection", got, ok)
	}

	pipeA, pipeB := net.Pipe()
	defer func() { _ = pipeA.Close() }()
	defer func() { _ = pipeB.Close() }()
	if _, ok := kernelConn(pipeA); ok {
		t.Fatal("kernelConn(net.Pipe) reported a kernel socket")
	}
	if _, ok := kernelConn(unwrapConn{Conn: tcp, inner: nil}); ok {
		t.Fatal("kernelConn accepted a wrapper that unwraps to nil")
	}
	cyclic := &selfUnwrapConn{Conn: tcp}
	cyclic.self = cyclic
	if _, ok := kernelConn(cyclic); ok {
		t.Fatal("kernelConn accepted a wrapper that unwraps to itself")
	}
}

func TestKernelConnIgnoresReplyGate(t *testing.T) {
	local, _ := tcpPair(t)
	// Splice must not look through the gate. copyStream waits, then unwraps
	// by hand; an Unwrap method here would deliver bytes before the reply.
	if _, ok := kernelConn(NewReplyGate(local)); ok {
		t.Fatal("kernelConn unwrapped a ReplyGate")
	}
}

type signalWriter struct {
	wrote chan struct{}
	once  sync.Once
	buf   bytes.Buffer
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.wrote) })
	return w.buf.Write(p)
}

func TestCopyWithContextDefersWritesUntilReplyGateReleased(t *testing.T) {
	dst := &signalWriter{wrote: make(chan struct{})}
	gate := NewReplyGate(dst)
	errCh := make(chan error, 1)
	go func() {
		errCh <- CopyWithContext(context.Background(), nil, gate, strings.NewReader("abc"), DirectionOut)
	}()
	select {
	case <-dst.wrote:
		t.Fatal("copy wrote before the reply gate was released")
	case <-time.After(50 * time.Millisecond):
	}
	gate.Release()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if got := dst.buf.String(); got != "abc" {
		t.Fatalf("copied %q, want abc", got)
	}
}

func TestCopyWithContextReplyGateUnblocksOnCancel(t *testing.T) {
	gate := NewReplyGate(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- CopyWithContext(ctx, nil, gate, strings.NewReader("abc"), DirectionOut)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CopyWithContext() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("copy stayed blocked after cancel")
	}
}

func TestReplyGateAbortResetsConn(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	conn := &abortRecorder{Conn: a}
	gate := NewReplyGate(conn)
	AbortAll(gate)
	if conn.aborted != 1 || conn.closed != 0 {
		t.Fatalf("abort/close calls = (%d, %d), want (1, 0)", conn.aborted, conn.closed)
	}
	select {
	case <-gate.ready:
	default:
		t.Fatal("abort left the reply gate closed to writers")
	}
}

type selfUnwrapConn struct {
	net.Conn
	self *selfUnwrapConn
}

func (c *selfUnwrapConn) Unwrap() net.Conn { return c.self }

// TestCopyWithContextRelaysBetweenSockets runs the socket-to-socket shape
// the front ends produce (a wrapped client socket on one side, a bare TCP
// egress on the other) in both directions, including the half-close that
// ends a session. On Linux this is the kernel splice path.
func TestCopyWithContextRelaysBetweenSockets(t *testing.T) {
	proxySide, client := tcpPair(t)
	echoSide, target := tcpPair(t)
	go func() {
		_, _ = io.Copy(echoSide, echoSide)
		_ = echoSide.Close()
	}()

	wrapped := unwrapConn{Conn: proxySide, inner: proxySide}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	up := make(chan error, 1)
	down := make(chan error, 1)
	go func() {
		err := CopyWithContext(ctx, nil, target, wrapped, DirectionOut)
		CloseWriteOrClose(target)
		up <- err
	}()
	go func() {
		err := CopyWithContext(ctx, nil, wrapped, target, DirectionIn)
		// Proxy's endSession does this half-close in production.
		CloseWriteOrClose(proxySide)
		down <- err
	}()

	payload := bytes.Repeat([]byte{0xa5}, 5*GetBufferSize()+123)
	go func() {
		_, _ = client.Write(payload)
		_ = client.(*net.TCPConn).CloseWrite()
	}()
	got, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("relayed %d bytes, want %d", len(got), len(payload))
	}
	for _, ch := range []chan error{up, down} {
		if err := <-ch; err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("copy error = %v", err)
		}
	}
}

func TestCloseHelpers(t *testing.T) {
	halfCloser := new(closeWriteRecorder)
	CloseWriteOrClose(halfCloser)
	if halfCloser.closeWriteCalls != 1 || halfCloser.closeCalls != 0 {
		t.Fatalf("half close calls = (%d, %d), want (1, 0)", halfCloser.closeWriteCalls, halfCloser.closeCalls)
	}

	first := new(closeRecorder)
	second := new(closeRecorder)
	CloseWriteOrClose(first)
	CloseAll(first, second, struct{}{})
	if first.calls != 2 || second.calls != 1 {
		t.Fatalf("close calls = (%d, %d), want (2, 1)", first.calls, second.calls)
	}
}
