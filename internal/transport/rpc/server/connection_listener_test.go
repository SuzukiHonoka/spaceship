package server

import (
	"errors"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestConnectionTrackingListenerTracksAndClosesAcceptedSockets(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := newConnectionTrackingListener(base)
	t.Cleanup(func() { _ = listener.Close() })

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	client, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	var serverConn net.Conn
	select {
	case serverConn = <-accepted:
	case err := <-acceptErr:
		t.Fatalf("Accept() error = %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("Accept() did not return")
	}
	if got := listener.connectionCount(); got != 1 {
		t.Fatalf("tracked connection count = %d, want 1", got)
	}
	syscallConn, ok := serverConn.(syscall.Conn)
	if !ok {
		t.Fatalf("accepted connection type %T does not preserve syscall.Conn", serverConn)
	}
	rawConn, err := syscallConn.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn() error = %v", err)
	}
	if err := rawConn.Control(func(uintptr) {}); err != nil {
		t.Fatalf("RawConn.Control() error = %v", err)
	}

	if err := listener.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := listener.connectionCount(); got != 0 {
		t.Fatalf("tracked connection count after Close = %d, want 0", got)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if conn, err := listener.Accept(); err == nil || conn != nil {
		t.Fatalf("Accept() after Close = (%v, %v), want closed error", conn, err)
	}

	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("client remained connected after tracking listener Close")
	}
}

func TestTrackedConnectionCloseUnregistersItself(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := newConnectionTrackingListener(base)
	t.Cleanup(func() { _ = listener.Close() })

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	client, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	conn := <-accepted
	if conn == nil {
		t.Fatal("Accept() returned nil connection")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if got := listener.connectionCount(); got != 0 {
		t.Fatalf("tracked connection count = %d after connection Close, want 0", got)
	}
}

type lateAcceptListener struct {
	conn    net.Conn
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (l *lateAcceptListener) Accept() (net.Conn, error) {
	close(l.entered)
	<-l.release
	return l.conn, nil
}

func (l *lateAcceptListener) Close() error {
	l.once.Do(func() { close(l.release) })
	return nil
}

func (*lateAcceptListener) Addr() net.Addr {
	return testAddr("late-accept")
}

type testAddr string

func (a testAddr) Network() string { return "test" }
func (a testAddr) String() string  { return string(a) }

func TestConnectionAcceptedDuringCloseIsRejected(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer func() { _ = clientSide.Close() }()

	base := &lateAcceptListener{
		conn:    serverSide,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	listener := newConnectionTrackingListener(base)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if conn != nil {
			_ = conn.Close()
		}
		acceptErr <- err
	}()

	<-base.entered
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-acceptErr:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept() error = %v, want net.ErrClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Accept() did not finish after listener Close")
	}

	if err := clientSide.SetReadDeadline(time.Now().Add(time.Second)); err == nil {
		if _, err := clientSide.Read(make([]byte, 1)); err == nil {
			t.Fatal("connection accepted during Close remained open")
		}
	}
}

func TestConnectionTrackingListenerRejectsNilConnection(t *testing.T) {
	listener := newConnectionTrackingListener(&nilConnListener{})
	defer func() { _ = listener.Close() }()

	if conn, err := listener.Accept(); err == nil || conn != nil {
		t.Fatalf("Accept() = (%v, %v), want nil connection error", conn, err)
	}
}

type nilConnListener struct{}

func (*nilConnListener) Accept() (net.Conn, error) { return nil, nil }
func (*nilConnListener) Close() error              { return nil }
func (*nilConnListener) Addr() net.Addr            { return testAddr("nil-conn") }

type fixedResultListener struct {
	conn       net.Conn
	acceptErr  error
	closeErr   error
	acceptOnce sync.Once
	closeCount int
}

func (l *fixedResultListener) Accept() (conn net.Conn, err error) {
	returned := false
	l.acceptOnce.Do(func() {
		returned = true
		conn = l.conn
		err = l.acceptErr
	})
	if !returned {
		return nil, net.ErrClosed
	}
	return conn, err
}

func (l *fixedResultListener) Close() error {
	l.closeCount++
	return l.closeErr
}

func (*fixedResultListener) Addr() net.Addr { return testAddr("fixed-result") }

func TestConnectionTrackingListenerClosesConnectionReturnedWithError(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer func() { _ = clientSide.Close() }()
	acceptErr := errors.New("accept failed")
	base := &fixedResultListener{conn: serverSide, acceptErr: acceptErr}
	listener := newConnectionTrackingListener(base)
	t.Cleanup(func() { _ = listener.Close() })

	conn, err := listener.Accept()
	if conn != nil || !errors.Is(err, acceptErr) {
		t.Fatalf("Accept() = (%v, %v), want (nil, %v)", conn, err, acceptErr)
	}

	if err := clientSide.SetReadDeadline(time.Now().Add(time.Second)); err == nil {
		if _, err := clientSide.Read(make([]byte, 1)); err == nil {
			t.Fatal("connection returned alongside Accept error remained open")
		}
	}
}

type closeErrorConn struct {
	net.Conn
	err        error
	closeCount int
}

func (c *closeErrorConn) Close() error {
	c.closeCount++
	_ = c.Conn.Close()
	return c.err
}

func TestConnectionTrackingListenerConcurrentCloseJoinsErrors(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer func() { _ = clientSide.Close() }()

	connCloseErr := errors.New("connection close failed")
	listenerCloseErr := errors.New("listener close failed")
	rawConn := &closeErrorConn{Conn: serverSide, err: connCloseErr}
	base := &fixedResultListener{conn: rawConn, closeErr: listenerCloseErr}
	listener := newConnectionTrackingListener(base)

	accepted, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := accepted.(*trackedConn); !ok {
		t.Fatalf("accepted connection type = %T, want *trackedConn", accepted)
	}

	const callers = 8
	results := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			results <- listener.Close()
		})
	}
	wg.Wait()
	close(results)

	for err := range results {
		if !errors.Is(err, listenerCloseErr) || !errors.Is(err, connCloseErr) {
			t.Fatalf("Close() error = %v, want both listener and connection errors", err)
		}
	}
	if base.closeCount != 1 {
		t.Fatalf("underlying listener Close count = %d, want 1", base.closeCount)
	}
	if rawConn.closeCount != 1 {
		t.Fatalf("underlying connection Close count = %d, want 1", rawConn.closeCount)
	}
	if err := accepted.Close(); !errors.Is(err, connCloseErr) {
		t.Fatalf("accepted Close() error = %v, want %v", err, connCloseErr)
	}
	if rawConn.closeCount != 1 {
		t.Fatalf("second accepted Close reached underlying connection %d times, want 1", rawConn.closeCount)
	}
}
