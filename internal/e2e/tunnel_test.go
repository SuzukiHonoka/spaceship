package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/server"
	serverconfig "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
)

const (
	// helperEnvKey turns this test binary into the far-end proxy server.
	helperEnvKey  = "SPACESHIP_E2E_TUNNEL_SERVER"
	helperAddrKey = "SPACESHIP_E2E_TUNNEL_ADDR"
	tunnelUUID    = "tunnel-e2e-user"
)

// TestMain re-executes this binary as the far-end proxy server when asked.
//
// The two ends must be separate processes because the router is process-global:
// the client end has to route a destination to the proxy egress while the server
// end routes that same destination to direct. In one process the server's dial
// would match the proxy route and go straight back into the tunnel.
func TestMain(m *testing.M) {
	switch {
	case os.Getenv(helperEnvKey) == "1":
		// Minimal far-end server, wired by hand.
		runTunnelServer()
		return
	case os.Getenv(systemConfigEnvKey) != "":
		// Full server booted from a real config file through api.Launcher,
		// exactly as cmd/spaceship does.
		runSystemServer(os.Getenv(systemConfigEnvKey))
		return
	}
	os.Exit(m.Run())
}

// runTunnelServer is the far-end process: a proxy server that dials targets
// directly. It never returns; the parent kills it during cleanup.
func runTunnelServer() {
	addr := os.Getenv(helperAddrKey)
	if addr == "" {
		fmt.Fprintln(os.Stderr, "tunnel helper: no listen address supplied")
		os.Exit(2)
	}

	if err := router.SetRoutes(router.Routes{
		{MatchType: router.TypeDefault, Destination: router.EgressDirect},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "tunnel helper: SetRoutes: %v\n", err)
		os.Exit(2)
	}

	srv, err := server.NewServer(context.Background(),
		serverconfig.Users{{UUID: tunnelUUID}}, nil, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tunnel helper: NewServer: %v\n", err)
		os.Exit(2)
	}
	if err := srv.ListenAndServe(addr); err != nil {
		fmt.Fprintf(os.Stderr, "tunnel helper: ListenAndServe: %v\n", err)
		os.Exit(2)
	}
}

// syncBuffer collects the helper's output for diagnostics without racing the
// goroutine os/exec uses to copy it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func listenerReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	return lastErr
}

// startTunnelServerProcess spawns the far-end proxy server and returns its address.
func startTunnelServerProcess(t *testing.T) string {
	t.Helper()

	addr := freeLoopbackAddr(t)
	// The -test.run pattern is a belt-and-braces guard: TestMain branches before
	// running any tests, but if that ever changed we still want zero tests here.
	cmd := exec.Command(os.Args[0], "-test.run=XXX_TUNNEL_HELPER_RUNS_NO_TESTS")
	cmd.Env = append(os.Environ(), helperEnvKey+"=1", helperAddrKey+"="+addr)

	out := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = out, out

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the tunnel server process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	if err := listenerReady(addr, 30*time.Second); err != nil {
		t.Fatalf("tunnel server process never listened on %s: %v\nprocess output:\n%s",
			addr, err, out.String())
	}
	return addr
}

// connectTunnelClient points the client pool at the far-end server and routes
// everything through it.
func connectTunnelClient(t *testing.T, serverAddr string) {
	t.Helper()
	routeAll(t, router.EgressProxy)
	client.SetUUID(tunnelUUID)
	if err := client.Init(serverAddr, "", false, 1, nil); err != nil {
		t.Fatalf("client.Init() error = %v", err)
	}
	t.Cleanup(client.Destroy)
}

// TestFullChain_SOCKS5ConnectThroughTunnel is the complete production path for
// TCP: a SOCKS5 client → spaceship's SOCKS front end → router → gRPC tunnel →
// far-end server → direct dial → target, and all the way back.
//
// Every other test covers one leg or the other. This is the only one that proves
// the legs actually fit together.
func TestFullChain_SOCKS5ConnectThroughTunnel(t *testing.T) {
	echo := startTCPEcho(t)
	connectTunnelClient(t, startTunnelServerProcess(t))
	socksAddr := startSocksServer(t)

	conn := socks5Connect(t, socksAddr)
	rep, _ := socks5Command(t, conn, cmdConnect, echo)
	if rep != repSuccess {
		t.Fatalf("CONNECT through the tunnel: reply = %d, want success", rep)
	}

	payload := []byte("socks5 to target through the tunnel")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("writing through the tunnel: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("reading the echo back through the tunnel: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("round trip payload = %q, want %q", got, payload)
	}
}

// TestFullChain_SOCKS5UDPThroughTunnel is the complete production path for UDP,
// and the highest-value test in the suite: SOCKS5 UDP ASSOCIATE → the relay →
// router → rpc client's typed UDP handshake → far-end server's resolveTarget →
// connected UDP dial → target, and the reverse relay all the way back.
//
// It is the only test that would catch the two ends disagreeing about the
// Network enum, which an older server silently treats as TCP.
func TestFullChain_SOCKS5UDPThroughTunnel(t *testing.T) {
	echo := startUDPEcho(t)
	connectTunnelClient(t, startTunnelServerProcess(t))
	socksAddr := startSocksServer(t)

	ctrl := socks5Connect(t, socksAddr)
	rep, relayAddr := socks5Command(t, ctrl, cmdUDPAssociate, nil)
	if rep != repSuccess {
		t.Fatalf("UDP ASSOCIATE through the tunnel: reply = %d, want success", rep)
	}

	sock := clientUDPSocket(t)
	payload := []byte("datagram to target through the tunnel")
	if _, err := sock.WriteTo(encodeUDPRequest(t, echo, payload), relayAddr); err != nil {
		t.Fatalf("sending datagram to the relay: %v", err)
	}

	if err := sock.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 65535)
	n, _, err := sock.ReadFrom(buf)
	if err != nil {
		t.Fatalf("no datagram completed the full chain: %v", err)
	}

	src, got := decodeUDPReply(t, buf[:n])
	if !bytes.Equal(got, payload) {
		t.Errorf("round trip payload = %q, want %q", got, payload)
	}
	if !src.IP.Equal(echo.IP) || src.Port != echo.Port {
		t.Errorf("reply source = %v, want the echo target %v", src, echo)
	}
}

// TestFullChain_SOCKS5UDPMultipleTargetsThroughTunnel verifies the relay keeps
// independent tunnelled flows per destination, each with its own gRPC stream.
func TestFullChain_SOCKS5UDPMultipleTargetsThroughTunnel(t *testing.T) {
	first, second := startUDPEcho(t), startUDPEcho(t)
	connectTunnelClient(t, startTunnelServerProcess(t))
	socksAddr := startSocksServer(t)

	ctrl := socks5Connect(t, socksAddr)
	rep, relayAddr := socks5Command(t, ctrl, cmdUDPAssociate, nil)
	if rep != repSuccess {
		t.Fatalf("UDP ASSOCIATE through the tunnel: reply = %d, want success", rep)
	}

	sock := clientUDPSocket(t)
	buf := make([]byte, 65535)

	for i, target := range []*net.UDPAddr{first, second, first} {
		payload := []byte{byte('A' + i)}
		if _, err := sock.WriteTo(encodeUDPRequest(t, target, payload), relayAddr); err != nil {
			t.Fatalf("target %d: sending datagram: %v", i, err)
		}
		if err := sock.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
			t.Fatalf("target %d: SetReadDeadline: %v", i, err)
		}
		n, _, err := sock.ReadFrom(buf)
		if err != nil {
			t.Fatalf("target %d: no reply through the tunnel: %v", i, err)
		}
		src, got := decodeUDPReply(t, buf[:n])
		if !bytes.Equal(got, payload) {
			t.Errorf("target %d: payload = %q, want %q", i, got, payload)
		}
		if !src.IP.Equal(target.IP) || src.Port != target.Port {
			t.Errorf("target %d: reply source = %v, want %v", i, src, target)
		}
	}
}

// TestFullChain_UnauthenticatedClientRejected covers the far-end auth
// interceptor over the full chain.
//
// It also validates the tests above: if traffic were somehow reaching the target
// without traversing the tunnel, an unknown user would still succeed here.
func TestFullChain_UnauthenticatedClientRejected(t *testing.T) {
	echo := startTCPEcho(t)
	serverAddr := startTunnelServerProcess(t)

	routeAll(t, router.EgressProxy)
	client.SetUUID("not-a-configured-user")
	if err := client.Init(serverAddr, "", false, 1, nil); err != nil {
		t.Fatalf("client.Init() error = %v", err)
	}
	t.Cleanup(client.Destroy)

	socksAddr := startSocksServer(t)
	conn := socks5Connect(t, socksAddr)

	start := time.Now()
	rep, _ := socks5Command(t, conn, cmdConnect, echo)
	elapsed := time.Since(start)

	if rep == repSuccess {
		t.Error("CONNECT succeeded with an unknown user: either the far-end auth " +
			"interceptor is not enforcing, or the traffic never reached the tunnel")
	}

	// The server rejects at the interceptor, before the stream carries anything.
	// The client must surface that immediately rather than sitting out the ack
	// timer, which is what a user experiences as a hung connection attempt.
	if elapsed > 5*time.Second {
		t.Errorf("rejection surfaced after %v; a session that has already failed "+
			"must not wait out the full ack timeout", elapsed)
	}
}
