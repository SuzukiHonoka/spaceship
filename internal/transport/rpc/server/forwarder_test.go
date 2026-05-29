package server

import (
	"context"
	"net"
	"testing"

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
func (m *mockProxyServer) SetTrailer(metadata.MD)      {}
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
