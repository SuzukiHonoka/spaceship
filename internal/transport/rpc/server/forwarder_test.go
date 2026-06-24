package server

import (
	"context"
	"net"
	"testing"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"google.golang.org/grpc/metadata"
)

type mockProxyServer struct {
	ctx      context.Context
	sent     chan *proto.ProxyDST
	received chan *proto.ProxySRC
}

func (m *mockProxyServer) Send(dst *proto.ProxyDST) error {
	m.sent <- dst
	return nil
}

func (m *mockProxyServer) Recv() (*proto.ProxySRC, error) {
	return <-m.received, nil
}

func (m *mockProxyServer) SetHeader(metadata.MD) error  { return nil }
func (m *mockProxyServer) SendHeader(metadata.MD) error { return nil }
func (m *mockProxyServer) SetTrailer(metadata.MD)       {}
func (m *mockProxyServer) Context() context.Context     { return m.ctx }
func (m *mockProxyServer) SendMsg(interface{}) error    { return nil }
func (m *mockProxyServer) RecvMsg(interface{}) error    { return nil }

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
	defer c2.Close()
	f.Conn = c1
	if err := f.Close(); err != nil {
		t.Errorf("Close conn error: %v", err)
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

func TestResolveTarget(t *testing.T) {
	defaultNet := transport.GetNetwork()
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
