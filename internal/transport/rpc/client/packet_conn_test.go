package client

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"google.golang.org/grpc"
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
		Status: proto.ProxyStatus_Session,
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

func TestStreamPacketConn_ReadFrom_SkipsAcceptedControl(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &mockProxyClient{
		recvChan: make(chan *proto.ProxyDST, 2),
		ctx:      ctx,
	}
	conn := NewStreamPacketConn(ctx, m, cancel, "example.com:53")

	m.recvChan <- &proto.ProxyDST{
		Status: proto.ProxyStatus_Accepted,
		HeaderOrPayload: &proto.ProxyDST_Header{
			Header: &proto.ProxyDST_ProxyHeader{Addr: "127.0.0.1:12345"},
		},
	}
	m.recvChan <- &proto.ProxyDST{
		Status: proto.ProxyStatus_Session,
		HeaderOrPayload: &proto.ProxyDST_Payload{
			Payload: []byte("payload"),
		},
	}

	buf := make([]byte, 1024)
	n, addr, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom() error = %v", err)
	}
	if string(buf[:n]) != "payload" {
		t.Errorf("ReadFrom() payload = %q, want payload", buf[:n])
	}
	if addr.String() != "example.com:53" {
		t.Errorf("ReadFrom() addr = %v, want example.com:53", addr)
	}
	if _, ok := addr.(*net.UDPAddr); ok {
		t.Errorf("ReadFrom() domain addr returned *net.UDPAddr; want generic packet addr")
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
	// A read deadline must surface as os.ErrDeadlineExceeded, the standard
	// net.Conn timeout sentinel (implements net.Error with Timeout() == true).
	if !errors.Is(err, os.ErrDeadlineExceeded) && !errors.Is(err, net.ErrClosed) {
		t.Errorf("ReadFrom() expected os.ErrDeadlineExceeded or ErrClosed, got %v", err)
	}
	if ne, ok := errors.AsType[net.Error](err); ok && !ne.Timeout() {
		t.Errorf("ReadFrom() deadline error should report Timeout() == true, got %v", err)
	}
}

// TestStreamPacketConn_DeadlineSetWhileBlocked verifies that a deadline set
// AFTER a ReadFrom is already blocked still unblocks the in-flight read — the
// key property of the pipeDeadline implementation.
func TestStreamPacketConn_DeadlineSetWhileBlocked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &mockProxyClient{
		recvChan: make(chan *proto.ProxyDST),
		ctx:      ctx,
	}
	conn := NewStreamPacketConn(ctx, m, cancel, "8.8.8.8:53")

	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 1024)
		_, _, err := conn.ReadFrom(buf)
		errCh <- err
	}()

	// Let the read block with no deadline, then set one.
	time.Sleep(50 * time.Millisecond)
	if err := conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("blocked ReadFrom expected os.ErrDeadlineExceeded, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadFrom did not unblock after deadline was set while blocked")
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

// mockProxyClient2 implements proto.ProxyClient (the stub that opens streams),
// returning a configurable stream so Client.DialPacket can be tested in
// isolation.
type mockProxyClient2 struct {
	proxyErr   error            // error returned by Proxy()
	blockSend  bool             // if true, the stream's Send blocks until its ctx is canceled
	lastStream *mockProxyClient // the most recently opened stream (for inspecting the handshake)
}

func (m *mockProxyClient2) Proxy(ctx context.Context, _ ...grpc.CallOption) (grpc.BidiStreamingClient[proto.ProxySRC, proto.ProxyDST], error) {
	if m.proxyErr != nil {
		return nil, m.proxyErr
	}
	stream := &mockProxyClient{
		ctx:      ctx,
		recvChan: make(chan *proto.ProxyDST),
	}
	if !m.blockSend {
		// Buffered so the handshake Send completes immediately.
		stream.sendChan = make(chan *proto.ProxySRC, 1)
	}
	// When blockSend is true, sendChan stays nil: Send blocks on the nil-channel
	// select until ctx is canceled (mirroring a stalled stream).
	m.lastStream = stream
	return stream, nil
}

func (m *mockProxyClient2) DnsResolve(context.Context, *proto.DnsRequest, ...grpc.CallOption) (*proto.DnsResponse, error) {
	return nil, nil
}

func TestClient_DialPacket_Success(t *testing.T) {
	m := &mockProxyClient2{}
	c := &Client{ProxyClient: m}
	pc, err := c.DialPacket("udp", "8.8.8.8:53")
	if err != nil {
		t.Fatalf("DialPacket() error = %v", err)
	}
	defer pc.Close()
	if _, ok := pc.(*StreamPacketConn); !ok {
		t.Errorf("DialPacket() returned %T, want *StreamPacketConn", pc)
	}

	// The handshake must carry the network as the typed field, with a bare
	// address (no legacy "udp://" prefix).
	sent := <-m.lastStream.sendChan
	hdr := sent.GetHeader()
	if hdr == nil {
		t.Fatalf("handshake message has no header: %+v", sent)
	}
	if hdr.GetNetwork() != proto.Network_UDP {
		t.Errorf("handshake Network = %v, want UDP", hdr.GetNetwork())
	}
	if hdr.GetAddr() != "8.8.8.8:53" {
		t.Errorf("handshake Addr = %q, want bare %q", hdr.GetAddr(), "8.8.8.8:53")
	}
}

func TestClient_DialPacket_UnsupportedNetwork(t *testing.T) {
	c := &Client{ProxyClient: &mockProxyClient2{}}
	if _, err := c.DialPacket("tcp", "8.8.8.8:53"); err == nil {
		t.Error("DialPacket(tcp) expected error, got nil")
	}
}

func TestClient_DialPacket_ProxyError(t *testing.T) {
	c := &Client{ProxyClient: &mockProxyClient2{proxyErr: errors.New("stream open failed")}}
	if _, err := c.DialPacket("udp", "8.8.8.8:53"); err == nil {
		t.Error("DialPacket() expected error when stream creation fails, got nil")
	}
}

// TestClient_DialPacket_HandshakeTimeout verifies a stalled handshake Send is
// bounded by the dial timeout rather than blocking the caller indefinitely.
func TestClient_DialPacket_HandshakeTimeout(t *testing.T) {
	old := transport.GetDialTimeout()
	transport.SetDialTimeout(100 * time.Millisecond)
	defer transport.SetDialTimeout(old)

	c := &Client{ProxyClient: &mockProxyClient2{blockSend: true}}

	start := time.Now()
	_, err := c.DialPacket("udp", "8.8.8.8:53")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("DialPacket() expected handshake timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("DialPacket() error = %v, want a timeout error", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("DialPacket() took %s; timeout was not enforced", elapsed)
	}
}

func TestStreamPacketConn_ReadFrom_StreamEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &mockProxyClient{ctx: ctx, err: io.EOF}
	conn := NewStreamPacketConn(ctx, m, cancel, "8.8.8.8:53")

	_, _, err := conn.ReadFrom(make([]byte, 64))
	if !errors.Is(err, net.ErrClosed) {
		t.Errorf("ReadFrom() on stream EOF = %v, want net.ErrClosed", err)
	}
}

func TestStreamPacketConn_ReadFrom_ErrorStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &mockProxyClient{ctx: ctx, recvChan: make(chan *proto.ProxyDST, 1)}
	conn := NewStreamPacketConn(ctx, m, cancel, "8.8.8.8:53")

	m.recvChan <- &proto.ProxyDST{Status: proto.ProxyStatus_Error}
	_, _, err := conn.ReadFrom(make([]byte, 64))
	if err == nil || errors.Is(err, net.ErrClosed) {
		t.Errorf("ReadFrom() on error status = %v, want a non-nil, non-ErrClosed error", err)
	}
}

func TestStreamPacketConn_ReadFrom_EOFStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &mockProxyClient{ctx: ctx, recvChan: make(chan *proto.ProxyDST, 1)}
	conn := NewStreamPacketConn(ctx, m, cancel, "8.8.8.8:53")

	m.recvChan <- &proto.ProxyDST{Status: proto.ProxyStatus_EOF}
	_, _, err := conn.ReadFrom(make([]byte, 64))
	if !errors.Is(err, net.ErrClosed) {
		t.Errorf("ReadFrom() on EOF status = %v, want net.ErrClosed", err)
	}
}

func TestStreamPacketConn_ReadFrom_ShortBuffer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &mockProxyClient{ctx: ctx, recvChan: make(chan *proto.ProxyDST, 1)}
	conn := NewStreamPacketConn(ctx, m, cancel, "8.8.8.8:53")

	m.recvChan <- &proto.ProxyDST{
		Status:          proto.ProxyStatus_Session,
		HeaderOrPayload: &proto.ProxyDST_Payload{Payload: []byte("0123456789")},
	}
	small := make([]byte, 4)
	n, _, err := conn.ReadFrom(small)
	if n != 4 || !errors.Is(err, io.ErrShortBuffer) {
		t.Errorf("ReadFrom() short buffer = (%d, %v), want (4, io.ErrShortBuffer)", n, err)
	}
}

func TestStreamPacketConn_ReadFrom_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &mockProxyClient{ctx: ctx, recvChan: make(chan *proto.ProxyDST)}
	conn := NewStreamPacketConn(ctx, m, cancel, "8.8.8.8:53")

	cancel() // closes the conn's context
	_, _, err := conn.ReadFrom(make([]byte, 64))
	if !errors.Is(err, net.ErrClosed) {
		t.Errorf("ReadFrom() on canceled context = %v, want net.ErrClosed", err)
	}
}
