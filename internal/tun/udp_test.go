package tun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/miekg/dns"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

func TestUDPAdmissionIsOneSessionPerTuple(t *testing.T) {
	service := &Service{
		ctx:             context.Background(),
		flowSlots:       make(chan struct{}, 4),
		udpSessionSlots: make(chan struct{}, 4),
	}
	first, ok := service.admitUDPSession("a|1|b|2")
	if !ok || first == nil {
		t.Fatal("first admission rejected a new 4-tuple")
	}
	if duplicate, ok := service.admitUDPSession("a|1|b|2"); !ok || duplicate != nil {
		t.Fatalf("duplicate admission = (%v, %t), want the live session kept", duplicate, ok)
	}
	if other, ok := service.admitUDPSession("a|1|b|3"); !ok || other == nil {
		t.Fatal("admission rejected a different 4-tuple")
	}
	service.finishUDPSession(first)
	if again, ok := service.admitUDPSession("a|1|b|2"); !ok || again == nil || again == first {
		t.Fatal("admission did not open a new session after the 4-tuple was released")
	}
	if got := len(service.flowSlots); got != 2 {
		t.Fatalf("flow slots = %d, want 2 live sessions", got)
	}
	if got := len(service.udpSessionSlots); got != 2 {
		t.Fatalf("UDP session slots = %d, want 2 live sessions", got)
	}
}

func newTestUDPSession(t *testing.T) *udpSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	session := &udpSession{key: "test", ctx: ctx, cancel: cancel}
	session.touch()
	return session
}

func pipeTUNSide(conn net.Conn) udpTUNSide {
	return udpTUNSide{conn: conn, read: func(deadline time.Time) ([]byte, func(), error) {
		buf := make([]byte, udpDatagramSize)
		_ = conn.SetReadDeadline(deadline)
		n, err := conn.Read(buf)
		return buf[:n], nil, err
	}}
}

func TestProxyUDPRejectsRouteThatCannotCarryUDP(t *testing.T) {
	route := &proxyResultRoute{}
	service := &Service{
		ctx: context.Background(),
		cfg: Config{UDPIdleTimeout: time.Second},
		resolveRoute: func(string) (transport.Transport, error) {
			return route, nil
		},
	}
	conn, peer := net.Pipe()
	defer func() { _ = conn.Close() }()
	defer func() { _ = peer.Close() }()

	id := stack.TransportEndpointID{
		LocalAddress: tcpip.AddrFrom4([4]byte{192, 0, 2, 20}),
		LocalPort:    443,
	}
	err := service.proxyUDP(newTestUDPSession(t), pipeTUNSide(conn), id)
	if err == nil || !strings.Contains(err.Error(), "does not support UDP") {
		t.Fatalf("proxyUDP() error = %v, want unsupported UDP", err)
	}
	if got := route.closeCount.Load(); got != 1 {
		t.Fatalf("route Close() calls = %d, want 1", got)
	}
}

func TestProxyUDPReportsRouteLookupFailure(t *testing.T) {
	wantErr := errors.New("route unavailable")
	service := &Service{
		ctx: context.Background(),
		cfg: Config{UDPIdleTimeout: time.Second},
		resolveRoute: func(string) (transport.Transport, error) {
			return nil, wantErr
		},
	}
	conn, peer := net.Pipe()
	defer func() { _ = conn.Close() }()
	defer func() { _ = peer.Close() }()

	id := stack.TransportEndpointID{
		LocalAddress: tcpip.AddrFrom4([4]byte{192, 0, 2, 20}),
		LocalPort:    443,
	}
	if err := service.proxyUDP(newTestUDPSession(t), pipeTUNSide(conn), id); !errors.Is(err, wantErr) {
		t.Fatalf("proxyUDP() error = %v, want wrapping %v", err, wantErr)
	}
}

func TestUDPForwarderCreatesOneSessionPerTuple(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{MaxConnections: 4})
	route := newUDPEchoRoute()
	service.setResolveRoute(func(string) (transport.Transport, error) {
		return route, nil
	})

	conn := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{198, 51, 100, 20}), 443, ipv4.ProtocolNumber)
	if err := exchangeUDP(conn, "one!"); err != nil {
		t.Fatal(err)
	}
	if err := exchangeUDP(conn, "two!"); err != nil {
		t.Fatal(err)
	}
	if got := route.dials.Load(); got != 1 {
		t.Fatalf("dials = %d, want 1 session for one 4-tuple", got)
	}
	select {
	case got := <-route.dialsSeen:
		if got.network != "udp4" || got.addr != "198.51.100.20:443" {
			t.Fatalf("dial = %+v", got)
		}
	default:
		t.Fatal("route did not record a dial")
	}

	other := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{198, 51, 100, 21}), 443, ipv4.ProtocolNumber)
	if err := exchangeUDP(other, "ping"); err != nil {
		t.Fatal(err)
	}
	if got := route.dials.Load(); got != 2 {
		t.Fatalf("dials = %d, want 2 sessions for two 4-tuples", got)
	}
}

func TestUDPForwarderRoutesIPv6(t *testing.T) {
	service, peer, peerNIC := newLinkedIPv6Service(t, Config{MaxConnections: 2})
	route := newUDPEchoRoute()
	service.setResolveRoute(func(host string) (transport.Transport, error) {
		if host != "2001:db8::20" {
			return nil, errors.New("unexpected host " + host)
		}
		return route, nil
	})

	addr := tcpip.AddrFrom16([16]byte{
		0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0x20,
	})
	conn := dialPeerUDP(t, peer, peerNIC, addr, 443, ipv6.ProtocolNumber)
	if err := exchangeUDP(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-route.dialsSeen:
		if got.network != "udp6" || got.addr != "[2001:db8::20]:443" {
			t.Fatalf("dial = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("IPv6 UDP was not dialed")
	}
}

func TestUDPSessionExpiresWhenIdle(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{
		MaxConnections: 2,
		UDPIdleTimeout: 300 * time.Millisecond,
	})
	route := newUDPEchoRoute()
	service.setResolveRoute(func(string) (transport.Transport, error) {
		return route, nil
	})

	conn := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{198, 51, 100, 30}), 9, ipv4.ProtocolNumber)
	if err := exchangeUDP(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	if len(service.flowSlots) != 1 {
		t.Fatalf("flow slots = %d, want the session held open after the echo", len(service.flowSlots))
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(service.flowSlots) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(service.flowSlots) != 0 {
		t.Fatal("idle UDP session was not closed")
	}
}

func TestUDPSessionClosesOnShutdown(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{
		MaxConnections: 2,
		UDPIdleTimeout: time.Minute,
	})
	route := newHoldPacketRoute()
	service.setResolveRoute(func(string) (transport.Transport, error) {
		return route, nil
	})

	conn := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{198, 51, 100, 31}), 9, ipv4.ProtocolNumber)
	writeDone := make(chan error, 1)
	go func() {
		_, err := conn.Write([]byte("ping"))
		writeDone <- err
	}()
	select {
	case <-route.started:
	case <-time.After(2 * time.Second):
		t.Fatal("UDP dial did not start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- service.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close() did not finish the active UDP session")
	}
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("UDP write did not finish")
	}
}

// UDP may only fill its own budget. Extra 4-tuples replace older UDP sessions,
// so the rest of MaxConnections stays free for TCP.
func TestUDPSessionsLeaveTCPShareFree(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{
		MaxConnections: 4,
		MaxUDPSessions: 2,
		UDPIdleTimeout: time.Minute,
	})
	udpRoute := newUDPEchoRoute()
	tcpRoute := &holdTCPRoute{}
	service.setResolveRoute(func(host string) (transport.Transport, error) {
		if host == "198.51.100.20" {
			return tcpRoute, nil
		}
		return udpRoute, nil
	})

	for port := uint16(1); port <= 4; port++ {
		conn := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{198, 51, 100, 70}), port, ipv4.ProtocolNumber)
		if err := exchangeUDP(conn, "ping"); err != nil {
			t.Fatalf("UDP session %d: %v", port, err)
		}
	}
	if got := udpRoute.dials.Load(); got != 4 {
		t.Fatalf("UDP dials = %d, want every 4-tuple admitted", got)
	}
	if got := liveUDPSessions(service); got != 2 {
		t.Fatalf("live UDP sessions = %d, want MaxUDPSessions", got)
	}
	if got := len(service.flowSlots); got != 2 {
		t.Fatalf("flow slots after UDP = %d, want only the UDP budget used", got)
	}

	for i := range 2 {
		conn := dialPeerTCP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{198, 51, 100, 20}), 443)
		if err := writeAll(conn, []byte("ping")); err != nil {
			t.Fatalf("TCP %d write: %v", i, err)
		}
		response := make([]byte, 4)
		if _, err := io.ReadFull(conn, response); err != nil {
			t.Fatalf("TCP %d was refused while UDP was at its budget: %v", i, err)
		}
	}
	if got := len(service.flowSlots); got != 4 {
		t.Fatalf("flow slots = %d, want 2 UDP + 2 TCP", got)
	}
}

func TestUDPReplacesLeastRecentlyActiveSession(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{
		MaxConnections: 4,
		MaxUDPSessions: 2,
		UDPIdleTimeout: time.Minute,
	})
	route := newUDPEchoRoute()
	service.setResolveRoute(func(string) (transport.Transport, error) {
		return route, nil
	})
	target := tcpip.AddrFrom4([4]byte{198, 51, 100, 71})

	first := dialPeerUDP(t, peer, peerNIC, target, 1, ipv4.ProtocolNumber)
	if err := exchangeUDP(first, "one!"); err != nil {
		t.Fatal(err)
	}
	second := dialPeerUDP(t, peer, peerNIC, target, 2, ipv4.ProtocolNumber)
	if err := exchangeUDP(second, "two!"); err != nil {
		t.Fatal(err)
	}
	// Make the first session the most recently active one.
	time.Sleep(5 * time.Millisecond)
	if err := exchangeUDP(first, "one!"); err != nil {
		t.Fatal(err)
	}

	third := dialPeerUDP(t, peer, peerNIC, target, 3, ipv4.ProtocolNumber)
	if err := exchangeUDP(third, "tri!"); err != nil {
		t.Fatal(err)
	}
	if !hasUDPSessionTo(service, 1) || hasUDPSessionTo(service, 2) || !hasUDPSessionTo(service, 3) {
		t.Fatal("replacement did not evict the least recently active session")
	}
	if err := exchangeUDP(first, "one!"); err != nil {
		t.Fatalf("surviving session stopped working: %v", err)
	}
	if got := route.dials.Load(); got != 3 {
		t.Fatalf("dials = %d, want the surviving session reused", got)
	}
}

// Replacement hands slots from the evicted session to the new one. Every slot
// and WaitGroup entry must still be returned exactly once.
func TestUDPReplacementReleasesSlotsExactlyOnce(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{
		MaxConnections: 4,
		MaxUDPSessions: 2,
		UDPIdleTimeout: 300 * time.Millisecond,
	})
	route := newUDPEchoRoute()
	service.setResolveRoute(func(string) (transport.Transport, error) {
		return route, nil
	})
	for port := uint16(1); port <= 8; port++ {
		conn := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{198, 51, 100, 72}), port, ipv4.ProtocolNumber)
		if err := exchangeUDP(conn, "ping"); err != nil {
			t.Fatalf("UDP session %d: %v", port, err)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for (len(service.flowSlots) != 0 || len(service.udpSessionSlots) != 0 || liveUDPSessions(service) != 0) &&
		time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if flows, udp, live := len(service.flowSlots), len(service.udpSessionSlots), liveUDPSessions(service); flows != 0 || udp != 0 || live != 0 {
		t.Fatalf("after idle expiry flow slots=%d UDP slots=%d sessions=%d, want all released", flows, udp, live)
	}

	closed := make(chan error, 1)
	go func() { closed <- service.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close() waited on a flow that replacement never finished")
	}
}

// With no UDP session to replace, a full MaxConnections budget still refuses
// a new UDP flow rather than taking a TCP slot.
func TestUDPRefusedWhenTCPHoldsEverySlot(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{
		MaxConnections: 1,
		MaxUDPSessions: 1,
		UDPIdleTimeout: time.Minute,
	})
	udpRoute := newUDPEchoRoute()
	tcpRoute := &holdTCPRoute{}
	service.setResolveRoute(func(host string) (transport.Transport, error) {
		if host == "198.51.100.20" {
			return tcpRoute, nil
		}
		return udpRoute, nil
	})

	conn := dialPeerTCP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{198, 51, 100, 20}), 443)
	if err := writeAll(conn, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}

	udpConn := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{198, 51, 100, 73}), 9, ipv4.ProtocolNumber)
	if _, err := udpConn.Write([]byte("extra")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := udpRoute.dials.Load(); got != 0 {
		t.Fatalf("UDP dials = %d, want the flow refused while TCP holds every slot", got)
	}
}

// An idle session must not keep a datagram buffer. Before buffers were
// borrowed per datagram, each session held two 64 KiB buffers.
func TestIdleUDPSessionHoldsNoDatagramBuffer(t *testing.T) {
	if testing.Short() {
		t.Skip("heap measurement")
	}
	const sessions = 400
	service, peer, peerNIC := newLinkedService(t, Config{
		MaxConnections: sessions + 1,
		MaxUDPSessions: sessions + 1,
		UDPIdleTimeout: time.Minute,
	})
	route := newUDPEchoRoute()
	route.packetReader = true
	service.setResolveRoute(func(string) (transport.Transport, error) {
		return route, nil
	})
	target := tcpip.AddrFrom4([4]byte{198, 51, 100, 74})

	// Warm the pool and one-time stack state outside the measurement.
	warm := dialPeerUDP(t, peer, peerNIC, target, 1, ipv4.ProtocolNumber)
	if err := exchangeUDP(warm, "warm"); err != nil {
		t.Fatal(err)
	}
	before := settledHeapInUse()
	for i := range sessions {
		conn := dialPeerUDP(t, peer, peerNIC, target, uint16(1000+i), ipv4.ProtocolNumber)
		if err := exchangeUDP(conn, "ping"); err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
	}
	after := settledHeapInUse()

	perSession := float64(after-before) / sessions
	t.Logf("heap per idle UDP session: %.1f KiB", perSession/1024)
	if perSession > 48*1024 {
		t.Fatalf("heap per idle UDP session = %.1f KiB, want no retained 64 KiB buffer", perSession/1024)
	}
}

// Buffers are no longer sized to the MTU guess: a reassembled datagram larger
// than the MTU must cross both directions intact.
func TestUDPCarriesDatagramLargerThanMTU(t *testing.T) {
	for _, packetReader := range []bool{true, false} {
		t.Run(fmt.Sprintf("packet_reader=%t", packetReader), func(t *testing.T) {
			service, peer, peerNIC := newLinkedService(t, Config{MaxConnections: 2, MTU: 1400})
			route := newUDPEchoRoute()
			route.packetReader = packetReader
			service.setResolveRoute(func(string) (transport.Transport, error) {
				return route, nil
			})
			conn := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{198, 51, 100, 75}), 9, ipv4.ProtocolNumber)
			payload := strings.Repeat("0123456789abcdef", 500)
			if err := exchangeUDP(conn, payload); err != nil {
				t.Fatalf("%d-byte datagram: %v", len(payload), err)
			}
		})
	}
}

func settledHeapInUse() uint64 {
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapInuse
}

func liveUDPSessions(service *Service) int {
	service.udpMu.Lock()
	defer service.udpMu.Unlock()
	return len(service.udpFlows)
}

func hasUDPSessionTo(service *Service, port uint16) bool {
	suffix := fmt.Sprintf("|%d", port)
	service.udpMu.Lock()
	defer service.udpMu.Unlock()
	for key := range service.udpFlows {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

func dialPeerTCP(t *testing.T, peer *stack.Stack, peerNIC tcpip.NICID, addr tcpip.Address, port uint16) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := gonet.DialContextTCP(ctx, peer, tcpip.FullAddress{NIC: peerNIC, Addr: addr, Port: port}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("DialContextTCP() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	return conn
}

func TestUDPRouteFailureReleasesConnectionSlot(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{
		MaxConnections: 1,
		UDPIdleTimeout: time.Minute,
	})
	route := &proxyResultRoute{}
	service.setResolveRoute(func(string) (transport.Transport, error) {
		return route, nil
	})

	conn := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{198, 51, 100, 50}), 9, ipv4.ProtocolNumber)
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for route.closeCount.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if route.closeCount.Load() == 0 {
		t.Fatal("unsupported UDP route was not closed")
	}
	for len(service.flowSlots) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(service.flowSlots) != 0 {
		t.Fatal("failed UDP flow did not release its connection slot")
	}

	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 8)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("unsupported UDP route returned a payload")
	}
}

func TestDNSHijackOwnsPort53WhileOtherUDPIsProxied(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{
		MaxConnections: 4,
		DNS: DNSConfig{
			Enabled:        true,
			QueryTimeout:   time.Second,
			TCPIdleTimeout: time.Second,
			MaxInFlight:    4,
		},
	})
	route := newUDPEchoRoute()
	var routeCalls atomic.Int32
	service.setResolveRoute(func(host string) (transport.Transport, error) {
		routeCalls.Add(1)
		if host == "192.0.2.53" {
			return nil, errors.New("port 53 must not use the general route")
		}
		return route, nil
	})
	service.setExchanger(&testDNSExchanger{exchange: func(
		_ context.Context,
		wire []byte,
		network proto.Network,
		_ bool,
	) ([]byte, error) {
		if network != proto.Network_UDP {
			return nil, errors.New("expected a UDP DNS query")
		}
		return dnsSuccess(t, wire, "203.0.113.9")
	}})

	query := dnsQueryWire(t, "known.test.", 7)
	dnsConn := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{192, 0, 2, 53}), 53, ipv4.ProtocolNumber)
	if _, err := dnsConn.Write(query); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, dns.MaxMsgSize)
	_ = dnsConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := dnsConn.Read(response)
	if err != nil {
		t.Fatalf("UDP DNS read: %v", err)
	}
	assertDNSAnswer(t, response[:n], 7, "203.0.113.9")
	if got := routeCalls.Load(); got != 0 {
		t.Fatalf("general route calls after DNS = %d, want 0", got)
	}

	other := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{198, 51, 100, 60}), 443, ipv4.ProtocolNumber)
	if err := exchangeUDP(other, "ping"); err != nil {
		t.Fatal(err)
	}
	if got := route.dials.Load(); got != 1 {
		t.Fatalf("non-DNS dials = %d, want 1", got)
	}
}

func TestUDPPort53IsProxiedWhenHijackDisabled(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{MaxConnections: 2})
	route := newUDPEchoRoute()
	service.setResolveRoute(func(string) (transport.Transport, error) {
		return route, nil
	})
	service.setExchanger(&testDNSExchanger{exchange: func(
		context.Context, []byte, proto.Network, bool,
	) ([]byte, error) {
		return nil, errors.New("dns hijack must not own port 53 when it is disabled")
	}})

	conn := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{192, 0, 2, 53}), 53, ipv4.ProtocolNumber)
	if err := exchangeUDP(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-route.dialsSeen:
		if got.addr != "192.0.2.53:53" {
			t.Fatalf("dial addr = %s, want 192.0.2.53:53", got.addr)
		}
	default:
		t.Fatal("port 53 was not proxied as UDP")
	}
}

func dialPeerUDP(
	t *testing.T,
	peer *stack.Stack,
	peerNIC tcpip.NICID,
	addr tcpip.Address,
	port uint16,
	network tcpip.NetworkProtocolNumber,
) net.Conn {
	t.Helper()
	conn, err := gonet.DialUDP(peer, nil, &tcpip.FullAddress{
		NIC:  peerNIC,
		Addr: addr,
		Port: port,
	}, network)
	if err != nil {
		t.Fatalf("DialUDP() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	return conn
}

func exchangeUDP(conn net.Conn, payload string) error {
	if _, err := conn.Write([]byte(payload)); err != nil {
		return err
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if string(buf) != payload {
		return errors.New("echo = " + string(buf))
	}
	return nil
}

type recordedUDPDial struct {
	network string
	addr    string
}

type udpEchoRoute struct {
	dials     atomic.Int32
	dialsSeen chan recordedUDPDial
	// packetReader makes dials return a transport.PacketReader, as the RPC
	// and direct transports do.
	packetReader bool
}

func newUDPEchoRoute() *udpEchoRoute {
	return &udpEchoRoute{dialsSeen: make(chan recordedUDPDial, 4)}
}

func (r *udpEchoRoute) String() string { return "test-udp-echo" }
func (r *udpEchoRoute) Dial(string, string) (net.Conn, error) {
	return nil, errors.New("not used")
}
func (r *udpEchoRoute) Close() error { return nil }
func (r *udpEchoRoute) Proxy(context.Context, string, chan<- string, io.Writer, io.Reader) error {
	return errors.New("not used")
}
func (r *udpEchoRoute) DialPacket(network, addr string) (net.PacketConn, error) {
	return r.DialPacketContext(context.Background(), network, addr)
}
func (r *udpEchoRoute) DialPacketContext(ctx context.Context, network, addr string) (net.PacketConn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.dials.Add(1)
	select {
	case r.dialsSeen <- recordedUDPDial{network: network, addr: addr}:
	default:
	}
	if r.packetReader {
		return &readerEchoPacketConn{newEchoPacketConn()}, nil
	}
	return newEchoPacketConn(), nil
}

var _ transport.ContextPacketDialer = (*udpEchoRoute)(nil)

// readerEchoPacketConn adds transport.PacketReader to echoPacketConn.
type readerEchoPacketConn struct {
	*echoPacketConn
}

func (c *readerEchoPacketConn) ReadPacket() ([]byte, net.Addr, error) {
	pkt, err := c.nextPacket()
	if err != nil {
		return nil, nil, err
	}
	return pkt, c.RemoteAddr(), nil
}

var _ transport.PacketReader = (*readerEchoPacketConn)(nil)

type holdPacketRoute struct {
	dials   atomic.Int32
	started chan struct{}
	once    sync.Once
}

func newHoldPacketRoute() *holdPacketRoute {
	return &holdPacketRoute{started: make(chan struct{})}
}

func (r *holdPacketRoute) String() string { return "test-udp-hold" }
func (r *holdPacketRoute) Dial(string, string) (net.Conn, error) {
	return nil, errors.New("not used")
}
func (r *holdPacketRoute) Close() error { return nil }
func (r *holdPacketRoute) Proxy(context.Context, string, chan<- string, io.Writer, io.Reader) error {
	return errors.New("not used")
}
func (r *holdPacketRoute) DialPacket(network, addr string) (net.PacketConn, error) {
	return r.DialPacketContext(context.Background(), network, addr)
}
func (r *holdPacketRoute) DialPacketContext(ctx context.Context, _, _ string) (net.PacketConn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.dials.Add(1)
	r.once.Do(func() { close(r.started) })
	return newHoldPacketConn(), nil
}

var _ transport.ContextPacketDialer = (*holdPacketRoute)(nil)

// holdTCPRoute echoes the first 4 bytes and then keeps the flow open until
// the service cancels it, so the flow keeps its MaxConnections slot.
type holdTCPRoute struct{}

func (*holdTCPRoute) String() string                        { return "test-tcp-hold" }
func (*holdTCPRoute) Dial(string, string) (net.Conn, error) { return nil, errors.New("not used") }
func (*holdTCPRoute) Close() error                          { return nil }
func (*holdTCPRoute) Proxy(
	ctx context.Context,
	_ string,
	localAddr chan<- string,
	dst io.Writer,
	src io.Reader,
) error {
	defer close(localAddr)
	localAddr <- "127.0.0.1:1"
	payload := make([]byte, 4)
	if _, err := io.ReadFull(src, payload); err != nil {
		return err
	}
	if _, err := dst.Write([]byte(strings.ToUpper(string(payload)))); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

var _ transport.Transport = (*holdTCPRoute)(nil)

type echoPacketConn struct {
	mu     sync.Mutex
	queue  [][]byte
	closed bool
	wake   chan struct{}
	readDL atomic.Int64
}

func newEchoPacketConn() *echoPacketConn {
	return &echoPacketConn{wake: make(chan struct{}, 1)}
}

func (c *echoPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	pkt, err := c.nextPacket()
	if err != nil {
		return 0, nil, err
	}
	return copy(p, pkt), c.RemoteAddr(), nil
}

// nextPacket waits without a caller buffer, so readerEchoPacketConn can
// behave like a real transport.PacketReader.
func (c *echoPacketConn) nextPacket() ([]byte, error) {
	for {
		c.mu.Lock()
		if len(c.queue) > 0 {
			pkt := c.queue[0]
			c.queue = c.queue[1:]
			c.mu.Unlock()
			return pkt, nil
		}
		if c.closed {
			c.mu.Unlock()
			return nil, net.ErrClosed
		}
		deadline := c.readDL.Load()
		c.mu.Unlock()

		var timer <-chan time.Time
		var stop func()
		if deadline > 0 {
			wait := time.Until(time.Unix(0, deadline))
			if wait <= 0 {
				return nil, os.ErrDeadlineExceeded
			}
			tr := time.NewTimer(wait)
			timer = tr.C
			stop = func() {
				if !tr.Stop() {
					select {
					case <-tr.C:
					default:
					}
				}
			}
		}
		select {
		case <-c.wake:
			if stop != nil {
				stop()
			}
		case <-timer:
			return nil, os.ErrDeadlineExceeded
		}
	}
}

func (c *echoPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	c.queue = append(c.queue, append([]byte(nil), p...))
	c.signalLocked()
	return len(p), nil
}

func (c *echoPacketConn) signalLocked() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *echoPacketConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.signalLocked()
	return nil
}

func (c *echoPacketConn) LocalAddr() net.Addr { return &net.UDPAddr{} }
func (c *echoPacketConn) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(198, 51, 100, 20), Port: 9}
}
func (c *echoPacketConn) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}
func (c *echoPacketConn) SetReadDeadline(t time.Time) error {
	if t.IsZero() {
		c.readDL.Store(0)
	} else {
		c.readDL.Store(t.UnixNano())
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return nil
}
func (c *echoPacketConn) SetWriteDeadline(time.Time) error { return nil }

var _ net.PacketConn = (*echoPacketConn)(nil)

type holdPacketConn struct {
	closed chan struct{}
	once   sync.Once
}

func newHoldPacketConn() *holdPacketConn {
	return &holdPacketConn{closed: make(chan struct{})}
}

func (c *holdPacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	<-c.closed
	return 0, nil, net.ErrClosed
}
func (c *holdPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
		return len(p), nil
	}
}
func (c *holdPacketConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}
func (c *holdPacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *holdPacketConn) RemoteAddr() net.Addr             { return &net.UDPAddr{} }
func (c *holdPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *holdPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *holdPacketConn) SetWriteDeadline(time.Time) error { return nil }

var _ net.PacketConn = (*holdPacketConn)(nil)
