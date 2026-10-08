package rpc_test

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
)

// localPair returns the proxy's side of a local client connection, the way a
// SOCKS or HTTP front end passes it to Proxy, and the client application's
// own end, which observes how the proxy ended the stream.
func localPair(t *testing.T) (proxySide, app net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	app, err = net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	proxySide = <-accepted
	if proxySide == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() {
		_ = proxySide.Close()
		_ = app.Close()
	})
	return utils.OnceNetConn(proxySide), app
}

// startResponder answers each connection with payload, then resets it or
// closes it in an orderly way. hold keeps the connection open instead.
func startResponder(t *testing.T, payload string, reset, hold bool) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var held sync.WaitGroup
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop); held.Wait() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			held.Go(func() {
				defer func() { _ = conn.Close() }()
				_, _ = conn.Write([]byte(payload))
				if hold {
					<-stop
					return
				}
				if reset {
					time.Sleep(100 * time.Millisecond)
					_ = conn.(*net.TCPConn).SetLinger(0)
				}
			})
		}
	}()
	return listener.Addr().String()
}

// readEnd reads app until its stream ends and reports how it ended.
func readEnd(app net.Conn) (string, error) {
	_ = app.SetReadDeadline(time.Now().Add(10 * time.Second))
	got, err := io.ReadAll(app)
	return string(got), err
}

// proxyThroughTunnel runs one session the way a front end does. With
// closeAfter it then closes the client connection, as SOCKS and HTTP do once
// Proxy returns; without it the test writes the front end's reply itself.
func proxyThroughTunnel(t *testing.T, target string, closeAfter bool) (proxySide, app net.Conn, done chan error) {
	t.Helper()
	c, err := client.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	proxySide, app = localPair(t)
	done = make(chan error, 1)
	go func() {
		err := c.Proxy(context.Background(), target, make(chan string, 1), proxySide, proxySide)
		if closeAfter {
			_ = proxySide.Close()
		}
		done <- err
	}()
	return proxySide, app, done
}

func TestEndToEnd_TargetResetReachesClientAsReset(t *testing.T) {
	routeAllDirect(t)
	connectClient(t, startProxyServer(t))
	_, app, done := proxyThroughTunnel(t, startResponder(t, "partial", true, false), true)

	if _, err := readEnd(app); !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("client saw %v, want a connection reset for a truncated response", err)
	}
	if err := <-done; err == nil {
		t.Fatal("Proxy reported success for a session the target reset")
	}
}

func TestEndToEnd_OrderlyCloseStaysOrderly(t *testing.T) {
	routeAllDirect(t)
	connectClient(t, startProxyServer(t))
	_, app, done := proxyThroughTunnel(t, startResponder(t, "complete response", false, false), true)

	got, err := readEnd(app)
	if err != nil || got != "complete response" {
		t.Fatalf("client read %q with %v, want the whole response and a clean EOF", got, err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}
}

func TestEndToEnd_TunnelLossReachesClientAsReset(t *testing.T) {
	routeAllDirect(t)
	r := startTestRelay(t, startProxyServer(t))
	connectClient(t, r.addr())
	_, app, done := proxyThroughTunnel(t, startResponder(t, "streaming", false, true), true)

	buf := make([]byte, len("streaming"))
	_ = app.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(app, buf); err != nil {
		t.Fatalf("session never delivered data: %v", err)
	}
	r.resetAll()

	if _, err := readEnd(app); !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("client saw %v after the tunnel was reset, want a connection reset", err)
	}
	if err := <-done; err == nil {
		t.Fatal("Proxy reported success for a session whose tunnel was lost")
	}
}

// Before the server accepts a session the front end owns the connection: it
// must still be able to write its own failure reply, so a refused dial must
// not reset it.
func TestEndToEnd_RejectedSessionLeavesClientWritable(t *testing.T) {
	routeAllDirect(t)
	connectClient(t, startProxyServer(t))
	refused := freeLoopbackAddr(t) // nothing listens here
	proxySide, app, done := proxyThroughTunnel(t, refused, false)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Proxy succeeded against a refused target")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Proxy did not report the refused target")
	}
	if _, err := proxySide.Write([]byte("failure reply")); err != nil {
		t.Fatalf("front end could not write its reply: %v", err)
	}
	_ = proxySide.Close()
	if got, err := readEnd(app); err != nil || got != "failure reply" {
		t.Fatalf("client read %q with %v, want the front end's reply", got, err)
	}
}

// testRelay sits between the client pool and the server so a test can break
// the tunnel's TCP connections the way a middlebox would.
type testRelay struct {
	listener net.Listener
	mu       sync.Mutex
	conns    []*net.TCPConn
}

func startTestRelay(t testing.TB, server string) *testRelay {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &testRelay{listener: listener}
	var pipes sync.WaitGroup
	t.Cleanup(func() {
		_ = listener.Close()
		r.resetAll()
		pipes.Wait()
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			upstream, err := net.Dial("tcp", server)
			if err != nil {
				_ = conn.Close()
				continue
			}
			r.mu.Lock()
			r.conns = append(r.conns, conn.(*net.TCPConn))
			r.mu.Unlock()
			pipes.Go(func() {
				_, _ = io.Copy(upstream, conn)
				_ = upstream.Close()
			})
			pipes.Go(func() {
				_, _ = io.Copy(conn, upstream)
				_ = conn.Close()
			})
		}
	}()
	return r
}

func (r *testRelay) addr() string { return r.listener.Addr().String() }

// resetAll sends a TCP reset on every client connection the relay carries.
func (r *testRelay) resetAll() {
	r.mu.Lock()
	conns := r.conns
	r.conns = nil
	r.mu.Unlock()
	for _, conn := range conns {
		_ = conn.SetLinger(0)
		_ = conn.Close()
	}
}
