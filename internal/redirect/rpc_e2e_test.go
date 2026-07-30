package redirect

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	rpcClient "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	rpcServer "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/server"
	serverconfig "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
)

const redirectRPCTestUUID = "redirect-rpc-e2e-user"

func reserveRedirectRPCLoopbackAddr(t *testing.T) string {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve gRPC listener: %v", err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("release gRPC listener: %v", err)
	}
	return addr
}

func waitForRedirectRPCListener(t *testing.T, addr string) {
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
	t.Fatalf("gRPC server did not listen on %s", addr)
}

func startRedirectRPCServer(t *testing.T) string {
	t.Helper()
	addr := reserveRedirectRPCLoopbackAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := rpcServer.NewServer(
		ctx,
		serverconfig.Users{{UUID: redirectRPCTestUUID}},
		nil,
		nil,
	)
	if err != nil {
		cancel()
		t.Fatal(err)
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.ListenAndServe(addr)
	}()
	waitForRedirectRPCListener(t, addr)

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("gRPC server shutdown error = %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("gRPC server did not stop")
		}
	})
	return addr
}

// TestServeConnRoutesThroughRPC composes the redirect frontend with the real
// gRPC client, server, server-side router, direct egress, and a TCP target. The
// kernel-specific SO_ORIGINAL_DST boundary is covered separately by the
// privileged netfilter integration test.
func TestServeConnRoutesThroughRPC(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		{MatchType: router.TypeDefault, Destination: router.EgressDirect},
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

	targetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = targetListener.Close() })
	targetAddr := targetListener.Addr().(*net.TCPAddr)

	payload := []byte("redirect through rpc")
	targetErr := make(chan error, 1)
	go func() {
		conn, err := targetListener.Accept()
		if err != nil {
			targetErr <- err
			return
		}
		defer func() { _ = conn.Close() }()

		got := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, got); err != nil {
			targetErr <- err
			return
		}
		if !bytes.Equal(got, payload) {
			targetErr <- errors.New("target received unexpected payload")
			return
		}
		_, err = conn.Write(got)
		targetErr <- err
	}()

	rpcClient.SetUUID(redirectRPCTestUUID)
	if err := rpcClient.Init(startRedirectRPCServer(t), "", false, 1, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rpcClient.Destroy)

	s := newTestServer(t, context.Background(), &Config{MaxConnections: 4})
	t.Cleanup(func() { _ = s.Close() })
	s.resolveDestination = func(net.Conn) (*net.TCPAddr, error) {
		return targetAddr, nil
	}
	s.resolveRoute = func(string) (transport.Transport, error) {
		return rpcClient.New()
	}

	redirectSide, clientSide := net.Pipe()
	defer func() { _ = clientSide.Close() }()
	if err := clientSide.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}

	proxyErr := make(chan error, 1)
	go func() {
		proxyErr <- s.ServeConn(redirectSide)
	}()

	if _, err := clientSide.Write(payload); err != nil {
		t.Fatalf("write redirected payload: %v", err)
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(clientSide, reply); err != nil {
		t.Fatalf("read redirected reply: %v", err)
	}
	if !bytes.Equal(reply, payload) {
		t.Fatalf("redirected reply = %q, want %q", reply, payload)
	}
	if err := clientSide.Close(); err != nil {
		t.Fatalf("close redirected client: %v", err)
	}

	select {
	case err := <-proxyErr:
		if err != nil {
			t.Fatalf("ServeConn() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServeConn() did not return after the client closed")
	}

	select {
	case err := <-targetErr:
		if err != nil {
			t.Fatalf("target server error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("target server did not complete")
	}
}
