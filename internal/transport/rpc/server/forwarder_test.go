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

func TestParseProxyTarget(t *testing.T) {
	defaultNet := transport.GetNetwork()
	tests := []struct {
		name        string
		raw         string
		wantNetwork string
		wantAddr    string
	}{
		{"udp scheme", "udp://8.8.8.8:53", "udp", "8.8.8.8:53"},
		{"tcp scheme", "tcp://example.com:443", "tcp", "example.com:443"},
		{"no scheme uses default", "example.com:443", defaultNet, "example.com:443"},
		{"ipv6 udp", "udp://[::1]:53", "udp", "[::1]:53"},
		{"empty", "", defaultNet, ""},
		{"scheme-like host not matched", "udps://x:1", defaultNet, "udps://x:1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotNet, gotAddr := parseProxyTarget(tt.raw)
			if gotNet != tt.wantNetwork || gotAddr != tt.wantAddr {
				t.Errorf("parseProxyTarget(%q) = (%q, %q), want (%q, %q)",
					tt.raw, gotNet, gotAddr, tt.wantNetwork, tt.wantAddr)
			}
		})
	}
}
