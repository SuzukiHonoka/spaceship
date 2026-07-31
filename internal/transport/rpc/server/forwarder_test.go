package server

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"google.golang.org/grpc/metadata"
)

type mockProxyServer struct {
	ctx           context.Context
	sent          chan *proto.ProxyDST
	received      chan *proto.ProxySRC
	recvStarted   chan struct{}
	recvStartOnce sync.Once
}

func (m *mockProxyServer) Send(dst *proto.ProxyDST) error {
	m.sent <- dst
	return nil
}

func (m *mockProxyServer) Recv() (*proto.ProxySRC, error) {
	m.recvStartOnce.Do(func() {
		if m.recvStarted != nil {
			close(m.recvStarted)
		}
	})
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case message, ok := <-m.received:
		if !ok {
			return nil, io.EOF
		}
		return message, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *mockProxyServer) SetHeader(metadata.MD) error  { return nil }
func (m *mockProxyServer) SendHeader(metadata.MD) error { return nil }
func (m *mockProxyServer) SetTrailer(metadata.MD)       {}
func (m *mockProxyServer) Context() context.Context     { return m.ctx }
func (m *mockProxyServer) SendMsg(interface{}) error    { return nil }
func (m *mockProxyServer) RecvMsg(interface{}) error    { return nil }

type closeTrackingRoute struct {
	closeCount int
	closeErr   error
}

func (*closeTrackingRoute) String() string {
	return "close-tracking"
}

func (*closeTrackingRoute) Dial(string, string) (net.Conn, error) {
	return nil, errors.New("not used")
}

func (r *closeTrackingRoute) Close() error {
	r.closeCount++
	return r.closeErr
}

func (*closeTrackingRoute) Proxy(
	context.Context,
	string,
	chan<- string,
	io.Writer,
	io.Reader,
) error {
	return errors.New("not used")
}

var _ transport.Transport = (*closeTrackingRoute)(nil)

func TestForwarder_New(t *testing.T) {
	ctx := context.Background()
	stream := &mockProxyServer{ctx: ctx}
	f := NewForwarder(ctx, stream)
	if f == nil {
		t.Fatal("NewForwarder returned nil")
	}
	if f.Stream != stream {
		t.Errorf("Stream mismatch")
	}
}

func TestForwarder_Close(t *testing.T) {
	f := &Forwarder{}
	if err := f.Close(); err != nil {
		t.Errorf("Close nil conn error: %v", err)
	}

	c1, c2 := net.Pipe()
	defer func() { _ = c2.Close() }()
	f.Conn = c1
	if err := f.Close(); err != nil {
		t.Errorf("Close conn error: %v", err)
	}
}

func TestForwarderCloseReleasesRouteOnceAndCachesError(t *testing.T) {
	sentinel := errors.New("route close failure")
	route := &closeTrackingRoute{closeErr: sentinel}
	f := &Forwarder{route: route}

	if err := f.Close(); !errors.Is(err, sentinel) {
		t.Fatalf("first Close() error = %v, want route failure", err)
	}
	if err := f.Close(); !errors.Is(err, sentinel) {
		t.Fatalf("second Close() error = %v, want cached route failure", err)
	}
	if route.closeCount != 1 {
		t.Fatalf("route close count = %d, want 1", route.closeCount)
	}
}

func TestForwarder_CopyTargetToClient_Ack(t *testing.T) {
	ctx := context.Background()
	f := NewForwarder(ctx, &mockProxyServer{})

	// Test ack failure
	close(f.Ack)
	err := f.CopyTargetToClient(ctx)
	if err == nil {
		t.Errorf("expected error on closed ack")
	}
}

func TestForwarder_CopyTargetToClientCancellationClosesTarget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stream := &mockProxyServer{
		ctx:  ctx,
		sent: make(chan *proto.ProxyDST, 1),
	}
	target, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	f := NewForwarder(ctx, stream)
	f.Conn = target
	f.network = "tcp"
	f.Ack <- struct{}{}

	done := make(chan error, 1)
	go func() { done <- f.CopyTargetToClient(ctx) }()

	select {
	case accepted := <-stream.sent:
		if accepted.Status != proto.ProxyStatus_Accepted {
			t.Fatalf("first status = %v, want Accepted", accepted.Status)
		}
	case <-time.After(time.Second):
		t.Fatal("CopyTargetToClient did not send the accepted status")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CopyTargetToClient() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CopyTargetToClient did not unblock after cancellation")
	}
	if err := target.SetDeadline(time.Now()); err == nil {
		t.Fatal("target connection remained open after cancellation")
	}
}

func TestForwarderHandshakeUsesPrefetchedHeaderAndContextDialer(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		{MatchType: router.TypeDefault, Destination: router.EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f := NewForwarder(ctx, &mockProxyServer{ctx: ctx})
	f.firstMessage = &proto.ProxySRC{
		HeaderOrPayload: &proto.ProxySRC_Header{
			Header: &proto.ProxySRC_ProxyHeader{Addr: listener.Addr().String()},
		},
	}
	defer func() { _ = f.Close() }()

	if err := f.handshake(); err != nil {
		t.Fatal(err)
	}
	if f.Target() != listener.Addr().String() || !strings.HasPrefix(f.network, "tcp") {
		t.Fatalf("handshake target=%q network=%q", f.Target(), f.network)
	}
}

func TestForwarderHandshakeRejectsInvalidFirstMessage(t *testing.T) {
	tests := []struct {
		name    string
		message *proto.ProxySRC
	}{
		{
			name: "payload",
			message: &proto.ProxySRC{
				HeaderOrPayload: &proto.ProxySRC_Payload{Payload: []byte("not a header")},
			},
		},
		{
			name: "nil header",
			message: &proto.ProxySRC{
				HeaderOrPayload: &proto.ProxySRC_Header{},
			},
		},
		{
			name: "invalid address",
			message: &proto.ProxySRC{
				HeaderOrPayload: &proto.ProxySRC_Header{
					Header: &proto.ProxySRC_ProxyHeader{Addr: "missing-port"},
				},
			},
		},
		{
			name: "empty host",
			message: &proto.ProxySRC{
				HeaderOrPayload: &proto.ProxySRC_Header{
					Header: &proto.ProxySRC_ProxyHeader{Addr: ":443"},
				},
			},
		},
		{
			name: "empty port",
			message: &proto.ProxySRC{
				HeaderOrPayload: &proto.ProxySRC_Header{
					Header: &proto.ProxySRC_ProxyHeader{Addr: "example.com:"},
				},
			},
		},
		{
			name: "unknown network",
			message: &proto.ProxySRC{
				HeaderOrPayload: &proto.ProxySRC_Header{
					Header: &proto.ProxySRC_ProxyHeader{
						Addr:    "example.com:443",
						Network: proto.Network(99),
					},
				},
			},
		},
		{
			name: "oversized target",
			message: &proto.ProxySRC{
				HeaderOrPayload: &proto.ProxySRC_Header{
					Header: &proto.ProxySRC_ProxyHeader{
						Addr: strings.Repeat("a", maxProxyTargetLength+1),
					},
				},
			},
		},
		{
			name: "log control character",
			message: &proto.ProxySRC{
				HeaderOrPayload: &proto.ProxySRC_Header{
					Header: &proto.ProxySRC_ProxyHeader{
						Addr: "example.com\ninjected:443",
					},
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			received := make(chan *proto.ProxySRC, 1)
			received <- test.message
			f := NewForwarder(context.Background(), &mockProxyServer{
				ctx:      context.Background(),
				received: received,
			})
			if err := f.handshake(); err == nil {
				t.Fatal("handshake accepted an invalid first message")
			}
			if f.Target() != "" {
				t.Fatalf("invalid target was retained for logging: %q", f.Target())
			}
		})
	}
}

func TestContainsControlOrSpace(t *testing.T) {
	for _, invalid := range []string{"host name:443", "host\tname:443", "host\nname:443", "host\x7fname:443"} {
		if !containsControlOrSpace(invalid) {
			t.Fatalf("containsControlOrSpace(%q) = false", invalid)
		}
	}
	if containsControlOrSpace("[2001:db8::1]:443") {
		t.Fatal("containsControlOrSpace rejected a valid IPv6 target")
	}
}

func TestForwarderHandshakeDialHonorsCanceledContext(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		{MatchType: router.TypeDefault, Destination: router.EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := NewForwarder(ctx, &mockProxyServer{
		ctx:  ctx,
		sent: make(chan *proto.ProxyDST, 1),
	})
	f.firstMessage = &proto.ProxySRC{
		HeaderOrPayload: &proto.ProxySRC_Header{
			Header: &proto.ProxySRC_ProxyHeader{Addr: "192.0.2.1:443"},
		},
	}
	if err := f.handshake(); !errors.Is(err, context.Canceled) {
		t.Fatalf("handshake error = %v, want context.Canceled", err)
	}
}

func TestResolveTarget(t *testing.T) {
	transport.EnableIPv6()
	t.Cleanup(transport.EnableIPv6)

	defaultNet := transport.DialNetwork(transport.GetNetwork())
	tests := []struct {
		name        string
		header      *proto.ProxySRC_ProxyHeader
		wantNetwork string
		wantAddr    string
	}{
		{
			name:        "typed udp",
			header:      &proto.ProxySRC_ProxyHeader{Addr: "8.8.8.8:53", Network: proto.Network_UDP},
			wantNetwork: "udp", wantAddr: "8.8.8.8:53",
		},
		{
			name:        "typed tcp (zero value)",
			header:      &proto.ProxySRC_ProxyHeader{Addr: "example.com:443", Network: proto.Network_TCP},
			wantNetwork: defaultNet, wantAddr: "example.com:443",
		},
		{
			name:        "unset network defaults to tcp",
			header:      &proto.ProxySRC_ProxyHeader{Addr: "example.com:443"},
			wantNetwork: defaultNet, wantAddr: "example.com:443",
		},
		{
			name:        "legacy udp:// prefix honored",
			header:      &proto.ProxySRC_ProxyHeader{Addr: "udp://8.8.8.8:53"},
			wantNetwork: "udp", wantAddr: "8.8.8.8:53",
		},
		{
			name:        "legacy tcp:// prefix honored",
			header:      &proto.ProxySRC_ProxyHeader{Addr: "tcp://example.com:443"},
			wantNetwork: "tcp", wantAddr: "example.com:443",
		},
		{
			name:        "legacy prefix takes precedence over typed field",
			header:      &proto.ProxySRC_ProxyHeader{Addr: "udp://8.8.8.8:53", Network: proto.Network_TCP},
			wantNetwork: "udp", wantAddr: "8.8.8.8:53",
		},
		{
			name:        "scheme-like host not matched",
			header:      &proto.ProxySRC_ProxyHeader{Addr: "udps://x:1"},
			wantNetwork: defaultNet, wantAddr: "udps://x:1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotNet, gotAddr := resolveTarget(tt.header)
			if gotNet != tt.wantNetwork || gotAddr != tt.wantAddr {
				t.Errorf("resolveTarget(%+v) = (%q, %q), want (%q, %q)",
					tt.header, gotNet, gotAddr, tt.wantNetwork, tt.wantAddr)
			}
		})
	}
}

func TestResolveTarget_IPv6Disabled(t *testing.T) {
	transport.DisableIPv6()
	t.Cleanup(transport.EnableIPv6)

	gotNet, gotAddr := resolveTarget(&proto.ProxySRC_ProxyHeader{
		Addr:    "8.8.8.8:53",
		Network: proto.Network_UDP,
	})
	if gotNet != "udp4" || gotAddr != "8.8.8.8:53" {
		t.Fatalf("resolveTarget UDP with IPv6 disabled = (%q, %q), want (udp4, 8.8.8.8:53)", gotNet, gotAddr)
	}

	gotNet, _ = resolveTarget(&proto.ProxySRC_ProxyHeader{Addr: "example.com:443"})
	if gotNet != "tcp4" {
		t.Fatalf("resolveTarget TCP with IPv6 disabled = %q, want tcp4", gotNet)
	}
}

func TestIsUDPNetwork(t *testing.T) {
	for _, n := range []string{"udp", "udp4", "udp6"} {
		if !isUDPNetwork(n) {
			t.Errorf("isUDPNetwork(%q) = false, want true", n)
		}
	}
	if isUDPNetwork("tcp") {
		t.Error("isUDPNetwork(tcp) = true, want false")
	}
}
