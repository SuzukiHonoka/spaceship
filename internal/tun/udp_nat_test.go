package tun

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/sniff"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/direct"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/miekg/dns"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
)

// testNetTranslatingSession wraps a real direct MultiPacketConn, mapping
// TEST-NET-2 addresses (198.51.100.x) to 127.0.0.1 on outbound writes and
// translating replies from 127.0.0.1 back to a configured virtual peer IP so
// gVisor's martian filter does not drop loopback destinations in unit tests.
type testNetTranslatingSession struct {
	transport.MultiPacketConn
	peerPortToVirtualIP map[uint16]netip.Addr
}

func (s *testNetTranslatingSession) WritePacketTo(payload []byte, dst transport.PacketTarget) error {
	if dst.Addr.IsValid() {
		dst.Addr = netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), dst.Addr.Port())
	}
	return s.MultiPacketConn.WritePacketTo(payload, dst)
}

func (s *testNetTranslatingSession) ReadPacketFrom() ([]byte, netip.AddrPort, error) {
	payload, peer, err := s.MultiPacketConn.ReadPacketFrom()
	if err != nil {
		return nil, netip.AddrPort{}, err
	}
	if vIP, ok := s.peerPortToVirtualIP[peer.Port()]; ok {
		peer = netip.AddrPortFrom(vIP, peer.Port())
	}
	return payload, peer, nil
}

type splitEIMRoute struct {
	*direct.Direct
	name                string
	dials               atomic.Int32
	peerPortToVirtualIP map[uint16]netip.Addr
}

func (r *splitEIMRoute) String() string { return r.name }

func (r *splitEIMRoute) DialPacketSession(
	ctx context.Context,
	network string,
	filtering transport.PacketFiltering,
) (transport.MultiPacketConn, error) {
	r.dials.Add(1)
	sess, err := r.Direct.DialPacketSession(ctx, network, filtering)
	if err != nil {
		return nil, err
	}
	return &testNetTranslatingSession{
		MultiPacketConn:     sess,
		peerPortToVirtualIP: r.peerPortToVirtualIP,
	}, nil
}

// TestUDPNAT_FullConeEIFOverTUN proves RFC 4787 Full Cone (EIM + EIF) end to
// end through the gVisor TUN stack: an unconnected UDP socket behind TUN sends
// to Peer A, and an uncontacted Peer B replies to the mapped egress socket; the
// client socket receives Peer B's packet with source address = Peer B.
func TestUDPNAT_FullConeEIFOverTUN(t *testing.T) {
	peerA, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen peerA: %v", err)
	}
	defer func() { _ = peerA.Close() }()

	peerB, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen peerB: %v", err)
	}
	defer func() { _ = peerB.Close() }()

	peerAPort := uint16(peerA.LocalAddr().(*net.UDPAddr).Port)
	peerBPort := uint16(peerB.LocalAddr().(*net.UDPAddr).Port)
	virtualA := netip.MustParseAddr("198.51.100.10")
	virtualB := netip.MustParseAddr("198.51.100.11")

	route := &splitEIMRoute{
		Direct: &direct.Direct{},
		name:   "direct",
		peerPortToVirtualIP: map[uint16]netip.Addr{
			peerAPort: virtualA,
			peerBPort: virtualB,
		},
	}

	service, peer, peerNIC := newLinkedService(t, Config{
		MaxConnections: 8,
		MaxUDPSessions: 4,
		UDPMapping:     UDPMappingEIM,
		UDPFiltering:   UDPFilteringEIF,
		UDPIdleTimeout: 5 * time.Second,
	})
	service.setResolveRoute(func(string) (transport.Transport, error) {
		return route, nil
	})

	// Unconnected client UDP socket inside the peer gVisor stack (simulating an
	// app behind TUN listening on one local port for replies from any peer).
	clientConn, err := gonet.DialUDP(peer, &tcpip.FullAddress{
		NIC:  peerNIC,
		Addr: tcpip.AddrFrom4([4]byte{10, 0, 0, 2}),
		Port: 45000,
	}, nil, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("gonet.DialUDP: %v", err)
	}
	defer func() { _ = clientConn.Close() }()

	if _, err := clientConn.WriteTo([]byte("hello-tun-eim"), &net.UDPAddr{
		IP:   net.IP(virtualA.AsSlice()),
		Port: int(peerAPort),
	}); err != nil {
		t.Fatalf("client WriteTo peerA: %v", err)
	}

	buf := make([]byte, 128)
	_ = peerA.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, mappedAddr, err := peerA.ReadFromUDPAddrPort(buf)
	if err != nil || string(buf[:n]) != "hello-tun-eim" {
		t.Fatalf("peerA read: n=%d payload=%q err=%v", n, string(buf[:n]), err)
	}

	// Uncontacted peerB replies to mappedAddr.
	if _, err := peerB.WriteToUDPAddrPort([]byte("from-uncontacted-b"), mappedAddr); err != nil {
		t.Fatalf("peerB write: %v", err)
	}

	_ = clientConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, from, err := clientConn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("client ReadFrom: %v", err)
	}
	if string(buf[:n]) != "from-uncontacted-b" {
		t.Fatalf("payload = %q, want from-uncontacted-b", buf[:n])
	}
	fromUDP, ok := from.(*net.UDPAddr)
	if !ok {
		t.Fatalf("from addr type = %T, want *net.UDPAddr", from)
	}
	if !fromUDP.IP.Equal(net.IP(virtualB.AsSlice())) || uint16(fromUDP.Port) != peerBPort {
		t.Fatalf("from = %s, want %s:%d", fromUDP, virtualB, peerBPort)
	}
}

// TestUDPNAT_SplitTunnelIsolatesEgressClasses verifies that a single client UDP
// socket sending to two destinations with different routes ("direct" vs "proxy")
// gets two separate EIM sessions rather than leaking traffic across egresses.
func TestUDPNAT_SplitTunnelIsolatesEgressClasses(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{
		MaxConnections: 8,
		MaxUDPSessions: 4,
		UDPMapping:     UDPMappingEIM,
		UDPFiltering:   UDPFilteringEIF,
		UDPIdleTimeout: 5 * time.Second,
	})

	peerDirect, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen peerDirect: %v", err)
	}
	defer func() { _ = peerDirect.Close() }()

	peerProxy, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen peerProxy: %v", err)
	}
	defer func() { _ = peerProxy.Close() }()

	directPort := uint16(peerDirect.LocalAddr().(*net.UDPAddr).Port)
	proxyPort := uint16(peerProxy.LocalAddr().(*net.UDPAddr).Port)
	virtualDirect := netip.MustParseAddr("198.51.100.20")
	virtualProxy := netip.MustParseAddr("198.51.100.21")

	routeDirect := &splitEIMRoute{
		Direct:              &direct.Direct{},
		name:                "direct",
		peerPortToVirtualIP: map[uint16]netip.Addr{directPort: virtualDirect},
	}
	routeProxy := &splitEIMRoute{
		Direct:              &direct.Direct{},
		name:                "rpc",
		peerPortToVirtualIP: map[uint16]netip.Addr{proxyPort: virtualProxy},
	}

	var mu sync.Mutex
	service.setResolveRoute(func(ip string) (transport.Transport, error) {
		mu.Lock()
		defer mu.Unlock()
		if ip == virtualDirect.String() {
			return routeDirect, nil
		}
		return routeProxy, nil
	})

	clientConn, err := gonet.DialUDP(peer, &tcpip.FullAddress{
		NIC:  peerNIC,
		Addr: tcpip.AddrFrom4([4]byte{10, 0, 0, 2}),
		Port: 46000,
	}, nil, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("gonet.DialUDP: %v", err)
	}
	defer func() { _ = clientConn.Close() }()

	// Packet 1 -> "direct" egress
	if _, err := clientConn.WriteTo([]byte("d1"), &net.UDPAddr{
		IP:   net.IP(virtualDirect.AsSlice()),
		Port: int(directPort),
	}); err != nil {
		t.Fatalf("write d1: %v", err)
	}
	buf := make([]byte, 64)
	_ = peerDirect.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := peerDirect.ReadFromUDPAddrPort(buf); err != nil {
		t.Fatalf("peerDirect read: %v", err)
	}

	// Packet 2 -> "rpc" egress from the exact same local port.
	if _, err := clientConn.WriteTo([]byte("p1"), &net.UDPAddr{
		IP:   net.IP(virtualProxy.AsSlice()),
		Port: int(proxyPort),
	}); err != nil {
		t.Fatalf("write p1: %v", err)
	}
	_ = peerProxy.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := peerProxy.ReadFromUDPAddrPort(buf); err != nil {
		t.Fatalf("peerProxy read: %v", err)
	}

	if got := routeDirect.dials.Load(); got != 1 {
		t.Fatalf("direct dials = %d, want 1", got)
	}
	if got := routeProxy.dials.Load(); got != 1 {
		t.Fatalf("proxy dials = %d, want 1", got)
	}

	// ResetNetwork must clear active UDP NAT sessions immediately.
	service.ResetNetwork()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(service.udpSessionSlots) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(service.udpSessionSlots); got != 0 {
		t.Fatalf("udpSessionSlots after ResetNetwork = %d, want 0", got)
	}
}

// TestStatelessUDPDNS_NoPersistentEndpoints verifies that hijacked UDP DNS
// queries do not leave persistent gVisor UDP endpoints or consume udpSessionSlots,
// and cleanly return SERVFAIL when MaxInFlight is exhausted.
func TestStatelessUDPDNS_NoPersistentEndpoints(t *testing.T) {
	gate := make(chan struct{})
	inFlight := make(chan struct{}, 2)

	service, peer, peerNIC := newLinkedService(t, Config{
		MaxConnections: 8,
		DNS: DNSConfig{
			Enabled:     true,
			Addresses:   []netip.Addr{netip.MustParseAddr("198.18.0.2")},
			MaxInFlight: 1,
		},
	})
	service.setExchanger(&testDNSExchanger{
		exchange: func(ctx context.Context, wire []byte, network proto.Network, _ bool) ([]byte, error) {
			inFlight <- struct{}{}
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			var q dns.Msg
			_ = q.Unpack(wire)
			resp := new(dns.Msg)
			resp.SetReply(&q)
			resp.Answer = append(resp.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   net.IPv4(93, 184, 216, 34),
			})
			return resp.Pack()
		},
	})

	makeQuery := func(name string) []byte {
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(name), dns.TypeA)
		raw, err := m.Pack()
		if err != nil {
			t.Fatalf("pack query: %v", err)
		}
		return raw
	}

	// Send first query to the custom virtual DNS IP on a non-53 port (verifies
	// DNS.Addresses matching as well as stateless handling).
	c1 := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{198, 18, 0, 2}), 5353, ipv4.ProtocolNumber)
	if _, err := c1.Write(makeQuery("first.example")); err != nil {
		t.Fatalf("write q1: %v", err)
	}
	<-inFlight

	// Second query arrives while the sole MaxInFlight slot is occupied → must
	// immediately return SERVFAIL.
	c2 := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{8, 8, 8, 8}), 53, ipv4.ProtocolNumber)
	if _, err := c2.Write(makeQuery("second.example")); err != nil {
		t.Fatalf("write q2: %v", err)
	}
	buf := make([]byte, 512)
	_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := c2.Read(buf)
	if err != nil {
		t.Fatalf("read q2 SERVFAIL: %v", err)
	}
	var m2 dns.Msg
	if err := m2.Unpack(buf[:n]); err != nil {
		t.Fatalf("unpack q2: %v", err)
	}
	if m2.Rcode != dns.RcodeServerFailure {
		t.Fatalf("q2 rcode = %d, want SERVFAIL (%d)", m2.Rcode, dns.RcodeServerFailure)
	}

	// Release first query and verify answer arrives cleanly.
	close(gate)
	_ = c1.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = c1.Read(buf)
	if err != nil {
		t.Fatalf("read q1 answer: %v", err)
	}
	var m1 dns.Msg
	if err := m1.Unpack(buf[:n]); err != nil {
		t.Fatalf("unpack q1: %v", err)
	}
	if m1.Rcode != dns.RcodeSuccess || len(m1.Answer) != 1 {
		t.Fatalf("q1 reply = rcode %d answers %d", m1.Rcode, len(m1.Answer))
	}

	// Verify no persistent gVisor UDP endpoints were registered by stateless DNS.
	if eps := service.stack.RegisteredEndpoints(); len(eps) != 0 {
		t.Fatalf("registered gVisor endpoints after stateless DNS = %d, want 0", len(eps))
	}
}

// TestTCP_PreHandshakeRejectDecisivelyBlockedIP verifies that a destination IP
// matched by a decisive CIDR block rule gets an immediate SYN-RST without
// completing a gVisor TCP handshake or consuming flow slots.
func TestTCP_PreHandshakeRejectDecisivelyBlockedIP(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		{
			Sources:     []string{"203.0.113.0/24"},
			Destination: router.EgressBlock,
			MatchType:   router.TypeCIDR,
		},
		router.CloneRoute(router.RouteClientDefault),
	}); err != nil {
		t.Fatalf("SetRoutes: %v", err)
	}

	service, peer, peerNIC := newLinkedService(t, Config{MaxConnections: 4})
	// Leave customRoute false so router.IPBlockDecisive runs before CreateEndpoint.
	if service.customRoute {
		t.Fatal("expected default router mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := gonet.DialContextTCP(ctx, peer, tcpip.FullAddress{
		NIC:  peerNIC,
		Addr: tcpip.AddrFrom4([4]byte{203, 0, 113, 7}),
		Port: 443,
	}, ipv4.ProtocolNumber)
	if err == nil {
		t.Fatal("expected decisively blocked IP to fail TCP handshake with RST")
	}
	if got := len(service.flowSlots); got != 0 {
		t.Fatalf("flowSlots after pre-handshake reject = %d, want 0", got)
	}
	// Ensure no stale TCP endpoint leaked.
	if eps := service.stack.RegisteredEndpoints(); len(eps) > 0 {
		t.Fatalf("unexpected %d registered endpoint(s) after pre-handshake reject", len(eps))
	}
}

// TestUDPNAT_QUICInitialSNIDomainRouting verifies that a QUIC v1 Initial
// datagram addressed to a raw IP recovers its SNI and routes by domain name.
func TestUDPNAT_QUICInitialSNIDomainRouting(t *testing.T) {
	peerUDP, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	defer func() { _ = peerUDP.Close() }()

	peerPort := uint16(peerUDP.LocalAddr().(*net.UDPAddr).Port)
	virtualIP := netip.MustParseAddr("198.51.100.44")

	route := &splitEIMRoute{
		Direct:              &direct.Direct{},
		name:                "direct",
		peerPortToVirtualIP: map[uint16]netip.Addr{peerPort: virtualIP},
	}

	seenKey := make(chan string, 1)
	service, peer, peerNIC := newLinkedService(t, Config{
		MaxConnections: 8,
		MaxUDPSessions: 4,
		UDPMapping:     UDPMappingEIM,
		UDPFiltering:   UDPFilteringEIF,
		UDPIdleTimeout: 5 * time.Second,
	})
	service.setResolveRoute(func(key string) (transport.Transport, error) {
		select {
		case seenKey <- key:
		default:
		}
		return route, nil
	})

	quicPkt, err := sniff.BuildQUICInitial("http3.example.org", sniff.QUICVersion1)
	if err != nil {
		t.Fatalf("BuildQUICInitial: %v", err)
	}
	clientConn, err := gonet.DialUDP(peer, &tcpip.FullAddress{
		NIC:  peerNIC,
		Addr: tcpip.AddrFrom4([4]byte{10, 0, 0, 2}),
		Port: 47000,
	}, nil, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("gonet.DialUDP: %v", err)
	}
	defer func() { _ = clientConn.Close() }()

	if _, err := clientConn.WriteTo(quicPkt, &net.UDPAddr{
		IP:   net.IP(virtualIP.AsSlice()),
		Port: int(peerPort),
	}); err != nil {
		t.Fatalf("WriteTo QUIC Initial: %v", err)
	}

	select {
	case got := <-seenKey:
		if got != "http3.example.org" {
			t.Fatalf("resolved route key = %q, want http3.example.org", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for QUIC SNI route lookup")
	}
}
