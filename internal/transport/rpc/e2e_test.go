// Package rpc_test holds end-to-end tests that drive a real gRPC server and a
// real client connection pool against a real target.
//
// These live in an external test package because they import both the client and
// server packages, which each import the parent rpc package for their options.
// Everything below exercises the full path — auth interceptor, stream handshake,
// routing, and payload copy — which the per-package unit tests deliberately stub
// out. UDP over gRPC in particular is new, and its wire format changed twice, so
// a round trip through the actual stack is the only thing that proves the
// handshake and the payload framing agree end to end.
package rpc_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/server"
	serverconfig "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
)

const testUUID = "e2e-test-user"

// freeLoopbackAddr reserves a loopback port and releases it. ListenAndServe
// binds the address itself, so there is no way to hand it a listener; the small
// reuse window is acceptable for a test.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a loopback port: %v", err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}
	return addr
}

// startProxyServer runs a real gRPC proxy server and returns its address.
func startProxyServer(t *testing.T) string {
	t.Helper()

	addr := freeLoopbackAddr(t)
	ctx, cancel := context.WithCancel(context.Background())

	srv, err := server.NewServer(ctx, serverconfig.Users{{UUID: testUUID}}, nil, nil)
	if err != nil {
		cancel()
		t.Fatalf("NewServer() error = %v", err)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe(addr) }()

	t.Cleanup(func() {
		cancel()
		select {
		case <-serveErr:
		case <-time.After(10 * time.Second):
			t.Error("proxy server did not shut down within 10s")
		}
	})

	waitForListener(t, addr)
	return addr
}

func waitForListener(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("proxy server never started listening on %s", addr)
}

// connectClient initializes the client connection pool against addr.
func connectClient(t *testing.T, addr string) {
	t.Helper()
	client.SetUUID(testUUID)
	if err := client.Init(addr, "", false, 1, nil); err != nil {
		t.Fatalf("client.Init() error = %v", err)
	}
	t.Cleanup(client.Destroy)
}

// routeAllDirect makes the server dial targets itself, which is what a real
// deployment does for the far end of the tunnel.
func routeAllDirect(t *testing.T) {
	t.Helper()
	if err := router.SetRoutes(router.Routes{
		{MatchType: router.TypeDefault, Destination: router.EgressDirect},
	}); err != nil {
		t.Fatalf("SetRoutes() error = %v", err)
	}
}

// startUDPEcho runs a UDP echo server and returns its address.
func startUDPEcho(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("udp echo listen: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, err := pc.WriteTo(buf[:n], addr); err != nil {
				return
			}
		}
	}()
	return pc.LocalAddr().String()
}

// TestEndToEnd_UDPRoundTripOverGRPC drives a datagram through the whole stack:
// client DialPacket → typed UDP handshake → server resolveTarget → direct dial →
// echo target → reverse path → client ReadFrom.
//
// This is the test that would have caught a client/server disagreement about the
// Network enum, which an old server silently treats as TCP.
func TestEndToEnd_UDPRoundTripOverGRPC(t *testing.T) {
	routeAllDirect(t)
	echoAddr := startUDPEcho(t)
	connectClient(t, startProxyServer(t))

	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New() error = %v", err)
	}
	defer c.Close()

	pc, err := c.DialPacket("udp", echoAddr)
	if err != nil {
		t.Fatalf("DialPacket(%s) error = %v", echoAddr, err)
	}
	defer pc.Close()

	target, err := net.ResolveUDPAddr("udp", echoAddr)
	if err != nil {
		t.Fatalf("resolve echo addr: %v", err)
	}

	payload := []byte("ping over grpc")
	if _, err := pc.WriteTo(payload, target); err != nil {
		t.Fatalf("WriteTo() error = %v", err)
	}

	if err := pc.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	buf := make([]byte, 2048)
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom() error = %v (no datagram completed the round trip)", err)
	}
	if !bytes.Equal(buf[:n], payload) {
		t.Errorf("round trip payload = %q, want %q", buf[:n], payload)
	}
}

// TestEndToEnd_UDPMultipleDatagrams verifies the stream carries a sequence of
// datagrams in order, not just the first. Each Send/Recv is one message on a
// long-lived stream, so a framing error would surface here rather than above.
func TestEndToEnd_UDPMultipleDatagrams(t *testing.T) {
	routeAllDirect(t)
	echoAddr := startUDPEcho(t)
	connectClient(t, startProxyServer(t))

	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New() error = %v", err)
	}
	defer c.Close()

	pc, err := c.DialPacket("udp", echoAddr)
	if err != nil {
		t.Fatalf("DialPacket() error = %v", err)
	}
	defer pc.Close()

	target, err := net.ResolveUDPAddr("udp", echoAddr)
	if err != nil {
		t.Fatalf("resolve echo addr: %v", err)
	}

	const datagrams = 8
	buf := make([]byte, 2048)
	for i := range datagrams {
		payload := []byte{byte('a' + i), byte('0' + i)}
		if _, err := pc.WriteTo(payload, target); err != nil {
			t.Fatalf("datagram %d: WriteTo() error = %v", i, err)
		}
		if err := pc.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
			t.Fatalf("SetReadDeadline() error = %v", err)
		}
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			t.Fatalf("datagram %d: ReadFrom() error = %v", i, err)
		}
		if !bytes.Equal(buf[:n], payload) {
			t.Fatalf("datagram %d: got %q, want %q", i, buf[:n], payload)
		}
	}
}

// signalWriter accumulates proxied bytes and signals once it has seen want of
// them. It is mutex-guarded because Proxy writes from its own goroutine.
type signalWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	want int
	sent bool
	done chan []byte
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if !w.sent && w.buf.Len() >= w.want {
		w.sent = true
		w.done <- bytes.Clone(w.buf.Bytes())
	}
	return n, err
}

// TestEndToEnd_TCPRoundTripOverGRPC exercises the primary data path: a proxied
// TCP session carried as a bidirectional gRPC stream.
//
// The source is a pipe rather than a bytes.Reader because Proxy models a live
// client connection: an immediate EOF on the source ends the session, and the
// reply would be cut off before it arrives.
func TestEndToEnd_TCPRoundTripOverGRPC(t *testing.T) {
	routeAllDirect(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp echo listen: %v", err)
	}
	defer ln.Close()

	payload := []byte("hello through the tunnel")
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		_, _ = conn.Write(buf)
	}()

	connectClient(t, startProxyServer(t))

	c, err := client.New()
	if err != nil {
		t.Fatalf("client.New() error = %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	srcReader, srcWriter := io.Pipe()
	received := make(chan []byte, 1)
	dst := &signalWriter{want: len(payload), done: received}

	proxyErr := make(chan error, 1)
	go func() {
		proxyErr <- c.Proxy(ctx, ln.Addr().String(), make(chan string, 1), dst, srcReader)
	}()

	if _, err := srcWriter.Write(payload); err != nil {
		t.Fatalf("writing to the proxied source: %v", err)
	}

	select {
	case got := <-received:
		if !bytes.Equal(got, payload) {
			t.Errorf("proxied payload = %q, want %q", got, payload)
		}
	case err := <-proxyErr:
		t.Fatalf("Proxy() returned before the reply arrived: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("no reply completed the round trip")
	}

	// Closing the source ends the session, which must let Proxy return.
	_ = srcWriter.Close()
	select {
	case <-proxyErr:
	case <-time.After(20 * time.Second):
		t.Error("Proxy() did not return after the source closed")
	}
}

// TestEndToEnd_FailFastWhenServerUnreachable verifies an unreachable server
// produces a prompt error rather than a hang.
//
// This is the regression guard for WaitForReady: with it set, stream creation
// blocks until the channel becomes READY, and since the stream context carries
// no deadline an outage turned every proxied request into an indefinite hang.
func TestEndToEnd_FailFastWhenServerUnreachable(t *testing.T) {
	oldDialTimeout := transport.GetDialTimeout()
	transport.SetDialTimeout(2 * time.Second)
	t.Cleanup(func() { transport.SetDialTimeout(oldDialTimeout) })

	// Nothing is listening here.
	connectClient(t, freeLoopbackAddr(t))

	c, err := client.New()
	if err != nil {
		// Failing this early is also acceptable — it is still fail-fast.
		return
	}
	defer c.Close()

	done := make(chan error, 1)
	go func() {
		pc, err := c.DialPacket("udp", "127.0.0.1:9")
		if pc != nil {
			_ = pc.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("DialPacket() succeeded against an unreachable server")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("DialPacket() blocked on an unreachable server: RPCs must fail fast, " +
			"so WaitForReady must stay unset")
	}
}
