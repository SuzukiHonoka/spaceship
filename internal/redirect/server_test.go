package redirect

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/forward"
)

func newTestServer(t *testing.T, ctx context.Context, cfg *Config) *Server {
	t.Helper()
	s, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type echoTransport struct {
	target     string
	closeCount atomic.Int32
}

func (t *echoTransport) String() string {
	return "test"
}

func (t *echoTransport) Proxy(_ context.Context, addr string, localAddr chan<- string, dst io.Writer, src io.Reader) error {
	defer close(localAddr)
	t.target = addr
	localAddr <- "127.0.0.1:1"

	payload := make([]byte, 4)
	if _, err := io.ReadFull(src, payload); err != nil {
		return err
	}
	_, err := dst.Write(bytes.ToUpper(payload))
	return err
}

func (t *echoTransport) Dial(_, _ string) (net.Conn, error) {
	return nil, errors.New("not used")
}

func (t *echoTransport) Close() error {
	t.closeCount.Add(1)
	return nil
}

func TestServeConnRoutesOriginalDestination(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer func() { _ = clientSide.Close() }()
	if err := clientSide.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}

	dst := &net.TCPAddr{IP: net.ParseIP("2001:db8::10"), Port: 443}
	tr := new(echoTransport)
	var routeKey string

	s := newTestServer(t, context.Background(), nil)
	s.resolveDestination = func(net.Conn) (*net.TCPAddr, error) {
		return dst, nil
	}
	s.resolveRoute = func(host string) (transport.Transport, error) {
		routeKey = host
		return tr, nil
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.ServeConn(serverSide)
	}()

	if _, err := clientSide.Write([]byte("ping")); err != nil {
		t.Fatalf("write client payload: %v", err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(clientSide, reply); err != nil {
		t.Fatalf("read proxy reply: %v", err)
	}
	if got, want := string(reply), "PING"; got != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ServeConn() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ServeConn() did not return")
	}

	if got, want := routeKey, "2001:db8::10"; got != want {
		t.Fatalf("route key = %q, want %q", got, want)
	}
	if got, want := tr.target, "[2001:db8::10]:443"; got != want {
		t.Fatalf("proxy target = %q, want %q", got, want)
	}
	if got := tr.closeCount.Load(); got != 1 {
		t.Fatalf("transport Close count = %d, want 1", got)
	}
}

func TestServerRoutesThroughConfiguredDirectEgress(t *testing.T) {
	testServerRoutesThroughEgress(t, router.EgressDirect)
}

func TestServerRoutesThroughConfiguredForwardEgress(t *testing.T) {
	if err := forward.Attach(new(net.Dialer)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := forward.Attach(nil); err != nil {
			t.Errorf("detach forward dialer: %v", err)
		}
	})
	testServerRoutesThroughEgress(t, router.EgressForward)
}

func testServerRoutesThroughEgress(t *testing.T, egress router.Egress) {
	t.Helper()
	if err := router.SetRoutes(router.Routes{
		{
			MatchType:   router.TypeDefault,
			Destination: egress,
		},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := router.SetRoutes(router.Routes{
			router.CloneRoute(router.RouteClientDefault),
		}); err != nil {
			t.Errorf("restore routes: %v", err)
		}
	})

	targetListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = targetListener.Close() }()
	targetAddr := targetListener.Addr().(*net.TCPAddr)
	targetErr := make(chan error, 1)
	go func() {
		conn, err := targetListener.Accept()
		if err != nil {
			targetErr <- err
			return
		}
		defer func() { _ = conn.Close() }()

		payload := make([]byte, 4)
		if _, err := io.ReadFull(conn, payload); err != nil {
			targetErr <- err
			return
		}
		_, err = conn.Write(bytes.ToUpper(payload))
		targetErr <- err
	}()

	ctx, cancel := context.WithCancel(context.Background())
	s := newTestServer(t, ctx, &Config{MaxConnections: 4})
	s.resolveDestination = func(net.Conn) (*net.TCPAddr, error) {
		return targetAddr, nil
	}

	redirectListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.serve(redirectListener)
	}()

	client, err := net.DialTimeout("tcp4", redirectListener.Addr().String(), time.Second)
	if err != nil {
		cancel()
		<-serveErr
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		cancel()
		<-serveErr
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		cancel()
		<-serveErr
		t.Fatal(err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(client, reply); err != nil {
		cancel()
		<-serveErr
		t.Fatal(err)
	}
	if got := string(reply); got != "PING" {
		cancel()
		<-serveErr
		t.Fatalf("redirected reply = %q, want PING", got)
	}

	select {
	case err := <-targetErr:
		if err != nil {
			cancel()
			<-serveErr
			t.Fatalf("target server: %v", err)
		}
	case <-time.After(3 * time.Second):
		cancel()
		<-serveErr
		t.Fatal("target server did not complete")
	}

	cancel()
	select {
	case err := <-serveErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("serve() error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("redirect server did not stop")
	}
}

type localAddrConn struct {
	net.Conn
	local net.Addr
}

func (c *localAddrConn) LocalAddr() net.Addr {
	return c.local
}

func TestServeConnRejectsRedirectLoop(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer func() { _ = clientSide.Close() }()

	dst := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}
	conn := &localAddrConn{Conn: serverSide, local: dst}

	s := newTestServer(t, context.Background(), nil)
	s.resolveDestination = func(net.Conn) (*net.TCPAddr, error) {
		return dst, nil
	}
	s.resolveRoute = func(string) (transport.Transport, error) {
		t.Fatal("route resolver called for a redirect loop")
		return nil, nil
	}

	err := s.ServeConn(conn)
	if !errors.Is(err, ErrRedirectLoop) {
		t.Fatalf("ServeConn() error = %v, want ErrRedirectLoop", err)
	}

	if err := clientSide.SetReadDeadline(time.Now().Add(time.Second)); err == nil {
		if _, err := clientSide.Read(make([]byte, 1)); err == nil {
			t.Fatal("client connection remained open after redirect-loop rejection")
		}
	}
}

func TestServeConnRejectsInvalidOriginalDestination(t *testing.T) {
	tests := []struct {
		name string
		dst  *net.TCPAddr
	}{
		{name: "nil"},
		{name: "nil IP", dst: &net.TCPAddr{Port: 80}},
		{name: "malformed IP", dst: &net.TCPAddr{IP: net.IP{192, 0, 2}, Port: 80}},
		{name: "unspecified IPv4", dst: &net.TCPAddr{IP: net.IPv4zero, Port: 80}},
		{name: "unspecified IPv6", dst: &net.TCPAddr{IP: net.IPv6zero, Port: 80}},
		{name: "negative port", dst: &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: -1}},
		{name: "zero port", dst: &net.TCPAddr{IP: net.ParseIP("192.0.2.1")}},
		{name: "oversized port", dst: &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 65536}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverSide, clientSide := net.Pipe()
			defer func() { _ = clientSide.Close() }()

			s := newTestServer(t, context.Background(), nil)
			s.resolveDestination = func(net.Conn) (*net.TCPAddr, error) {
				return tt.dst, nil
			}

			err := s.ServeConn(serverSide)
			if !errors.Is(err, ErrInvalidOriginalDestination) {
				t.Fatalf("ServeConn() error = %v, want ErrInvalidOriginalDestination", err)
			}
		})
	}
}

func TestServeConnPropagatesDestinationAndRouteErrors(t *testing.T) {
	destinationErr := errors.New("destination lookup failed")
	t.Run("destination", func(t *testing.T) {
		serverSide, clientSide := net.Pipe()
		defer func() { _ = clientSide.Close() }()

		s := newTestServer(t, context.Background(), nil)
		s.resolveDestination = func(net.Conn) (*net.TCPAddr, error) {
			return nil, destinationErr
		}
		if err := s.ServeConn(serverSide); !errors.Is(err, destinationErr) {
			t.Fatalf("ServeConn() error = %v, want destination error", err)
		}
	})

	routeErr := errors.New("route lookup failed")
	t.Run("route", func(t *testing.T) {
		serverSide, clientSide := net.Pipe()
		defer func() { _ = clientSide.Close() }()

		s := newTestServer(t, context.Background(), nil)
		s.resolveDestination = func(net.Conn) (*net.TCPAddr, error) {
			return &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 443}, nil
		}
		s.resolveRoute = func(string) (transport.Transport, error) {
			return nil, routeErr
		}
		if err := s.ServeConn(serverSide); !errors.Is(err, routeErr) {
			t.Fatalf("ServeConn() error = %v, want route error", err)
		}
	})
}

type failingTransport struct {
	err        error
	closeCount atomic.Int32
}

func (t *failingTransport) String() string {
	return "failing-test"
}

func (t *failingTransport) Proxy(context.Context, string, chan<- string, io.Writer, io.Reader) error {
	return t.err
}

func (t *failingTransport) Dial(_, _ string) (net.Conn, error) {
	return nil, errors.New("not used")
}

func (t *failingTransport) Close() error {
	t.closeCount.Add(1)
	return nil
}

func TestServeConnClosesTransportAfterProxyError(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer func() { _ = clientSide.Close() }()

	proxyErr := errors.New("proxy failed")
	tr := &failingTransport{err: proxyErr}
	s := newTestServer(t, context.Background(), nil)
	s.resolveDestination = func(net.Conn) (*net.TCPAddr, error) {
		return &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 443}, nil
	}
	s.resolveRoute = func(string) (transport.Transport, error) {
		return tr, nil
	}

	if err := s.ServeConn(serverSide); !errors.Is(err, proxyErr) {
		t.Fatalf("ServeConn() error = %v, want proxy error", err)
	}
	if got := tr.closeCount.Load(); got != 1 {
		t.Fatalf("transport Close count = %d, want 1", got)
	}
}

type blockingTransport struct {
	started chan struct{}
	done    chan struct{}
}

func (t *blockingTransport) String() string {
	return "blocking-test"
}

func (t *blockingTransport) Proxy(_ context.Context, _ string, localAddr chan<- string, _ io.Writer, src io.Reader) error {
	defer close(t.done)
	defer close(localAddr)
	localAddr <- "127.0.0.1:1"
	close(t.started)
	_, err := src.Read(make([]byte, 1))
	return err
}

func (t *blockingTransport) Dial(_, _ string) (net.Conn, error) {
	return nil, errors.New("not used")
}

func (t *blockingTransport) Close() error {
	return nil
}

func TestServerContextCancellationClosesActiveConnections(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newTestServer(t, ctx, nil)

	tr := &blockingTransport{
		started: make(chan struct{}),
		done:    make(chan struct{}),
	}
	s.resolveDestination = func(net.Conn) (*net.TCPAddr, error) {
		return &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 443}, nil
	}
	s.resolveRoute = func(string) (transport.Transport, error) {
		return tr, nil
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.serve(listener)
	}()

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	select {
	case <-tr.started:
	case <-time.After(3 * time.Second):
		t.Fatal("redirect handler did not start")
	}

	cancel()

	select {
	case err := <-serveErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("serve() error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("redirect server did not stop")
	}
	select {
	case <-tr.done:
	default:
		t.Fatal("serve() returned before its active handler exited")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

type gatedTransport struct {
	started chan struct{}
	release <-chan struct{}
}

func (t *gatedTransport) String() string {
	return "gated-test"
}

func (t *gatedTransport) Proxy(ctx context.Context, _ string, localAddr chan<- string, _ io.Writer, _ io.Reader) error {
	defer close(localAddr)
	localAddr <- "127.0.0.1:1"
	t.started <- struct{}{}
	select {
	case <-t.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *gatedTransport) Dial(_, _ string) (net.Conn, error) {
	return nil, errors.New("not used")
}

func (t *gatedTransport) Close() error {
	return nil
}

func TestServerBoundsConcurrentHandlers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newTestServer(t, ctx, &Config{MaxConnections: 1})

	release := make(chan struct{})
	started := make(chan struct{}, 2)
	s.resolveDestination = func(net.Conn) (*net.TCPAddr, error) {
		return &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 443}, nil
	}
	s.resolveRoute = func(string) (transport.Transport, error) {
		return &gatedTransport{started: started, release: release}, nil
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.serve(listener)
	}()

	first, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("first redirect handler did not start")
	}

	second, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	select {
	case <-started:
		t.Fatal("second handler exceeded max_connections")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("second handler did not start after capacity was released")
	}

	cancel()
	select {
	case err := <-serveErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("serve() error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("redirect server did not stop")
	}
}

func TestServerShutdownUnblocksFullAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newTestServer(t, ctx, &Config{MaxConnections: 1})

	started := make(chan struct{}, 1)
	s.resolveDestination = func(net.Conn) (*net.TCPAddr, error) {
		return &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 443}, nil
	}
	s.resolveRoute = func(string) (transport.Transport, error) {
		return &gatedTransport{started: started, release: make(chan struct{})}, nil
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.serve(listener)
	}()

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("redirect handler did not start")
	}

	cancel()
	select {
	case err := <-serveErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("serve() error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("redirect server remained blocked on full admission")
	}
}

func TestNewValidatesAndDefaultsMaxConnections(t *testing.T) {
	s := newTestServer(t, context.Background(), nil)
	if got := s.maxConnections; got != DefaultMaxConnections {
		t.Fatalf("default max connections = %d, want %d", got, DefaultMaxConnections)
	}
	custom := newTestServer(t, context.Background(), &Config{MaxConnections: 7})
	if got := custom.maxConnections; got != 7 {
		t.Fatalf("custom max connections = %d, want 7", got)
	}
	if _, err := New(context.Background(), &Config{MaxConnections: -1}); !errors.Is(err, ErrInvalidMaxConnections) {
		t.Fatalf("New() error = %v, want ErrInvalidMaxConnections", err)
	}
}

func TestListenAndServeUnsupportedPlatform(t *testing.T) {
	if Supported() {
		t.Skip("platform supports transparent redirect")
	}

	s := newTestServer(t, context.Background(), nil)
	err := s.ListenAndServe("127.0.0.1:0")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ListenAndServe() error = %v, want ErrUnsupported", err)
	}
	if !strings.Contains(err.Error(), "Linux") {
		t.Fatalf("unsupported error = %q, want Linux guidance", err)
	}
	if _, err := originalDestination(nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("originalDestination() error = %v, want ErrUnsupported", err)
	}
}

type stubListener struct {
	accept func() (net.Conn, error)
	close  func() error
}

func (l *stubListener) Accept() (net.Conn, error) {
	return l.accept()
}

func (l *stubListener) Close() error {
	if l.close != nil {
		return l.close()
	}
	return nil
}

func (*stubListener) Addr() net.Addr {
	return stubAddr("redirect-test")
}

type stubAddr string

func (a stubAddr) Network() string { return "test" }
func (a stubAddr) String() string  { return string(a) }

type stubAcceptError struct {
	timeout   bool
	temporary bool
}

func (e stubAcceptError) Error() string   { return "temporary accept failure" }
func (e stubAcceptError) Timeout() bool   { return e.timeout }
func (e stubAcceptError) Temporary() bool { return e.temporary }

func TestServerRetriesTemporaryAcceptErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  stubAcceptError
	}{
		{name: "timeout", err: stubAcceptError{timeout: true}},
		{name: "temporary", err: stubAcceptError{temporary: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			terminalErr := errors.New("terminal accept failure")
			var calls atomic.Int32
			listener := &stubListener{
				accept: func() (net.Conn, error) {
					if calls.Add(1) == 1 {
						return nil, tt.err
					}
					return nil, terminalErr
				},
			}

			s := newTestServer(t, context.Background(), nil)
			if err := s.serve(listener); !errors.Is(err, terminalErr) {
				t.Fatalf("serve() error = %v, want terminal accept error", err)
			}
			if got := calls.Load(); got != 2 {
				t.Fatalf("Accept() calls = %d, want 2", got)
			}
		})
	}
}

func TestServerCancellationInterruptsAcceptRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newTestServer(t, ctx, nil)

	enteredAccept := make(chan struct{})
	releaseAccept := make(chan struct{})
	var enteredOnce sync.Once
	listener := &stubListener{
		accept: func() (net.Conn, error) {
			enteredOnce.Do(func() { close(enteredAccept) })
			<-releaseAccept
			return nil, stubAcceptError{temporary: true}
		},
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.serve(listener)
	}()

	select {
	case <-enteredAccept:
	case <-time.After(3 * time.Second):
		t.Fatal("redirect server did not enter Accept")
	}

	// Cancel before allowing Accept to return. The retry timer is therefore
	// created with an already-canceled context and must not delay shutdown.
	cancel()
	close(releaseAccept)

	select {
	case err := <-serveErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("serve() error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("redirect server remained blocked in accept retry")
	}
}

func TestServerRejectsNilAndDuplicateListeners(t *testing.T) {
	s := newTestServer(t, context.Background(), nil)
	if err := s.serve(nil); err == nil {
		t.Fatal("serve(nil) succeeded")
	}

	first := &stubListener{accept: func() (net.Conn, error) {
		return nil, net.ErrClosed
	}}
	if err := s.attachListener(first); err != nil {
		t.Fatalf("attachListener(first) error = %v", err)
	}

	var secondClosed atomic.Bool
	second := &stubListener{
		accept: func() (net.Conn, error) {
			return nil, net.ErrClosed
		},
		close: func() error {
			secondClosed.Store(true)
			return nil
		},
	}
	if err := s.serve(second); err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("serve(second) error = %v, want already-listening error", err)
	}
	if !secondClosed.Load() {
		t.Fatal("duplicate listener was not closed")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestServerCloseReportsListenerError(t *testing.T) {
	closeErr := errors.New("listener close failed")
	listener := &stubListener{
		accept: func() (net.Conn, error) {
			return nil, net.ErrClosed
		},
		close: func() error {
			return closeErr
		},
	}
	s := newTestServer(t, nil, nil)
	if err := s.attachListener(listener); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close() error = %v, want listener close error", err)
	}
	if err := s.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("second Close() error = %v, want listener close error", err)
	}
	if err := s.attachListener(listener); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("attachListener() after Close error = %v, want net.ErrClosed", err)
	}

	serverSide, clientSide := net.Pipe()
	defer func() { _ = serverSide.Close() }()
	defer func() { _ = clientSide.Close() }()
	if s.registerConn(serverSide) {
		t.Fatal("registerConn() accepted a connection after Close")
	}
}
