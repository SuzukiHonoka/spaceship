//go:build linux

package tun

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/forward"
	rpcTransport "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	rpcClient "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	rpcServer "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/server"
	serverConfig "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	pkgDNS "github.com/SuzukiHonoka/spaceship/v2/pkg/dns"
	"github.com/miekg/dns"
	"golang.org/x/net/proxy"
	"golang.org/x/sys/unix"
)

const (
	tunIntegrationEnv       = "SPACESHIP_TUN_INTEGRATION"
	tunIntegrationHelperEnv = "SPACESHIP_TUN_INTEGRATION_HELPER"
	tunIntegrationUUID      = "tun-kernel-integration-user"
)

// TestKernelTUNIntegration exercises a real IFF_TUN descriptor and Linux route
// through the gVisor TCP forwarder plus TCP/UDP DNS interception. It runs in a
// disposable network namespace and is opt-in because it requires unshare,
// iproute2, /dev/net/tun, and network-administration privileges.
func TestKernelTUNIntegration(t *testing.T) {
	if os.Getenv(tunIntegrationHelperEnv) == "1" {
		runKernelTUNIntegrationHelper(t)
		return
	}
	if os.Getenv(tunIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run the isolated TUN integration test", tunIntegrationEnv)
	}
	for _, name := range []string{"unshare", "ip"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("required integration tool %q not found: %v", name, err)
		}
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Fatalf("/dev/net/tun unavailable: %v", err)
	}

	args := []string{"--net", "--mount", "--mount-proc", "--fork"}
	if os.Geteuid() != 0 {
		args = append([]string{"--user", "--map-root-user"}, args...)
	}
	args = append(args,
		"--",
		os.Args[0],
		"-test.run=^TestKernelTUNIntegration$",
		"-test.count=1",
		"-test.v",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "unshare", args...)
	cmd.Env = append(os.Environ(), tunIntegrationHelperEnv+"=1")
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("isolated TUN test timed out: %v\n%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("isolated TUN test failed: %v\n%s", err, output)
	}
	t.Logf("isolated TUN test output:\n%s", output)
}

func runKernelTUNIntegrationHelper(t *testing.T) {
	runTUNCommand(t, "ip", "link", "set", "lo", "up")

	t.Run("external descriptor ownership", testKernelExternalDescriptorOwnership)

	originalMark := transport.BypassMark()
	originalResolver := transport.OutboundResolver()
	transport.SetBypassMark(DefaultBypassMark)
	t.Cleanup(func() {
		transport.SetOutboundResolver(originalResolver)
		transport.SetBypassMark(originalMark)
	})

	resolver := startKernelResolver(t)
	transport.SetOutboundResolver(transport.NewFixedResolver(resolver.address, time.Second))
	assertOutboundSocketMark(t, DefaultBypassMark)

	controlAddress := startKernelRPCServer(t, resolver.address)

	targetDialer := &kernelTargetDialer{
		targets: make(chan kernelDialTarget, 4),
	}
	if err := forward.Attach(targetDialer); err != nil {
		t.Fatalf("attach target dialer: %v", err)
	}
	if err := router.SetRoutes(router.Routes{{
		Destination: router.EgressForward,
		MatchType:   router.TypeDefault,
	}}); err != nil {
		t.Fatalf("install server route: %v", err)
	}
	t.Cleanup(func() {
		_ = router.SetRoutes(nil)
		_ = forward.Attach(nil)
	})

	ctx, cancel := context.WithCancel(context.Background())
	service, err := New(ctx, Config{
		Name:      "ss-tun-test",
		MTU:       1400,
		RouteMode: "manual",
		DNS: DNSConfig{
			Enabled:        true,
			QueryTimeout:   time.Second,
			TCPIdleTimeout: 2 * time.Second,
		},
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = service.Close()
	})

	service.resolveRoute = func(string) (transport.Transport, error) {
		return rpcClient.New()
	}

	runErr := make(chan error, 1)
	go func() { runErr <- service.Run() }()

	runTUNCommand(t, "ip", "addr", "add", "10.23.0.1/24", "dev", service.Name())
	runTUNCommand(t, "ip", "-6", "addr", "add", "fd00:23::1/64", "dev", service.Name())
	runTUNCommand(t, "ip", "route", "add", "default", "dev", service.Name(), "table", "100")
	runTUNCommand(t, "ip", "-6", "route", "add", "default", "dev", service.Name(), "table", "100")
	runTUNCommand(t, "ip", "route", "add", "192.0.2.0/24", "dev", "lo")
	runTUNCommand(t, "ip", "-6", "route", "add", "2001:db8:ffff::/64", "dev", "lo")

	markWithMask := fmt.Sprintf("%#x/0xffffffff", DefaultBypassMark)
	runTUNCommand(t, "ip", "rule", "add", "pref", "100", "fwmark", markWithMask, "lookup", "main")
	runTUNCommand(t, "ip", "rule", "add", "pref", "110", "not", "fwmark", markWithMask, "lookup", "100")
	runTUNCommand(t, "ip", "-6", "rule", "add", "pref", "100", "fwmark", markWithMask, "lookup", "main")
	runTUNCommand(t, "ip", "-6", "rule", "add", "pref", "110", "not", "fwmark", markWithMask, "lookup", "100")
	assertKernelPolicyRouting(t, service.Name(), DefaultBypassMark)

	// Initialize only after the catch-all rules are live. Both the hostname
	// lookup and control connection must therefore carry the bypass mark.
	rpcClient.SetUUID(tunIntegrationUUID)
	mux, err := RequiredRPCPoolSize(service.cfg, rpcTransport.MaxConcurrentStreams)
	if err != nil {
		t.Fatalf("calculate TUN gRPC pool size: %v", err)
	}
	if mux != 2 {
		t.Fatalf("default TUN gRPC pool size = %d, want 2", mux)
	}
	if err := rpcClient.Init(controlAddress, "", false, mux, nil); err != nil {
		t.Fatalf("initialize gRPC client: %v", err)
	}
	t.Cleanup(func() {
		rpcClient.Destroy()
		rpcClient.SetUUID("")
	})
	waitForKernelRPCClient(t)
	select {
	case network := <-resolver.controlQueries:
		if network != "udp" {
			t.Fatalf("control hostname resolver network = %q, want udp", network)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("gRPC control hostname did not use the configured resolver")
	}

	t.Run("IPv4 TCP through authenticated RPC", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp4", "198.51.100.20:443", 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, 4)
		if _, err := io.ReadFull(conn, response); err != nil {
			t.Fatal(err)
		}
		if string(response) != "PING" {
			t.Fatalf("TCP response = %q, want PING", response)
		}
		assertKernelTarget(t, targetDialer.targets, "tcp", "198.51.100.20:443")
	})

	t.Run("IPv6 TCP through authenticated RPC", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp6", "[2001:db8::20]:443", 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, 4)
		if _, err := io.ReadFull(conn, response); err != nil {
			t.Fatal(err)
		}
		if string(response) != "PING" {
			t.Fatalf("TCP response = %q, want PING", response)
		}
		assertKernelTarget(t, targetDialer.targets, "tcp", "[2001:db8::20]:443")
	})

	t.Run("IPv4 UDP DNS through server resolver", func(t *testing.T) {
		conn, err := net.DialTimeout("udp4", "198.51.100.53:53", 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		query := kernelDNSQuery(t, 0x1001)
		if _, err := conn.Write(query); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, dns.MaxMsgSize)
		n, err := conn.Read(response)
		if err != nil {
			t.Fatal(err)
		}
		assertKernelDNSResponse(t, response[:n], 0x1001)
		assertKernelResolverNetwork(t, resolver.knownQueries, "udp")
	})

	t.Run("IPv4 TCP DNS through server resolver", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp4", "198.51.100.53:53", 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

		query := kernelDNSQuery(t, 0x1002)
		frame := make([]byte, 2+len(query))
		binary.BigEndian.PutUint16(frame[:2], uint16(len(query)))
		copy(frame[2:], query)
		if _, err := conn.Write(frame); err != nil {
			t.Fatal(err)
		}
		var size [2]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, int(binary.BigEndian.Uint16(size[:])))
		if _, err := io.ReadFull(conn, response); err != nil {
			t.Fatal(err)
		}
		assertKernelDNSResponse(t, response, 0x1002)
		assertKernelResolverNetwork(t, resolver.knownQueries, "tcp")
	})

	t.Run("IPv6 TCP DNS through server resolver", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp6", "[2001:db8::53]:53", 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

		query := kernelDNSQuery(t, 0x1004)
		frame := make([]byte, 2+len(query))
		binary.BigEndian.PutUint16(frame[:2], uint16(len(query)))
		copy(frame[2:], query)
		if _, err := conn.Write(frame); err != nil {
			t.Fatal(err)
		}
		var size [2]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, int(binary.BigEndian.Uint16(size[:])))
		if _, err := io.ReadFull(conn, response); err != nil {
			t.Fatal(err)
		}
		assertKernelDNSResponse(t, response, 0x1004)
		assertKernelResolverNetwork(t, resolver.knownQueries, "tcp")
	})

	t.Run("IPv6 UDP DNS through server resolver", func(t *testing.T) {
		conn, err := net.DialTimeout("udp6", "[2001:db8::53]:53", 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		query := kernelDNSQuery(t, 0x1003)
		if _, err := conn.Write(query); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, dns.MaxMsgSize)
		n, err := conn.Read(response)
		if err != nil {
			t.Fatal(err)
		}
		assertKernelDNSResponse(t, response[:n], 0x1003)
		assertKernelResolverNetwork(t, resolver.knownQueries, "udp")
	})

	t.Run("non-DNS UDP is rejected", func(t *testing.T) {
		conn, err := net.DialTimeout("udp4", "198.51.100.20:123", 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
		_, writeErr := conn.Write([]byte("unsupported"))
		var readErr error
		if writeErr == nil {
			var response [1]byte
			_, readErr = conn.Read(response[:])
		}
		if writeErr == nil && readErr == nil {
			t.Fatal("non-DNS UDP unexpectedly returned a response")
		}
	})

	cancel()
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Service.Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("TUN service did not stop")
	}
}

func testKernelExternalDescriptorOwnership(t *testing.T) {
	fd, name, err := createTUN("ss-tun-fd")
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := configureCreatedInterface(name, 1400); err != nil {
		t.Fatal(err)
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		t.Fatalf("make external TUN descriptor blocking before New: %v", err)
	}
	if flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0); err != nil {
		t.Fatal(err)
	} else if flags&unix.O_NONBLOCK != 0 {
		t.Fatal("external TUN descriptor remained nonblocking before New")
	}

	if _, err := New(context.Background(), Config{
		Name:           "ss-other-fd",
		FileDescriptor: &fd,
		MaxConnections: 2,
	}); err == nil || !strings.Contains(err.Error(), "external descriptor belongs to interface") {
		t.Fatalf("mismatched external TUN name error = %v", err)
	}
	if got, err := validateTUNDescriptor(fd); err != nil || got != name {
		t.Fatalf("original descriptor after rejected name = %q, %v; want %q, nil", got, err, name)
	}
	if flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0); err != nil {
		t.Fatal(err)
	} else if flags&unix.O_NONBLOCK != 0 {
		t.Fatal("rejected external descriptor changed caller file status")
	}

	service, err := New(context.Background(), Config{
		FileDescriptor: &fd,
		MaxConnections: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.Name() != name {
		t.Fatalf("external TUN name = %q, want %q", service.Name(), name)
	}
	if flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0); err != nil {
		t.Fatal(err)
	} else if flags&unix.O_NONBLOCK == 0 {
		t.Fatal("New did not make the shared external TUN file status nonblocking")
	}
	if err := service.Close(); err != nil {
		t.Fatalf("close external-FD service: %v", err)
	}
	if got, err := validateTUNDescriptor(fd); err != nil || got != name {
		t.Fatalf("original descriptor after Service.Close = %q, %v; want %q, nil", got, err, name)
	}
}

type kernelResolver struct {
	address        string
	controlQueries chan string
	knownQueries   chan string
}

func startKernelResolver(t *testing.T) kernelResolver {
	t.Helper()

	tcpListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen TCP resolver: %v", err)
	}
	udpConn, err := net.ListenPacket("udp4", tcpListener.Addr().String())
	if err != nil {
		_ = tcpListener.Close()
		t.Fatalf("listen UDP resolver: %v", err)
	}

	resolver := kernelResolver{
		address:        tcpListener.Addr().String(),
		controlQueries: make(chan string, 4),
		knownQueries:   make(chan string, 4),
	}
	handler := func(network string) dns.Handler {
		return dns.HandlerFunc(func(w dns.ResponseWriter, request *dns.Msg) {
			response := new(dns.Msg)
			response.SetReply(request)
			response.RecursionAvailable = true
			if len(request.Question) != 1 {
				response.Rcode = dns.RcodeFormatError
				_ = w.WriteMsg(response)
				return
			}

			question := request.Question[0]
			switch {
			case question.Name == "spaceship-control.test." && question.Qtype == dns.TypeA:
				response.Answer = []dns.RR{&dns.A{
					Hdr: dns.RR_Header{
						Name:   question.Name,
						Rrtype: dns.TypeA,
						Class:  dns.ClassINET,
						Ttl:    60,
					},
					A: net.IPv4(127, 0, 0, 1),
				}}
				select {
				case resolver.controlQueries <- network:
				default:
				}
			case question.Name == "spaceship-control.test." && question.Qtype == dns.TypeAAAA:
				select {
				case resolver.controlQueries <- network:
				default:
				}
			case question.Name == "known.test." && question.Qtype == dns.TypeA:
				response.Answer = []dns.RR{&dns.A{
					Hdr: dns.RR_Header{
						Name:   question.Name,
						Rrtype: dns.TypeA,
						Class:  dns.ClassINET,
						Ttl:    60,
					},
					A: net.IPv4(203, 0, 113, 53),
				}}
				select {
				case resolver.knownQueries <- network:
				default:
				}
			default:
				response.Rcode = dns.RcodeNameError
			}
			_ = w.WriteMsg(response)
		})
	}

	udpServer := &dns.Server{PacketConn: udpConn, Handler: handler("udp")}
	tcpServer := &dns.Server{Listener: tcpListener, Handler: handler("tcp")}
	udpStarted := make(chan struct{})
	tcpStarted := make(chan struct{})
	udpServer.NotifyStartedFunc = func() { close(udpStarted) }
	tcpServer.NotifyStartedFunc = func() { close(tcpStarted) }
	go func() { _ = udpServer.ActivateAndServe() }()
	go func() { _ = tcpServer.ActivateAndServe() }()
	t.Cleanup(func() {
		_ = tcpServer.Shutdown()
		_ = udpServer.Shutdown()
	})

	for network, started := range map[string]<-chan struct{}{
		"TCP": tcpStarted,
		"UDP": udpStarted,
	} {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s resolver did not start", network)
		}
	}
	return resolver
}

func startKernelRPCServer(t *testing.T, resolverAddress string) string {
	t.Helper()

	reserved, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve gRPC address: %v", err)
	}
	serverAddress := reserved.Addr().String()
	if err := reserved.Close(); err != nil {
		t.Fatalf("release reserved gRPC address: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	server, err := rpcServer.NewServer(
		ctx,
		serverConfig.Users{{UUID: tunIntegrationUUID}},
		nil,
		&pkgDNS.DNS{Type: pkgDNS.TypeCommon, Server: resolverAddress},
	)
	if err != nil {
		cancel()
		t.Fatalf("create gRPC server: %v", err)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe(serverAddress) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp4", serverAddress, 50*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("gRPC server did not listen at %s: %v", serverAddress, dialErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-serveErr:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("gRPC server shutdown error = %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("gRPC server did not stop")
		}
	})

	_, port, err := net.SplitHostPort(serverAddress)
	if err != nil {
		t.Fatalf("parse gRPC address %q: %v", serverAddress, err)
	}
	return net.JoinHostPort("spaceship-control.test", port)
}

func waitForKernelRPCClient(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, detail := range rpcClient.GetConnectionDetails() {
			if detail.ConnectivityState == "READY" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("gRPC client did not become ready: %s", rpcClient.GetConnectionStatus())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type kernelDialTarget struct {
	network string
	address string
}

type kernelTargetDialer struct {
	targets chan kernelDialTarget
}

func (d *kernelTargetDialer) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}

func (d *kernelTargetDialer) DialContext(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error) {
	select {
	case d.targets <- kernelDialTarget{network: network, address: address}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	clientConn, targetConn := net.Pipe()
	// DialContext's context only bounds connection establishment. Once the
	// method returns, the connection remains valid until either peer closes it.
	go func() {
		defer targetConn.Close()
		_ = targetConn.SetDeadline(time.Now().Add(3 * time.Second))

		payload := make([]byte, 4)
		if _, err := io.ReadFull(targetConn, payload); err != nil {
			return
		}
		_, _ = targetConn.Write(bytes.ToUpper(payload))
	}()
	return clientConn, nil
}

var _ proxy.ContextDialer = (*kernelTargetDialer)(nil)

func TestKernelTargetDialerConnectionOutlivesDialContext(t *testing.T) {
	const address = "198.51.100.20:443"

	dialer := &kernelTargetDialer{
		targets: make(chan kernelDialTarget, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		cancel()
		t.Fatalf("dial target: %v", err)
	}
	defer conn.Close()

	cancel()
	assertKernelTarget(t, dialer.targets, "tcp", address)

	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set connection deadline: %v", err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write after dial context cancellation: %v", err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatalf("read after dial context cancellation: %v", err)
	}
	if got, want := string(response), "PING"; got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
}

func assertKernelTarget(
	t *testing.T,
	targets <-chan kernelDialTarget,
	wantNetwork string,
	wantAddress string,
) {
	t.Helper()
	select {
	case target := <-targets:
		if target.network != wantNetwork || target.address != wantAddress {
			t.Fatalf(
				"server target = %s %s, want %s %s",
				target.network,
				target.address,
				wantNetwork,
				wantAddress,
			)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server target dial did not start")
	}
}

func assertKernelResolverNetwork(t *testing.T, queries <-chan string, want string) {
	t.Helper()
	select {
	case got := <-queries:
		if got != want {
			t.Fatalf("server resolver network = %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("server resolver did not receive %s query", want)
	}
}

func assertKernelPolicyRouting(t *testing.T, device string, mark uint32) {
	t.Helper()
	markValue := fmt.Sprintf("%#x", mark)
	checks := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "marked IPv4 uses main",
			args: []string{"route", "get", "192.0.2.200", "mark", markValue},
			want: "dev lo",
		},
		{
			name: "unmarked IPv4 uses TUN",
			args: []string{"route", "get", "192.0.2.200"},
			want: "dev " + device,
		},
		{
			name: "marked IPv6 uses main",
			args: []string{"-6", "route", "get", "2001:db8:ffff::200", "mark", markValue},
			want: "dev lo",
		},
		{
			name: "unmarked IPv6 uses TUN",
			args: []string{"-6", "route", "get", "2001:db8:ffff::200"},
			want: "dev " + device,
		},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			output := runTUNCommandOutput(t, "ip", check.args...)
			if !strings.Contains(output, check.want) {
				t.Fatalf("route output = %q, want containing %q", output, check.want)
			}
		})
	}
}

func kernelDNSQuery(t *testing.T, id uint16) []byte {
	t.Helper()
	query := new(dns.Msg)
	query.SetQuestion("known.test.", dns.TypeA)
	query.Id = id
	wire, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func assertKernelDNSResponse(t *testing.T, wire []byte, id uint16) {
	t.Helper()
	response := new(dns.Msg)
	if err := response.Unpack(wire); err != nil {
		t.Fatal(err)
	}
	if response.Id != id || response.Rcode != dns.RcodeSuccess ||
		len(response.Answer) != 1 {
		t.Fatalf("DNS response = %+v", response)
	}
	answer, ok := response.Answer[0].(*dns.A)
	if !ok || !answer.A.Equal(net.IPv4(203, 0, 113, 53)) {
		t.Fatalf("DNS answer = %v, want 203.0.113.53", response.Answer[0])
	}
}

func runTUNCommand(t *testing.T, name string, args ...string) {
	t.Helper()
	_ = runTUNCommandOutput(t, name, args...)
}

func runTUNCommandOutput(t *testing.T, name string, args ...string) string {
	t.Helper()
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, output)
	}
	return string(output)
}

func assertOutboundSocketMark(t *testing.T, want uint32) {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

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

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := transport.NewOutboundDialer(time.Second).DialContext(
		ctx,
		"tcp4",
		listener.Addr().String(),
	)
	if err != nil {
		t.Fatalf("marked outbound dial: %v", err)
	}
	defer conn.Close()

	syscallConn, ok := conn.(syscall.Conn)
	if !ok {
		t.Fatalf("outbound connection type %T does not expose SyscallConn", conn)
	}
	raw, err := syscallConn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var (
		got        int
		getMarkErr error
	)
	if err := raw.Control(func(fd uintptr) {
		got, getMarkErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK)
	}); err != nil {
		t.Fatalf("access marked socket: %v", err)
	}
	if getMarkErr != nil {
		t.Fatalf("read SO_MARK: %v", getMarkErr)
	}
	if uint32(got) != want {
		t.Fatalf("SO_MARK = %#x, want %#x", got, want)
	}

	select {
	case serverConn := <-accepted:
		_ = serverConn.Close()
	case err := <-acceptErr:
		t.Fatalf("accept marked connection: %v", err)
	case <-ctx.Done():
		t.Fatal("accept marked connection timed out")
	}
}
