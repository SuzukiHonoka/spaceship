package socks

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
)

func TestParseUDPHeader(t *testing.T) {
	tests := []struct {
		name    string
		buf     []byte
		want    *UDPHeader
		wantErr bool
	}{
		{
			name: "valid ipv4",
			buf:  append([]byte{0, 0, 0, ipv4Address, 127, 0, 0, 1, 0, 80}, []byte("data")...),
			want: &UDPHeader{
				Frag:       0,
				Addr:       &AddrSpec{IP: net.ParseIP("127.0.0.1").To4(), Port: 80},
				DataOffset: 10,
			},
			wantErr: false,
		},
		{
			name: "valid ipv6",
			buf:  append([]byte{0, 0, 0, ipv6Address}, append(net.ParseIP("::1"), 0, 80)...),
			want: &UDPHeader{
				Frag:       0,
				Addr:       &AddrSpec{IP: net.ParseIP("::1"), Port: 80},
				DataOffset: 22,
			},
			wantErr: false,
		},
		{
			name: "valid fqdn",
			buf:  append([]byte{0, 0, 0, fqdnAddress, 7}, append([]byte("example"), 0, 80)...),
			want: &UDPHeader{
				Frag:       0,
				Addr:       &AddrSpec{FQDN: "example", Port: 80},
				DataOffset: 14,
			},
			wantErr: false,
		},
		{
			name:    "too short",
			buf:     []byte{0, 0, 0},
			wantErr: true,
		},
		{
			name:    "invalid atyp",
			buf:     []byte{0, 0, 0, 0x05, 127, 0, 0, 1, 0, 80},
			wantErr: true,
		},
		{
			name:    "non-zero reserved bytes",
			buf:     []byte{0, 1, 0, ipv4Address, 127, 0, 0, 1, 0, 80},
			wantErr: true,
		},
		{
			name:    "truncated ipv4",
			buf:     []byte{0, 0, 0, ipv4Address, 127, 0, 0},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseUDPHeader(tt.buf)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseUDPHeader() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Frag != tt.want.Frag {
					t.Errorf("Frag = %v, want %v", got.Frag, tt.want.Frag)
				}
				if got.DataOffset != tt.want.DataOffset {
					t.Errorf("DataOffset = %v, want %v", got.DataOffset, tt.want.DataOffset)
				}
				if tt.want.Addr.IP != nil && !got.Addr.IP.Equal(tt.want.Addr.IP) {
					t.Errorf("IP = %v, want %v", got.Addr.IP, tt.want.Addr.IP)
				}
				if got.Addr.FQDN != tt.want.Addr.FQDN {
					t.Errorf("FQDN = %v, want %v", got.Addr.FQDN, tt.want.Addr.FQDN)
				}
				if got.Addr.Port != tt.want.Addr.Port {
					t.Errorf("Port = %v, want %v", got.Addr.Port, tt.want.Addr.Port)
				}
			}
		})
	}
}

func TestMarshalUDPHeader(t *testing.T) {
	tests := []struct {
		name    string
		addr    *AddrSpec
		want    []byte
		wantErr bool
	}{
		{
			name: "ipv4",
			addr: &AddrSpec{IP: net.ParseIP("127.0.0.1"), Port: 80},
			want: []byte{0, 0, 0, ipv4Address, 127, 0, 0, 1, 0, 80},
		},
		{
			name: "ipv6",
			addr: &AddrSpec{IP: net.ParseIP("::1"), Port: 80},
			want: append([]byte{0, 0, 0, ipv6Address}, append(net.ParseIP("::1"), 0, 80)...),
		},
		{
			name: "fqdn",
			addr: &AddrSpec{FQDN: "example", Port: 80},
			want: []byte{0, 0, 0, fqdnAddress, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 0, 80},
		},
		{
			name:    "invalid",
			addr:    &AddrSpec{},
			wantErr: true,
		},
		{
			name:    "nil",
			addr:    nil,
			wantErr: true,
		},
		{
			name:    "fqdn too long",
			addr:    &AddrSpec{FQDN: strings.Repeat("a", 256), Port: 80},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MarshalUDPHeader(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Errorf("MarshalUDPHeader() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !bytes.Equal(got, tt.want) {
				t.Errorf("MarshalUDPHeader() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestUDPRelay_RejectsEgressWithoutUDP verifies that when the matched egress
// does not implement transport.PacketDialer, the relay returns an error and
// does NOT silently fall back to a local UDP socket (which would bypass the
// proxy and leak the client's IP / local DNS). No NAT entry must be created.
func TestUDPRelay_RejectsEgressWithoutUDP(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		&router.Route{MatchType: router.TypeRegex, Sources: []string{".*"}, Destination: router.EgressBlackHole},
	}); err != nil {
		t.Fatal(err)
	}

	relay, err := NewUDPRelay(nil, nil)
	if err != nil {
		t.Fatalf("NewUDPRelay() error = %v", err)
	}
	defer relay.Close()

	const target = "8.8.8.8:53"
	_, err = relay.getOrCreateNAT(target, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345})
	if err == nil {
		t.Fatal("expected an error for an egress without UDP support; got nil (indicates a local-dial leak)")
	}
	if !strings.Contains(err.Error(), "does not support UDP") {
		t.Errorf("expected 'does not support UDP' error, got: %v", err)
	}
	if _, ok := relay.natTable.Load(target); ok {
		t.Error("a NAT entry was created despite the egress not supporting UDP")
	}
}

// TestUDPRelay_LargePayload exercises the reverse path with a large target
// response, validating the in-place header construction (no second buffer, no
// overflow panic) and that the payload arrives intact.
func TestUDPRelay_LargePayload(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		&router.Route{MatchType: router.TypeRegex, Sources: []string{".*"}, Destination: router.EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}

	// Multi-KB payload: above the typical MTU yet within the per-datagram limit
	// of all platforms (macOS caps UDP datagrams at ~9216 bytes by default).
	const respSize = 8000
	es, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer es.Close()
	big := bytes.Repeat([]byte{0xAB}, respSize)
	go func() {
		buf := make([]byte, 2048)
		for {
			_, addr, err := es.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = es.WriteTo(big, addr)
		}
	}()

	relay, err := NewUDPRelay(nil, nil)
	if err != nil {
		t.Fatalf("NewUDPRelay: %v", err)
	}
	go func() { _ = relay.Run() }()
	defer relay.Close()

	client, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client listen: %v", err)
	}
	defer client.Close()

	target := &AddrSpec{IP: net.ParseIP("127.0.0.1"), Port: uint16(es.LocalAddr().(*net.UDPAddr).Port)}
	header, _ := MarshalUDPHeader(target)
	packet := append(append([]byte{}, header...), []byte("ping")...)
	relayAddr := relay.RelayAddr().(*net.UDPAddr)
	dst := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: relayAddr.Port}

	var resp []byte
	rbuf := make([]byte, 70000)
	for attempt := 0; attempt < 20; attempt++ {
		_, _ = client.WriteTo(packet, dst)
		_ = client.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, _, err := client.ReadFrom(rbuf)
		if err == nil {
			resp = rbuf[:n]
			break
		}
	}
	if resp == nil {
		t.Fatal("no response received for large payload")
	}
	rhdr, err := ParseUDPHeader(resp)
	if err != nil {
		t.Fatalf("parse response header: %v", err)
	}
	if got := len(resp[rhdr.DataOffset:]); got != respSize {
		t.Errorf("payload length = %d, want %d", got, respSize)
	}
}

// TestUDPRelay_RejectsFragmentedPacket verifies fragmented datagrams (FRAG != 0)
// are dropped and never create an outbound NAT entry.
func TestUDPRelay_RejectsFragmentedPacket(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		&router.Route{MatchType: router.TypeRegex, Sources: []string{".*"}, Destination: router.EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}

	relay, err := NewUDPRelay(nil, nil)
	if err != nil {
		t.Fatalf("NewUDPRelay: %v", err)
	}
	defer relay.Close()

	header, _ := MarshalUDPHeader(&AddrSpec{IP: net.ParseIP("127.0.0.1"), Port: 9999})
	header[2] = 1 // FRAG byte != 0
	packet := append(header, []byte("data")...)

	relay.handlePacket(udpPacket{data: packet, clientAddr: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}})

	if n := natEntryCount(relay); n != 0 {
		t.Errorf("fragmented packet created %d NAT entries, want 0", n)
	}
}

// TestUDPRelay_RejectsWrongClientIP verifies datagrams from an IP other than the
// authenticated client are dropped before any outbound socket is created.
func TestUDPRelay_RejectsWrongClientIP(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		&router.Route{MatchType: router.TypeRegex, Sources: []string{".*"}, Destination: router.EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}

	relay, err := NewUDPRelay(net.ParseIP("10.0.0.1"), nil) // only this IP is allowed
	if err != nil {
		t.Fatalf("NewUDPRelay: %v", err)
	}
	defer relay.Close()

	header, _ := MarshalUDPHeader(&AddrSpec{IP: net.ParseIP("127.0.0.1"), Port: 9999})
	packet := append(header, []byte("data")...)

	relay.handlePacket(udpPacket{data: packet, clientAddr: &net.UDPAddr{IP: net.ParseIP("127.0.0.2"), Port: 1}})

	if n := natEntryCount(relay); n != 0 {
		t.Errorf("datagram from unauthorized IP created %d NAT entries, want 0", n)
	}
}

func natEntryCount(r *UDPRelay) int {
	n := 0
	r.natTable.Range(func(_ string, _ *natEntry) bool { n++; return true })
	return n
}

func TestUDPRelayReadLoopCopiesPacketsAndDropsWhenQueueFull(t *testing.T) {
	first := []byte("first")
	relay := &UDPRelay{
		relay: &scriptedPacketConn{reads: []readEvent{
			{data: first, addr: testClientAddr()},
			{data: []byte("second"), addr: testClientAddr()},
		}},
		jobs: make(chan udpPacket, 1),
	}

	if err := relay.readLoop(); err != nil {
		t.Fatal(err)
	}
	if got := len(relay.jobs); got != 1 {
		t.Fatalf("queued packets = %d, want 1", got)
	}
	job := <-relay.jobs
	if !bytes.Equal(job.data, first) {
		t.Fatalf("queued packet = %q, want %q", job.data, first)
	}
}

func TestNATTableInstallEvictsLeastRecentlyUsed(t *testing.T) {
	table := natTable{}
	oldestConn := &scriptedPacketConn{}
	oldest := &natEntry{conn: oldestConn}
	oldest.lastSeen.Store(1)
	recent := &natEntry{}
	recent.lastSeen.Store(2)
	newEntry := &natEntry{}
	newEntry.lastSeen.Store(3)
	table.Store("oldest", oldest)
	table.Store("recent", recent)

	actual, evicted, installed := table.Install("new", newEntry, 2)
	if !installed || actual != newEntry {
		t.Fatalf("Install() = (%p, %v), want new entry installed", actual, installed)
	}
	if evicted != oldest {
		t.Fatalf("evicted = %p, want oldest entry %p", evicted, oldest)
	}
	if _, ok := table.Load("oldest"); ok {
		t.Fatal("oldest entry remains in bounded NAT table")
	}
	if _, ok := table.Load("recent"); !ok {
		t.Fatal("recent entry was evicted")
	}
	if _, ok := table.Load("new"); !ok {
		t.Fatal("new entry was not stored")
	}
}

// timeoutError is a net.Error that reports a timeout, for driving reverseRelay.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type readEvent struct {
	data []byte
	addr net.Addr
	err  error
}

// scriptedPacketConn is a net.PacketConn with scripted ReadFrom results and
// captured WriteTo payloads. Once the script is exhausted, ReadFrom returns
// net.ErrClosed so the relay loop terminates.
type scriptedPacketConn struct {
	mu         sync.Mutex
	reads      []readEvent
	idx        int
	writes     [][]byte
	closeCount int
}

type blockingPacketConn struct {
	closed    chan struct{}
	closeOnce sync.Once
}

func newBlockingPacketConn() *blockingPacketConn {
	return &blockingPacketConn{closed: make(chan struct{})}
}

func (c *blockingPacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	<-c.closed
	return 0, nil, net.ErrClosed
}

func (c *blockingPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return len(p), nil
}

func (c *blockingPacketConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *blockingPacketConn) LocalAddr() net.Addr              { return testClientAddr() }
func (c *blockingPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *blockingPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *blockingPacketConn) SetWriteDeadline(time.Time) error { return nil }

func (c *scriptedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idx >= len(c.reads) {
		return 0, nil, net.ErrClosed
	}
	ev := c.reads[c.idx]
	c.idx++
	if ev.err != nil {
		return 0, ev.addr, ev.err
	}
	return copy(p, ev.data), ev.addr, nil
}

func (c *scriptedPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (c *scriptedPacketConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeCount++
	return nil
}
func (c *scriptedPacketConn) LocalAddr() net.Addr                { return &net.UDPAddr{} }
func (c *scriptedPacketConn) SetDeadline(t time.Time) error      { return nil }
func (c *scriptedPacketConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *scriptedPacketConn) SetWriteDeadline(t time.Time) error { return nil }

func (c *scriptedPacketConn) writeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.writes)
}

func (c *scriptedPacketConn) readIdx() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.idx
}

func (c *scriptedPacketConn) closeCountValue() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCount
}

type closeCountingTransport struct {
	closeCount atomic.Int32
}

func (t *closeCountingTransport) String() string { return "closeCounting" }
func (t *closeCountingTransport) Dial(_, _ string) (net.Conn, error) {
	return nil, errors.New("unused")
}
func (t *closeCountingTransport) Proxy(_ context.Context, _ string, localAddr chan<- string, _ io.Writer, _ io.Reader) error {
	close(localAddr)
	return errors.New("unused")
}
func (t *closeCountingTransport) Close() error {
	t.closeCount.Add(1)
	return nil
}

type targetAwareTransport struct {
	closeCountingTransport
	conn            net.PacketConn
	target          net.Addr
	dialErr         error
	dialPacketCalls atomic.Int32
	dialTargetCalls atomic.Int32
}

func (t *targetAwareTransport) DialPacket(_, _ string) (net.PacketConn, error) {
	t.dialPacketCalls.Add(1)
	return t.conn, t.dialErr
}

func (t *targetAwareTransport) DialPacketTarget(_, _ string) (net.PacketConn, net.Addr, error) {
	t.dialTargetCalls.Add(1)
	return t.conn, t.target, t.dialErr
}

type packetOnlyTransport struct {
	closeCountingTransport
	conn      net.PacketConn
	dialErr   error
	dialCalls atomic.Int32
}

func (t *packetOnlyTransport) DialPacket(_, _ string) (net.PacketConn, error) {
	t.dialCalls.Add(1)
	return t.conn, t.dialErr
}

type testPacketAddr string

func (a testPacketAddr) Network() string { return "udp" }
func (a testPacketAddr) String() string  { return string(a) }

const testClientPort = 5555

func testClientAddr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: testClientPort}
}

func TestNATEntry_CloseClosesConnAndRouteOnce(t *testing.T) {
	outbound := &scriptedPacketConn{}
	route := &closeCountingTransport{}
	limiter := newUDPResourceLimiter(1, 1)
	if !limiter.acquire("client") {
		t.Fatal("failed to acquire NAT test budget")
	}
	entry := &natEntry{
		conn:      outbound,
		route:     route,
		limiter:   limiter,
		clientKey: "client",
	}

	entry.Close()
	entry.Close()

	if got := outbound.closeCountValue(); got != 1 {
		t.Errorf("packet conn close count = %d, want 1", got)
	}
	if got := route.closeCount.Load(); got != 1 {
		t.Errorf("route close count = %d, want 1", got)
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.total != 0 || limiter.byClient["client"] != 0 {
		t.Errorf("NAT budget after close = total %d, client %d; want zero", limiter.total, limiter.byClient["client"])
	}
}

func TestDialPacketTargetUsesResolvedAddressFromTransport(t *testing.T) {
	conn := &scriptedPacketConn{}
	resolved := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 53}
	route := &targetAwareTransport{conn: conn, target: resolved}

	gotConn, gotTarget, err := dialPacketTarget(route, "udp", "dual-stack.example:53")
	if err != nil {
		t.Fatalf("dialPacketTarget() error = %v", err)
	}
	if gotConn != conn || gotTarget != resolved {
		t.Fatalf("dialPacketTarget() = (%T, %v), want supplied conn and %v", gotConn, gotTarget, resolved)
	}
	if route.dialTargetCalls.Load() != 1 || route.dialPacketCalls.Load() != 0 {
		t.Fatalf("dial calls = target %d, legacy %d; want 1, 0", route.dialTargetCalls.Load(), route.dialPacketCalls.Load())
	}
}

func TestDialPacketTargetLegacyTransportPreservesDomain(t *testing.T) {
	conn := &scriptedPacketConn{}
	route := &packetOnlyTransport{conn: conn}

	gotConn, gotTarget, err := dialPacketTarget(route, "udp", "proxy.example:53")
	if err != nil {
		t.Fatalf("dialPacketTarget() error = %v", err)
	}
	if gotConn != conn {
		t.Fatalf("dialPacketTarget() conn = %T, want supplied conn", gotConn)
	}
	if gotTarget.String() != "proxy.example:53" {
		t.Fatalf("dialPacketTarget() target = %v, want unresolved domain", gotTarget)
	}
	if route.dialCalls.Load() != 1 {
		t.Fatalf("DialPacket() calls = %d, want 1", route.dialCalls.Load())
	}
}

func TestDialPacketTargetRejectsInvalidTransportResults(t *testing.T) {
	t.Run("target dialer nil connection", func(t *testing.T) {
		route := &targetAwareTransport{target: testPacketAddr("example.com:53")}
		if _, _, err := dialPacketTarget(route, "udp", "example.com:53"); err == nil || !strings.Contains(err.Error(), "nil connection") {
			t.Fatalf("dialPacketTarget() error = %v, want nil connection error", err)
		}
	})

	t.Run("target dialer nil target closes connection", func(t *testing.T) {
		conn := &scriptedPacketConn{}
		route := &targetAwareTransport{conn: conn}
		if _, _, err := dialPacketTarget(route, "udp", "example.com:53"); err == nil || !strings.Contains(err.Error(), "nil target") {
			t.Fatalf("dialPacketTarget() error = %v, want nil target error", err)
		}
		if got := conn.closeCountValue(); got != 1 {
			t.Fatalf("connection close count = %d, want 1", got)
		}
	})

	t.Run("legacy dialer error closes connection", func(t *testing.T) {
		conn := &scriptedPacketConn{}
		dialErr := errors.New("legacy dial failed")
		route := &packetOnlyTransport{conn: conn, dialErr: dialErr}
		if _, _, err := dialPacketTarget(route, "udp", "example.com:53"); !errors.Is(err, dialErr) {
			t.Fatalf("dialPacketTarget() error = %v, want %v", err, dialErr)
		}
		if got := conn.closeCountValue(); got != 1 {
			t.Fatalf("connection close count = %d, want 1", got)
		}
	})

	t.Run("legacy dialer nil connection", func(t *testing.T) {
		route := &packetOnlyTransport{}
		if _, _, err := dialPacketTarget(route, "udp", "example.com:53"); err == nil || !strings.Contains(err.Error(), "nil connection") {
			t.Fatalf("dialPacketTarget() error = %v, want nil connection error", err)
		}
	})
}

func TestUDPResourceLimiterEnforcesGlobalAndPerClientLimits(t *testing.T) {
	limiter := newUDPResourceLimiter(3, 2)
	if !limiter.acquire("client-a") {
		t.Fatal("expected first client-a acquisition to succeed")
	}
	if !limiter.acquire("client-a") {
		t.Fatal("expected second client-a acquisition to succeed")
	}
	if limiter.acquire("client-a") {
		t.Fatal("per-client limit allowed a third client-a acquisition")
	}
	if !limiter.acquire("client-b") {
		t.Fatal("expected client-b acquisition to use remaining global capacity")
	}
	if limiter.acquire("client-c") {
		t.Fatal("global limit allowed a fourth acquisition")
	}

	limiter.release("client-a")
	if !limiter.acquire("client-c") {
		t.Fatal("released capacity was not reusable")
	}
}

func TestNewUDPRelayAssociationLimitReleasedOnClose(t *testing.T) {
	associations := newUDPResourceLimiter(1, 1)
	natEntries := newUDPResourceLimiter(4, 4)
	clientIP := net.ParseIP("127.0.0.1")
	listen := func(_, _ string) (net.PacketConn, error) {
		return &scriptedPacketConn{}, nil
	}

	first, err := newUDPRelayWithListener(clientIP, nil, associations, natEntries, 0, listen)
	if err != nil {
		t.Fatalf("first newUDPRelay() error = %v", err)
	}
	if _, err := newUDPRelayWithListener(clientIP, nil, associations, natEntries, 0, listen); !errors.Is(err, ErrUDPAssociationLimit) {
		first.Close()
		t.Fatalf("second newUDPRelay() error = %v, want ErrUDPAssociationLimit", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	third, err := newUDPRelayWithListener(clientIP, nil, associations, natEntries, 0, listen)
	if err != nil {
		t.Fatalf("newUDPRelay() after release error = %v", err)
	}
	defer third.Close()
}

func TestNewUDPRelayListenFailureReleasesAssociationLimit(t *testing.T) {
	associations := newUDPResourceLimiter(1, 1)
	natEntries := newUDPResourceLimiter(1, 1)
	clientIP := net.ParseIP("127.0.0.1")
	listenErr := errors.New("listen failed")

	_, err := newUDPRelayWithListener(clientIP, nil, associations, natEntries, 0, func(_, _ string) (net.PacketConn, error) {
		return nil, listenErr
	})
	if !errors.Is(err, listenErr) {
		t.Fatalf("newUDPRelayWithListener() error = %v, want %v", err, listenErr)
	}

	relay, err := newUDPRelayWithListener(clientIP, nil, associations, natEntries, 0, func(_, _ string) (net.PacketConn, error) {
		return &scriptedPacketConn{}, nil
	})
	if err != nil {
		t.Fatalf("newUDPRelayWithListener() after listen failure error = %v", err)
	}
	defer relay.Close()
}

func TestUDPRelayDialFailureReleasesNATLimitAndRoute(t *testing.T) {
	associations := newUDPResourceLimiter(1, 1)
	natEntries := newUDPResourceLimiter(1, 1)
	relay, err := newUDPRelayWithListener(
		net.ParseIP("127.0.0.1"),
		nil,
		associations,
		natEntries,
		0,
		func(_, _ string) (net.PacketConn, error) { return &scriptedPacketConn{}, nil },
	)
	if err != nil {
		t.Fatalf("newUDPRelayWithListener() error = %v", err)
	}
	defer relay.Close()

	dialErr := errors.New("dial failed")
	outbound := &scriptedPacketConn{}
	route := &targetAwareTransport{conn: outbound, dialErr: dialErr}
	relay.getRoute = func(string) (transport.Transport, error) { return route, nil }

	_, err = relay.getOrCreateNAT("proxy.example:53", testClientAddr())
	if !errors.Is(err, dialErr) {
		t.Fatalf("getOrCreateNAT() error = %v, want %v", err, dialErr)
	}
	if got := route.closeCount.Load(); got != 1 {
		t.Fatalf("route close count = %d, want 1", got)
	}
	if got := outbound.closeCountValue(); got != 1 {
		t.Fatalf("packet conn close count = %d, want 1", got)
	}
	if n := natEntryCount(relay); n != 0 {
		t.Fatalf("NAT entries after dial failure = %d, want 0", n)
	}

	natEntries.mu.Lock()
	defer natEntries.mu.Unlock()
	if natEntries.total != 0 || natEntries.byClient[relay.clientKey] != 0 {
		t.Fatalf("NAT budget after dial failure = total %d, client %d; want zero", natEntries.total, natEntries.byClient[relay.clientKey])
	}
}

func TestUDPRelayNATLimitReleasedOnClose(t *testing.T) {
	associations := newUDPResourceLimiter(2, 2)
	natEntries := newUDPResourceLimiter(1, 1)
	relay, err := newUDPRelayWithListener(
		net.ParseIP("127.0.0.1"),
		nil,
		associations,
		natEntries,
		0,
		func(_, _ string) (net.PacketConn, error) { return &scriptedPacketConn{}, nil },
	)
	if err != nil {
		t.Fatalf("newUDPRelay() error = %v", err)
	}
	relay.getRoute = func(host string) (transport.Transport, error) {
		return &targetAwareTransport{
			conn:   newBlockingPacketConn(),
			target: testPacketAddr(net.JoinHostPort(host, "53")),
		}, nil
	}

	if _, err := relay.getOrCreateNAT("127.0.0.1:53001", testClientAddr()); err != nil {
		relay.Close()
		t.Fatalf("first getOrCreateNAT() error = %v", err)
	}
	if _, err := relay.getOrCreateNAT("127.0.0.1:53002", testClientAddr()); !errors.Is(err, ErrUDPNATLimit) {
		relay.Close()
		t.Fatalf("second getOrCreateNAT() error = %v, want ErrUDPNATLimit", err)
	}
	if err := relay.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	natEntries.mu.Lock()
	defer natEntries.mu.Unlock()
	if natEntries.total != 0 {
		t.Fatalf("NAT budget after relay close = %d, want 0", natEntries.total)
	}
}

func TestAddrSpecFromNetAddr(t *testing.T) {
	tests := []struct {
		name string
		addr net.Addr
		want *AddrSpec
		ok   bool
	}{
		{
			name: "udp ipv4",
			addr: &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 53},
			want: &AddrSpec{IP: net.ParseIP("1.2.3.4"), Port: 53},
			ok:   true,
		},
		{
			name: "generic fqdn",
			addr: testPacketAddr("example.com:443"),
			want: &AddrSpec{FQDN: "example.com", Port: 443},
			ok:   true,
		},
		{
			name: "generic ip",
			addr: testPacketAddr("8.8.8.8:53"),
			want: &AddrSpec{IP: net.ParseIP("8.8.8.8"), Port: 53},
			ok:   true,
		},
		{name: "nil", addr: nil, ok: false},
		{name: "missing port", addr: testPacketAddr("example.com"), ok: false},
		{name: "bad port", addr: testPacketAddr("example.com:not-a-port"), ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := addrSpecFromNetAddr(tt.addr)
			if ok != tt.ok {
				t.Fatalf("addrSpecFromNetAddr() ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if got.FQDN != tt.want.FQDN || got.Port != tt.want.Port {
				t.Fatalf("addrSpecFromNetAddr() = %+v, want %+v", got, tt.want)
			}
			if tt.want.IP != nil && !got.IP.Equal(tt.want.IP) {
				t.Fatalf("addrSpecFromNetAddr() IP = %v, want %v", got.IP, tt.want.IP)
			}
		})
	}
}

// TestReverseRelay_RelaysWithHeader verifies a target response is delivered to
// the client with a correct SOCKS5 UDP header prepended in place.
func TestReverseRelay_RelaysWithHeader(t *testing.T) {
	relaySock := &scriptedPacketConn{}
	relay := &UDPRelay{relay: relaySock, done: make(chan struct{})}

	outbound := &scriptedPacketConn{reads: []readEvent{
		{data: []byte("PONG"), addr: &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 53}},
	}}
	relay.reverseRelay(&natEntry{conn: outbound}, natKey(testClientAddr(), "1.2.3.4:53"), testClientAddr(), "1.2.3.4:53")

	if relaySock.writeCount() != 1 {
		t.Fatalf("writes to client = %d, want 1", relaySock.writeCount())
	}
	got := relaySock.writes[0]
	hdr, err := ParseUDPHeader(got)
	if err != nil {
		t.Fatalf("relayed datagram header parse: %v", err)
	}
	if hdr.Addr.IP.String() != "1.2.3.4" || hdr.Addr.Port != 53 {
		t.Errorf("relayed header = %v:%d, want 1.2.3.4:53", hdr.Addr.IP, hdr.Addr.Port)
	}
	if string(got[hdr.DataOffset:]) != "PONG" {
		t.Errorf("relayed payload = %q, want PONG", got[hdr.DataOffset:])
	}
}

// TestReverseRelay_RelaysDomainHeader verifies generic packet addresses retain
// FQDN source information instead of being rewritten to 0.0.0.0.
func TestReverseRelay_RelaysDomainHeader(t *testing.T) {
	relaySock := &scriptedPacketConn{}
	relay := &UDPRelay{relay: relaySock, done: make(chan struct{})}

	outbound := &scriptedPacketConn{reads: []readEvent{
		{data: []byte("PONG"), addr: testPacketAddr("example.com:53")},
	}}
	relay.reverseRelay(&natEntry{conn: outbound}, natKey(testClientAddr(), "example.com:53"), testClientAddr(), "example.com:53")

	if relaySock.writeCount() != 1 {
		t.Fatalf("writes to client = %d, want 1", relaySock.writeCount())
	}
	got := relaySock.writes[0]
	hdr, err := ParseUDPHeader(got)
	if err != nil {
		t.Fatalf("relayed datagram header parse: %v", err)
	}
	if hdr.Addr.FQDN != "example.com" || hdr.Addr.Port != 53 {
		t.Errorf("relayed header = %+v, want example.com:53", hdr.Addr)
	}
	if string(got[hdr.DataOffset:]) != "PONG" {
		t.Errorf("relayed payload = %q, want PONG", got[hdr.DataOffset:])
	}
}

func TestReverseRelay_DropsOversizedResponse(t *testing.T) {
	relaySock := &scriptedPacketConn{}
	relay := &UDPRelay{relay: relaySock, done: make(chan struct{})}

	longHost := strings.Repeat("a", 255)
	headerLen := 4 + 1 + len(longHost) + 2
	oversizedPayload := bytes.Repeat([]byte("x"), udpMaxPacketSize-headerLen+1)
	outbound := &scriptedPacketConn{reads: []readEvent{
		{data: oversizedPayload, addr: testPacketAddr(longHost + ":53")},
	}}
	relay.reverseRelay(&natEntry{conn: outbound}, natKey(testClientAddr(), longHost+":53"), testClientAddr(), longHost+":53")

	if relaySock.writeCount() != 0 {
		t.Errorf("writes = %d, want 0 for oversized response", relaySock.writeCount())
	}
}

// TestReverseRelay_SkipsNonUDPAddr verifies a non-*net.UDPAddr source is skipped.
func TestReverseRelay_SkipsNonUDPAddr(t *testing.T) {
	relaySock := &scriptedPacketConn{}
	relay := &UDPRelay{relay: relaySock, done: make(chan struct{})}

	outbound := &scriptedPacketConn{reads: []readEvent{
		{data: []byte("x"), addr: &net.IPAddr{IP: net.ParseIP("1.2.3.4")}},
	}}
	relay.reverseRelay(&natEntry{conn: outbound}, natKey(testClientAddr(), "1.2.3.4:53"), testClientAddr(), "1.2.3.4:53")

	if relaySock.writeCount() != 0 {
		t.Errorf("writes = %d, want 0 for non-UDP source addr", relaySock.writeCount())
	}
}

// TestReverseRelay_TimeoutKeepAlive verifies that on a read timeout with recent
// forward activity, the relay keeps the flow alive and processes the next packet.
func TestReverseRelay_TimeoutKeepAlive(t *testing.T) {
	relaySock := &scriptedPacketConn{}
	relay := &UDPRelay{relay: relaySock, done: make(chan struct{})}

	const target = "1.2.3.4:53"
	entry := &natEntry{}
	entry.lastSeen.Store(time.Now().UnixNano()) // recent
	relay.natTable.Store(natKey(testClientAddr(), target), entry)

	outbound := &scriptedPacketConn{reads: []readEvent{
		{err: timeoutError{}}, // timeout -> keepalive -> continue
		{data: []byte("PONG"), addr: &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 53}},
	}}
	entry.conn = outbound
	relay.reverseRelay(entry, natKey(testClientAddr(), target), testClientAddr(), target)

	if relaySock.writeCount() != 1 {
		t.Errorf("writes = %d, want 1 (keepalive should process the next datagram)", relaySock.writeCount())
	}
}

// TestReverseRelay_TimeoutIdleReturns verifies that on a read timeout with no
// recent activity, the relay tears the flow down without processing more.
func TestReverseRelay_TimeoutIdleReturns(t *testing.T) {
	relaySock := &scriptedPacketConn{}
	relay := &UDPRelay{relay: relaySock, done: make(chan struct{})}

	outbound := &scriptedPacketConn{reads: []readEvent{
		{err: timeoutError{}}, // timeout, no natTable entry -> idle -> return
		{data: []byte("PONG"), addr: &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 53}},
	}}
	relay.reverseRelay(&natEntry{conn: outbound}, natKey(testClientAddr(), "1.2.3.4:53"), testClientAddr(), "1.2.3.4:53")

	if relaySock.writeCount() != 0 {
		t.Errorf("writes = %d, want 0 (idle timeout should return)", relaySock.writeCount())
	}
	if relay := outbound.readIdx(); relay != 1 {
		t.Errorf("reads = %d, want 1 (should return on first idle timeout)", relay)
	}
}

// TestReverseRelay_DoneReturns verifies that a closed relay terminates the loop.
func TestReverseRelay_DoneReturns(t *testing.T) {
	relaySock := &scriptedPacketConn{}
	relay := &UDPRelay{relay: relaySock, done: make(chan struct{})}
	close(relay.done)

	outbound := &scriptedPacketConn{reads: []readEvent{
		{err: timeoutError{}},
		{data: []byte("PONG"), addr: &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 53}},
	}}
	relay.reverseRelay(&natEntry{conn: outbound}, natKey(testClientAddr(), "1.2.3.4:53"), testClientAddr(), "1.2.3.4:53")

	if relaySock.writeCount() != 0 {
		t.Errorf("writes = %d, want 0 (relay closed)", relaySock.writeCount())
	}
}

// TestUDPRelay_NATKeyedByClientAddr verifies two source ports on one association
// get independent flows for the same target. Keying on the target alone would
// share a single entry, and the reverse relay would deliver every response to
// whichever source port opened the flow first.
func TestUDPRelay_NATKeyedByClientAddr(t *testing.T) {
	relay, err := newUDPRelayWithListener(
		net.ParseIP("127.0.0.1"),
		nil,
		newUDPResourceLimiter(4, 4),
		newUDPResourceLimiter(8, 8),
		8,
		func(_, _ string) (net.PacketConn, error) { return &scriptedPacketConn{}, nil },
	)
	if err != nil {
		t.Fatalf("newUDPRelayWithListener() error = %v", err)
	}
	defer relay.Close()

	relay.getRoute = func(host string) (transport.Transport, error) {
		return &targetAwareTransport{
			conn:   newBlockingPacketConn(),
			target: testPacketAddr(net.JoinHostPort(host, "53")),
		}, nil
	}

	const target = "127.0.0.1:53"
	clientA := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1111}
	clientB := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2222}

	entryA, err := relay.getOrCreateNAT(target, clientA)
	if err != nil {
		t.Fatalf("getOrCreateNAT(clientA) error = %v", err)
	}
	entryB, err := relay.getOrCreateNAT(target, clientB)
	if err != nil {
		t.Fatalf("getOrCreateNAT(clientB) error = %v", err)
	}
	if entryA == entryB {
		t.Fatal("both client ports share a NAT entry; responses would go to the wrong port")
	}

	// The same client port must still reuse its own entry.
	again, err := relay.getOrCreateNAT(target, clientA)
	if err != nil {
		t.Fatalf("getOrCreateNAT(clientA) repeat error = %v", err)
	}
	if again != entryA {
		t.Error("same client port did not reuse its NAT entry")
	}
}

// TestReverseRelay_DropsSpoofedSource verifies a datagram whose source is not the
// flow's target is dropped rather than relayed to the client tagged with the
// sender's address.
func TestReverseRelay_DropsSpoofedSource(t *testing.T) {
	relaySock := &scriptedPacketConn{}
	relay := &UDPRelay{relay: relaySock, done: make(chan struct{})}

	target := &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 53}
	outbound := &scriptedPacketConn{reads: []readEvent{
		{data: []byte("SPOOF"), addr: &net.UDPAddr{IP: net.ParseIP("6.6.6.6"), Port: 53}},
		{data: []byte("SPOOF"), addr: &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 9999}},
		{data: []byte("PONG"), addr: target},
	}}
	entry := &natEntry{conn: outbound, targetAddr: target}

	relay.reverseRelay(entry, natKey(testClientAddr(), "1.2.3.4:53"), testClientAddr(), "1.2.3.4:53")

	if relaySock.writeCount() != 1 {
		t.Fatalf("writes to client = %d, want 1 (spoofed datagrams must be dropped)", relaySock.writeCount())
	}
	got := relaySock.writes[0]
	hdr, err := ParseUDPHeader(got)
	if err != nil {
		t.Fatalf("relayed datagram header parse: %v", err)
	}
	if string(got[hdr.DataOffset:]) != "PONG" {
		t.Errorf("relayed payload = %q, want PONG", got[hdr.DataOffset:])
	}
}

// TestSourceMatchesTarget covers the filter's edge cases, including the domain
// target used by proxied egress where no IP comparison is possible.
func TestSourceMatchesTarget(t *testing.T) {
	target := &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 53}
	tests := []struct {
		name   string
		src    net.Addr
		target net.Addr
		want   bool
	}{
		{"exact match", &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 53}, target, true},
		{"wrong ip", &net.UDPAddr{IP: net.ParseIP("6.6.6.6"), Port: 53}, target, false},
		{"wrong port", &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 9999}, target, false},
		{"v4 in v6 form", &net.UDPAddr{IP: net.ParseIP("::ffff:1.2.3.4"), Port: 53}, target, true},
		{"domain target accepted", &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 53}, testPacketAddr("example.com:53"), true},
		{"domain source accepted", testPacketAddr("example.com:53"), target, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourceMatchesTarget(tt.src, tt.target); got != tt.want {
				t.Errorf("sourceMatchesTarget() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestUDPSettings_Disable verifies the config kill switch refuses new relays so
// handleAssociate can report "command not supported" and clients fall back to TCP.
func TestUDPSettings_Disable(t *testing.T) {
	t.Cleanup(func() { SetUDPSettings(UDPSettings{}) })

	SetUDPSettings(UDPSettings{Disable: true})
	if !UDPDisabled() {
		t.Fatal("UDPDisabled() = false after Disable")
	}
	if _, err := NewUDPRelay(nil, nil); !errors.Is(err, ErrUDPDisabled) {
		t.Fatalf("NewUDPRelay() error = %v, want ErrUDPDisabled", err)
	}

	SetUDPSettings(UDPSettings{})
	if UDPDisabled() {
		t.Fatal("UDPDisabled() = true after reset")
	}
	relay, err := NewUDPRelay(nil, nil)
	if err != nil {
		t.Fatalf("NewUDPRelay() after reset error = %v", err)
	}
	relay.Close()
}

// TestUDPSettings_LimitsApplied verifies configured limits replace the defaults
// and that zero values fall back to them.
func TestUDPSettings_LimitsApplied(t *testing.T) {
	t.Cleanup(func() { SetUDPSettings(UDPSettings{}) })

	SetUDPSettings(UDPSettings{MaxAssociations: 1, MaxAssociationsPerClient: 1, MaxNATEntries: 3})
	if _, _, maxNAT := udpLimiters(); maxNAT != 3 {
		t.Errorf("configured maxNAT = %d, want 3", maxNAT)
	}

	first, err := NewUDPRelay(net.ParseIP("127.0.0.1"), nil)
	if err != nil {
		t.Fatalf("first NewUDPRelay() error = %v", err)
	}
	defer first.Close()
	if _, err := NewUDPRelay(net.ParseIP("127.0.0.1"), nil); !errors.Is(err, ErrUDPAssociationLimit) {
		t.Fatalf("second NewUDPRelay() error = %v, want ErrUDPAssociationLimit", err)
	}

	SetUDPSettings(UDPSettings{})
	if _, _, maxNAT := udpLimiters(); maxNAT != defaultMaxNATEntries {
		t.Errorf("reset maxNAT = %d, want %d", maxNAT, defaultMaxNATEntries)
	}
}

// TestRelayBindAddress verifies the relay binds where the client reached us
// rather than on a dual-stack wildcard across every interface.
//
// The preferIPv4 cases matter: that setting governs egress to proxied
// destinations, not this client-facing socket. Honoring it here would bind an
// IPv4 socket for a client that reached us over IPv6, while the SOCKS reply
// still advertises the IPv6 address — an association the client cannot use.
func TestRelayBindAddress(t *testing.T) {
	tests := []struct {
		name        string
		localIP     net.IP
		preferIPv4  bool
		wantNetwork string
		wantAddr    string
	}{
		{"ipv4 local", net.ParseIP("192.0.2.10"), false, "udp4", "192.0.2.10:0"},
		{"ipv6 local", net.ParseIP("2001:db8::1"), false, "udp6", "[2001:db8::1]:0"},
		{"ipv6 local with ipv6 egress disabled", net.ParseIP("2001:db8::1"), true, "udp6", "[2001:db8::1]:0"},
		{"ipv4 local with ipv6 egress disabled", net.ParseIP("192.0.2.10"), true, "udp4", "192.0.2.10:0"},
		// A wildcard bind would report an unspecified BND.ADDR, which the client
		// cannot send datagrams to; loopback is both usable and narrower.
		{"unspecified falls back to loopback", net.IPv4zero, false, "udp4", "127.0.0.1:0"},
		{"nil falls back to loopback", nil, false, "udp4", "127.0.0.1:0"},
		{"nil falls back to loopback with ipv6 egress disabled", nil, true, "udp4", "127.0.0.1:0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.preferIPv4 {
				transport.DisableIPv6()
				t.Cleanup(transport.EnableIPv6)
			}
			network, addr := relayBindAddress(tt.localIP)
			if network != tt.wantNetwork || addr != tt.wantAddr {
				t.Errorf("relayBindAddress() = %s/%s, want %s/%s", network, addr, tt.wantNetwork, tt.wantAddr)
			}
		})
	}
}

// TestRelayBindAddressReachableFromClient is an end-to-end check that the
// address advertised to the client is actually one the relay socket receives on.
// A family mismatch between bind and advertised address would silently produce
// an association that drops every datagram.
func TestRelayBindAddressReachableFromClient(t *testing.T) {
	transport.DisableIPv6()
	t.Cleanup(transport.EnableIPv6)

	for _, localIP := range []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")} {
		t.Run(localIP.String(), func(t *testing.T) {
			network, bindAddr := relayBindAddress(localIP)
			pc, err := net.ListenPacket(network, bindAddr)
			if err != nil {
				t.Skipf("cannot bind %s/%s on this host: %v", network, bindAddr, err)
			}
			defer pc.Close()

			client, err := net.ListenPacket(network, "")
			if err != nil {
				t.Fatalf("client listen: %v", err)
			}
			defer client.Close()

			if _, err := client.WriteTo([]byte("ping"), pc.LocalAddr()); err != nil {
				t.Fatalf("client write to advertised addr %s: %v", pc.LocalAddr(), err)
			}
			_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
			buf := make([]byte, 16)
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				t.Fatalf("relay did not receive on its advertised address %s: %v", pc.LocalAddr(), err)
			}
			if string(buf[:n]) != "ping" {
				t.Errorf("relay read %q, want ping", buf[:n])
			}
		})
	}
}

// TestNATKey verifies flow identity includes the client address, and degrades
// safely when the client address is unknown.
func TestNATKey(t *testing.T) {
	clientA := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1111}
	clientB := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2222}
	const target = "1.2.3.4:53"

	if natKey(clientA, target) == natKey(clientB, target) {
		t.Error("different client ports produced the same NAT key")
	}
	if natKey(clientA, target) != natKey(clientA, target) {
		t.Error("NAT key is not stable for the same client and target")
	}
	if natKey(clientA, target) == natKey(clientA, "1.2.3.4:54") {
		t.Error("different targets produced the same NAT key")
	}
	if got := natKey(nil, target); got != target {
		t.Errorf("natKey(nil, %q) = %q, want %q", target, got, target)
	}
}
