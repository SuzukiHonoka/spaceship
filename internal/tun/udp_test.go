package tun

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
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

func TestUDPFlowTrackingIsOneSessionPerTuple(t *testing.T) {
	service := &Service{}
	if !service.trackUDPFlow("a|1|b|2") {
		t.Fatal("first trackUDPFlow rejected a new 4-tuple")
	}
	if service.trackUDPFlow("a|1|b|2") {
		t.Fatal("trackUDPFlow accepted a duplicate 4-tuple")
	}
	if !service.trackUDPFlow("a|1|b|3") {
		t.Fatal("trackUDPFlow rejected a different 4-tuple")
	}
	service.untrackUDPFlow("a|1|b|2")
	if !service.trackUDPFlow("a|1|b|2") {
		t.Fatal("trackUDPFlow rejected a 4-tuple after it was released")
	}
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
	err := service.proxyUDP(conn, id)
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
	if err := service.proxyUDP(conn, id); !errors.Is(err, wantErr) {
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

func TestUDPConnectionLimitRejectsExtraSession(t *testing.T) {
	service, peer, peerNIC := newLinkedService(t, Config{
		MaxConnections: 1,
		UDPIdleTimeout: time.Minute,
	})
	route := newHoldPacketRoute()
	service.setResolveRoute(func(string) (transport.Transport, error) {
		return route, nil
	})

	first := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{198, 51, 100, 40}), 9, ipv4.ProtocolNumber)
	if _, err := first.Write([]byte("hold")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-route.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first UDP session did not start")
	}

	second := dialPeerUDP(t, peer, peerNIC, tcpip.AddrFrom4([4]byte{198, 51, 100, 41}), 9, ipv4.ProtocolNumber)
	if _, err := second.Write([]byte("extra")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := route.dials.Load(); got != 1 {
		t.Fatalf("dials = %d, want the extra 4-tuple rejected by MaxConnections", got)
	}
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
	return newEchoPacketConn(), nil
}

var _ transport.ContextPacketDialer = (*udpEchoRoute)(nil)

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
	for {
		c.mu.Lock()
		if len(c.queue) > 0 {
			pkt := c.queue[0]
			c.queue = c.queue[1:]
			c.mu.Unlock()
			n := copy(p, pkt)
			return n, c.RemoteAddr(), nil
		}
		if c.closed {
			c.mu.Unlock()
			return 0, nil, net.ErrClosed
		}
		deadline := c.readDL.Load()
		c.mu.Unlock()

		var timer <-chan time.Time
		var stop func()
		if deadline > 0 {
			wait := time.Until(time.Unix(0, deadline))
			if wait <= 0 {
				return 0, nil, os.ErrDeadlineExceeded
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
			return 0, nil, os.ErrDeadlineExceeded
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
