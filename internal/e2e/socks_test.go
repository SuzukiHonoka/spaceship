package e2e

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/socks"
)

// SOCKS5 wire constants, spelled out rather than imported so these tests check
// the protocol independently of the implementation's own encoder.
const (
	socks5Ver              = 0x05
	authNone               = 0x00
	cmdConnect             = 0x01
	cmdUDPAssociate        = 0x03
	atypIPv4               = 0x01
	atypDomain             = 0x03
	atypIPv6               = 0x04
	repSuccess             = 0x00
	repRuleFailure         = 0x02
	repCommandNotSupported = 0x07
)

// freeLoopbackAddr reserves an ephemeral loopback port for a later Listen.
//
// Under go test ./... other packages may steal the port between Close and the
// real bind, so we pick from the process-private high range first (much lower
// collision rate than the shared ephemeral pool), fall back to :0, and verify
// the address is free just before returning.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	for i := 0; i < 64; i++ {
		port := 40000 + int(freePortSeq.Add(1)%20000)
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			continue
		}
		if err := ln.Close(); err != nil {
			t.Fatalf("releasing reserved port: %v", err)
		}
		return addr
	}
	// Last resort: OS ephemeral allocation.
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

// freePortSeq is process-local so concurrent freeLoopbackAddr calls in this
// test binary rarely collide with each other.
var freePortSeq atomic.Uint64


func waitForListener(t *testing.T, addr string) {
	t.Helper()
	waitForListenerNetwork(t, "tcp", addr)
}

func waitForListenerNetwork(t *testing.T, network, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout(network, addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nothing started listening on %s/%s", network, addr)
}

// startSocksServer runs a real auth-less SOCKS5 listener over TCP.
func startSocksServer(t *testing.T) string {
	t.Helper()
	return startSocksServerNetwork(t, "tcp", freeLoopbackAddr(t))
}

// startSocksServerNetwork runs the listener on an explicit network/address.
func startSocksServerNetwork(t *testing.T, network, addr string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := socks.New(ctx, &socks.Config{})

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(network, addr) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		select {
		case <-errCh:
		case <-time.After(10 * time.Second):
			t.Error("socks server did not shut down")
		}
	})

	waitForListenerNetwork(t, network, addr)
	return addr
}

// routeAll installs a single catch-all route to the given egress.
func routeAll(t *testing.T, egress router.Egress) {
	t.Helper()
	if err := router.SetRoutes(router.Routes{
		{MatchType: router.TypeDefault, Destination: egress},
	}); err != nil {
		t.Fatalf("SetRoutes(%s) error = %v", egress, err)
	}
}

func startUDPEcho(t *testing.T) *net.UDPAddr {
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
	return pc.LocalAddr().(*net.UDPAddr)
}

func startTCPEcho(t *testing.T) *net.TCPAddr {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp echo listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr)
}

// socks5Connect dials the SOCKS listener over TCP and completes the no-auth
// handshake.
func socks5Connect(t *testing.T, socksAddr string) net.Conn {
	t.Helper()
	return socks5ConnectNetwork(t, "tcp", socksAddr)
}

// socks5ConnectNetwork is socks5Connect over an arbitrary network, so the
// unix-socket listener can be driven too.
func socks5ConnectNetwork(t *testing.T, network, socksAddr string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout(network, socksAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial socks %s/%s: %v", network, socksAddr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}

	if _, err := conn.Write([]byte{socks5Ver, 1, authNone}); err != nil {
		t.Fatalf("socks greeting: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("reading method selection: %v", err)
	}
	if reply[0] != socks5Ver || reply[1] != authNone {
		t.Fatalf("method selection = %v, want [5 0]", reply)
	}
	return conn
}

// socks5Command issues a request and returns the reply code and bound address.
func socks5Command(t *testing.T, conn net.Conn, cmd byte, target *net.TCPAddr) (byte, *net.UDPAddr) {
	t.Helper()

	req := []byte{socks5Ver, cmd, 0x00}
	if target == nil {
		// UDP ASSOCIATE with an unspecified source, which is what clients that do
		// not know their own outbound port send.
		req = append(req, atypIPv4, 0, 0, 0, 0, 0, 0)
	} else {
		ip4 := target.IP.To4()
		if ip4 == nil {
			t.Fatalf("test targets must be IPv4, got %v", target.IP)
		}
		req = append(req, atypIPv4)
		req = append(req, ip4...)
		req = binary.BigEndian.AppendUint16(req, uint16(target.Port))
	}
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("writing socks request: %v", err)
	}

	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatalf("reading socks reply header: %v", err)
	}
	if head[0] != socks5Ver {
		t.Fatalf("reply version = %d, want 5", head[0])
	}

	var ip net.IP
	switch head[3] {
	case atypIPv4:
		b := make([]byte, 4)
		if _, err := io.ReadFull(conn, b); err != nil {
			t.Fatalf("reading bound IPv4: %v", err)
		}
		ip = net.IP(b)
	case atypIPv6:
		b := make([]byte, 16)
		if _, err := io.ReadFull(conn, b); err != nil {
			t.Fatalf("reading bound IPv6: %v", err)
		}
		ip = net.IP(b)
	case atypDomain:
		lb := make([]byte, 1)
		if _, err := io.ReadFull(conn, lb); err != nil {
			t.Fatalf("reading bound domain length: %v", err)
		}
		b := make([]byte, lb[0])
		if _, err := io.ReadFull(conn, b); err != nil {
			t.Fatalf("reading bound domain: %v", err)
		}
	default:
		t.Fatalf("unknown bound address type %d", head[3])
	}

	pb := make([]byte, 2)
	if _, err := io.ReadFull(conn, pb); err != nil {
		t.Fatalf("reading bound port: %v", err)
	}
	return head[1], &net.UDPAddr{IP: ip, Port: int(binary.BigEndian.Uint16(pb))}
}

// encodeUDPRequest builds a SOCKS5 UDP request datagram (RFC 1928 §7).
func encodeUDPRequest(t *testing.T, target *net.UDPAddr, payload []byte) []byte {
	t.Helper()
	ip4 := target.IP.To4()
	if ip4 == nil {
		t.Fatalf("test targets must be IPv4, got %v", target.IP)
	}
	out := []byte{0, 0, 0, atypIPv4}
	out = append(out, ip4...)
	out = binary.BigEndian.AppendUint16(out, uint16(target.Port))
	return append(out, payload...)
}

// decodeUDPReply parses a SOCKS5 UDP reply datagram.
func decodeUDPReply(t *testing.T, b []byte) (*net.UDPAddr, []byte) {
	t.Helper()
	if len(b) < 4 {
		t.Fatalf("reply datagram too short: %d bytes", len(b))
	}
	if b[0] != 0 || b[1] != 0 {
		t.Fatalf("reply RSV = %v, want zeros", b[:2])
	}
	if b[2] != 0 {
		t.Fatalf("reply FRAG = %d, want 0", b[2])
	}

	off := 4
	var ip net.IP
	switch b[3] {
	case atypIPv4:
		ip, off = net.IP(b[off:off+4]), off+4
	case atypIPv6:
		ip, off = net.IP(b[off:off+16]), off+16
	default:
		t.Fatalf("unexpected reply address type %d", b[3])
	}
	port := binary.BigEndian.Uint16(b[off : off+2])
	return &net.UDPAddr{IP: ip, Port: int(port)}, b[off+2:]
}

// clientUDPSocket opens a loopback UDP socket for talking to the relay. It must
// be on 127.0.0.1 because the relay only accepts datagrams from the IP that
// opened the TCP control connection.
func clientUDPSocket(t *testing.T) net.PacketConn {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client udp listen: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc
}

// TestFullStack_SOCKS5UDPAssociate drives the UDP relay the way a real client
// does: TCP control connection, ASSOCIATE, then datagrams over the wire.
//
// Every piece of the rewritten relay is on this path — handleAssociate's bind
// address, the reader/worker pool, NAT creation, the connected outbound socket,
// reverse-path source filtering, and header framing.
func TestFullStack_SOCKS5UDPAssociate(t *testing.T) {
	routeAll(t, router.EgressDirect)
	echo := startUDPEcho(t)
	socksAddr := startSocksServer(t)

	ctrl := socks5Connect(t, socksAddr)
	rep, relayAddr := socks5Command(t, ctrl, cmdUDPAssociate, nil)
	if rep != repSuccess {
		t.Fatalf("UDP ASSOCIATE reply = %d, want success", rep)
	}
	if relayAddr.Port == 0 {
		t.Fatal("relay advertised port 0")
	}
	if relayAddr.IP == nil || relayAddr.IP.IsUnspecified() {
		t.Fatalf("relay advertised an unusable address %v; the client cannot reach it", relayAddr.IP)
	}

	client := clientUDPSocket(t)
	payload := []byte("socks5 udp round trip")
	if _, err := client.WriteTo(encodeUDPRequest(t, echo, payload), relayAddr); err != nil {
		t.Fatalf("sending datagram to relay: %v", err)
	}

	if err := client.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 65535)
	n, _, err := client.ReadFrom(buf)
	if err != nil {
		t.Fatalf("no reply from relay: %v", err)
	}

	src, got := decodeUDPReply(t, buf[:n])
	if !bytes.Equal(got, payload) {
		t.Errorf("relayed payload = %q, want %q", got, payload)
	}
	if !src.IP.Equal(echo.IP) || src.Port != echo.Port {
		t.Errorf("reply source = %v, want the echo target %v", src, echo)
	}
}

// TestFullStack_SOCKS5UDPTwoSourcePorts is the over-the-wire proof of the NAT
// keying fix. Two client sockets on one association talk to the same target;
// each must get its own reply. Keyed on the target alone, both replies would go
// to whichever socket opened the flow first, and the second read would time out.
func TestFullStack_SOCKS5UDPTwoSourcePorts(t *testing.T) {
	routeAll(t, router.EgressDirect)
	echo := startUDPEcho(t)
	socksAddr := startSocksServer(t)

	ctrl := socks5Connect(t, socksAddr)
	rep, relayAddr := socks5Command(t, ctrl, cmdUDPAssociate, nil)
	if rep != repSuccess {
		t.Fatalf("UDP ASSOCIATE reply = %d, want success", rep)
	}

	for i, payload := range [][]byte{[]byte("from-socket-one"), []byte("from-socket-two")} {
		sock := clientUDPSocket(t)
		if _, err := sock.WriteTo(encodeUDPRequest(t, echo, payload), relayAddr); err != nil {
			t.Fatalf("socket %d: sending datagram: %v", i, err)
		}
		if err := sock.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
			t.Fatalf("socket %d: SetReadDeadline: %v", i, err)
		}
		buf := make([]byte, 65535)
		n, _, err := sock.ReadFrom(buf)
		if err != nil {
			t.Fatalf("socket %d: no reply (its flow was likely merged with the other "+
				"source port): %v", i, err)
		}
		if _, got := decodeUDPReply(t, buf[:n]); !bytes.Equal(got, payload) {
			t.Errorf("socket %d: payload = %q, want %q", i, got, payload)
		}
	}
}

// TestFullStack_SOCKS5UDPRefusedWithoutCapableEgress verifies a client gets a
// clean "command not supported" — and so falls back to TCP — when no installed
// route could carry a datagram. Before this, ASSOCIATE succeeded and the client
// waited forever for replies that were dropped at dial time.
func TestFullStack_SOCKS5UDPRefusedWithoutCapableEgress(t *testing.T) {
	routeAll(t, router.EgressBlackHole)
	socksAddr := startSocksServer(t)

	ctrl := socks5Connect(t, socksAddr)
	rep, _ := socks5Command(t, ctrl, cmdUDPAssociate, nil)
	if rep != repCommandNotSupported {
		t.Errorf("UDP ASSOCIATE reply = %d, want %d (command not supported)",
			rep, repCommandNotSupported)
	}
}

// TestFullStack_SOCKS5UDPRefusedWhenDisabled verifies the config kill switch is
// visible on the wire, not just in the relay constructor.
func TestFullStack_SOCKS5UDPRefusedWhenDisabled(t *testing.T) {
	routeAll(t, router.EgressDirect)
	socks.SetUDPSettings(socks.UDPSettings{Disable: true})
	t.Cleanup(func() { socks.SetUDPSettings(socks.UDPSettings{}) })

	socksAddr := startSocksServer(t)
	ctrl := socks5Connect(t, socksAddr)
	rep, _ := socks5Command(t, ctrl, cmdUDPAssociate, nil)
	if rep != repCommandNotSupported {
		t.Errorf("UDP ASSOCIATE reply = %d, want %d (command not supported)",
			rep, repCommandNotSupported)
	}
}

// TestFullStack_SOCKS5Connect covers the TCP CONNECT path over the wire.
func TestFullStack_SOCKS5Connect(t *testing.T) {
	routeAll(t, router.EgressDirect)
	echo := startTCPEcho(t)
	socksAddr := startSocksServer(t)

	conn := socks5Connect(t, socksAddr)
	rep, _ := socks5Command(t, conn, cmdConnect, echo)
	if rep != repSuccess {
		t.Fatalf("CONNECT reply = %d, want success", rep)
	}

	payload := []byte("socks5 connect round trip")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("writing through the tunnel: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("echoed payload = %q, want %q", got, payload)
	}
}

// TestFullStack_SOCKS5ConnectBlockedRoute verifies a blocked destination is
// refused with a rule failure rather than silently hanging.
func TestFullStack_SOCKS5ConnectBlockedRoute(t *testing.T) {
	routeAll(t, router.EgressBlock)
	echo := startTCPEcho(t)
	socksAddr := startSocksServer(t)

	conn := socks5Connect(t, socksAddr)
	rep, _ := socks5Command(t, conn, cmdConnect, echo)
	if rep != repRuleFailure {
		t.Errorf("CONNECT reply for a blocked route = %d, want %d (rule failure)",
			rep, repRuleFailure)
	}
}

// TestFullStack_SOCKS5ConnectBlackholeDiscards verifies the blackhole egress
// accepts the connection and then swallows traffic, rather than erroring.
func TestFullStack_SOCKS5ConnectBlackholeDiscards(t *testing.T) {
	routeAll(t, router.EgressBlackHole)
	echo := startTCPEcho(t)
	socksAddr := startSocksServer(t)

	conn := socks5Connect(t, socksAddr)
	rep, _ := socks5Command(t, conn, cmdConnect, echo)
	if rep != repSuccess {
		t.Fatalf("CONNECT reply = %d, want success (blackhole accepts then discards)", rep)
	}

	if _, err := conn.Write([]byte("swallowed")); err != nil {
		t.Fatalf("writing to a blackholed connection: %v", err)
	}
	// Nothing may come back.
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 32)
	if n, err := conn.Read(buf); err == nil && n > 0 {
		t.Errorf("blackhole returned %d bytes (%q); it must discard", n, buf[:n])
	}
}

// TestFullStack_SOCKS5UDPOverUnixListener covers UDP ASSOCIATE on a unix-socket
// SOCKS listener.
//
// A unix control connection exposes no local IP, so the relay has nothing to
// derive a bind address from. It previously fell back to a wildcard bind and
// advertised an unspecified BND.ADDR ("::"), which a client cannot send
// datagrams to — the association looked healthy and silently swallowed
// everything. The relay must bind loopback instead, since a client that reached
// us over a unix socket is necessarily on this host.
func TestFullStack_SOCKS5UDPOverUnixListener(t *testing.T) {
	routeAll(t, router.EgressDirect)
	echo := startUDPEcho(t)

	// Not t.TempDir(): it embeds the test name, and the resulting path blows past
	// the ~104 byte limit the platform imposes on unix socket paths.
	dir, err := os.MkdirTemp("", "sp")
	if err != nil {
		t.Fatalf("creating socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sockPath := filepath.Join(dir, "s.sock")
	if len(sockPath) > 100 {
		t.Skipf("socket path %q is too long for this platform", sockPath)
	}
	startSocksServerNetwork(t, "unix", sockPath)

	ctrl := socks5ConnectNetwork(t, "unix", sockPath)
	rep, relayAddr := socks5Command(t, ctrl, cmdUDPAssociate, nil)
	if rep != repSuccess {
		t.Fatalf("UDP ASSOCIATE over a unix listener: reply = %d, want success", rep)
	}
	if relayAddr.IP == nil || relayAddr.IP.IsUnspecified() {
		t.Fatalf("relay advertised %v, which the client cannot send datagrams to",
			relayAddr.IP)
	}

	sock := clientUDPSocket(t)
	payload := []byte("datagram over a unix-socket association")
	if _, err := sock.WriteTo(encodeUDPRequest(t, echo, payload), relayAddr); err != nil {
		t.Fatalf("sending datagram to the advertised relay address: %v", err)
	}

	if err := sock.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 65535)
	n, _, err := sock.ReadFrom(buf)
	if err != nil {
		t.Fatalf("no reply from the relay: %v", err)
	}
	if _, got := decodeUDPReply(t, buf[:n]); !bytes.Equal(got, payload) {
		t.Errorf("relayed payload = %q, want %q", got, payload)
	}
}
