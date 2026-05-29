package client

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"google.golang.org/grpc/metadata"
)

// mockProxyClient implements proto.Proxy_ProxyClient
type mockProxyClient struct {
	recvChan chan *proto.ProxyDST
	sendChan chan *proto.ProxySRC
	ctx      context.Context
	err      error
}

func (m *mockProxyClient) Send(src *proto.ProxySRC) error {
	if m.err != nil {
		return m.err
	}
	select {
	case m.sendChan <- src:
		return nil
	case <-m.ctx.Done():
		return m.ctx.Err()
	}
}

func (m *mockProxyClient) Recv() (*proto.ProxyDST, error) {
	if m.err != nil {
		return nil, m.err
	}
	select {
	case dst, ok := <-m.recvChan:
		if !ok {
			return nil, io.EOF
		}
		return dst, nil
	case <-m.ctx.Done():
		return nil, m.ctx.Err()
	}
}

func (m *mockProxyClient) Header() (metadata.MD, error) { return nil, nil }
func (m *mockProxyClient) Trailer() metadata.MD         { return nil }
func (m *mockProxyClient) CloseSend() error             { return nil }
func (m *mockProxyClient) Context() context.Context     { return context.Background() }
func (m *mockProxyClient) RecvMsg(m_ interface{}) error { return nil }
func (m *mockProxyClient) SendMsg(m_ interface{}) error { return nil }

func TestStreamPacketConn_ReadFrom(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &mockProxyClient{
		recvChan: make(chan *proto.ProxyDST, 1),
		ctx:      ctx,
	}
	conn := NewStreamPacketConn(ctx, m, cancel, "8.8.8.8:53")

	payload := []byte("hello udp")
	m.recvChan <- &proto.ProxyDST{
		Status: proto.ProxyStatus_Accepted,
		HeaderOrPayload: &proto.ProxyDST_Payload{
			Payload: payload,
		},
	}

	buf := make([]byte, 1024)
	n, addr, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom() error = %v", err)
	}

	if n != len(payload) {
		t.Errorf("ReadFrom() n = %v, want %v", n, len(payload))
	}
	if string(buf[:n]) != string(payload) {
		t.Errorf("ReadFrom() buf = %v, want %v", string(buf[:n]), string(payload))
	}

	// Verify targetAddr parsed correctly
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		t.Errorf("addr is not *net.UDPAddr")
	} else if udpAddr.IP.String() != "8.8.8.8" || udpAddr.Port != 53 {
		t.Errorf("addr = %v, want 8.8.8.8:53", addr)
	}
}

func TestStreamPacketConn_WriteTo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &mockProxyClient{
		sendChan: make(chan *proto.ProxySRC, 1),
		ctx:      ctx,
	}
	conn := NewStreamPacketConn(ctx, m, cancel, "8.8.8.8:53")

	payload := []byte("hello udp")
	n, err := conn.WriteTo(payload, nil)
	if err != nil {
		t.Fatalf("WriteTo() error = %v", err)
	}
	if n != len(payload) {
		t.Errorf("WriteTo() n = %v, want %v", n, len(payload))
	}

	src := <-m.sendChan
	p := src.GetPayload()
	if string(p) != string(payload) {
		t.Errorf("WriteTo() sent payload = %v, want %v", string(p), string(payload))
	}
}

func TestStreamPacketConn_Deadlines(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &mockProxyClient{
		recvChan: make(chan *proto.ProxyDST),
		ctx:      ctx,
	}
	conn := NewStreamPacketConn(ctx, m, cancel, "8.8.8.8:53")

	err := conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}

	buf := make([]byte, 1024)
	_, _, err = conn.ReadFrom(buf)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		if err == net.ErrClosed {
			// context cancellation returns ErrClosed by our translation
		} else {
			t.Errorf("ReadFrom() expected DeadlineExceeded or ErrClosed, got %v", err)
		}
	}
}

func TestStreamPacketConn_Close(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &mockProxyClient{
		ctx: ctx,
	}
	conn := NewStreamPacketConn(ctx, m, cancel, "8.8.8.8:53")

	if err := conn.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}

	// Context should be canceled
	if ctx.Err() == nil {
		t.Errorf("Context was not canceled on Close()")
	}
}
