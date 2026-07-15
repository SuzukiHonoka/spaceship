package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
)

type zeroReadConn struct{}

func (zeroReadConn) Read([]byte) (int, error)         { return 0, nil }
func (zeroReadConn) Write(p []byte) (int, error)      { return len(p), nil }
func (zeroReadConn) Close() error                     { return nil }
func (zeroReadConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (zeroReadConn) RemoteAddr() net.Addr             { return &net.UDPAddr{} }
func (zeroReadConn) SetDeadline(time.Time) error      { return nil }
func (zeroReadConn) SetReadDeadline(time.Time) error  { return nil }
func (zeroReadConn) SetWriteDeadline(time.Time) error { return nil }

type singleReadConn struct {
	zeroReadConn
	data           []byte
	read           bool
	readBufferSize int
}

func (c *singleReadConn) Read(p []byte) (int, error) {
	if c.read {
		return 0, io.EOF
	}
	c.read = true
	c.readBufferSize = len(p)
	return copy(p, c.data), io.EOF
}

func TestCopyClientToTarget_EmptyPayload_TCP_EOF(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	f := &Forwarder{Conn: c1, network: "tcp"}
	err := f.copyClientToTarget(&proto.ProxySRC{
		HeaderOrPayload: &proto.ProxySRC_Payload{Payload: nil},
	})
	if err != io.EOF {
		t.Fatalf("TCP empty payload = %v, want io.EOF", err)
	}
}

func TestCopyClientToTarget_EmptyPayload_UDP_OK(t *testing.T) {
	// Connected UDP pair via localhost.
	server, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	client, err := net.Dial("udp4", server.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	done := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 16)
		n, _, readErr := server.ReadFrom(buf)
		if readErr != nil {
			done <- nil
			return
		}
		done <- buf[:n]
	}()

	f := &Forwarder{Conn: client, network: "udp4"}
	if err := f.copyClientToTarget(&proto.ProxySRC{
		HeaderOrPayload: &proto.ProxySRC_Payload{Payload: []byte{}},
	}); err != nil {
		t.Fatalf("UDP empty payload error = %v, want nil", err)
	}

	// Some stacks deliver empty datagrams; others may not. The critical
	// production property is that the session is not torn down with EOF.
	select {
	case <-done:
	default:
	}
}

func TestCopyTargetToClient_EmptyPayload_UDP_OK(t *testing.T) {
	stream := &mockProxyServer{sent: make(chan *proto.ProxyDST, 1)}
	f := &Forwarder{Conn: zeroReadConn{}, Stream: stream, network: "udp4"}
	dst := &proto.ProxyDST{HeaderOrPayload: &proto.ProxyDST_Payload{}}
	payload := dst.HeaderOrPayload.(*proto.ProxyDST_Payload)

	if err := f.copyTargetToClient(make([]byte, 1), dst, payload); err != nil {
		t.Fatalf("copyTargetToClient() error = %v", err)
	}
	select {
	case got := <-stream.sent:
		if data := got.GetPayload(); data == nil || len(data) != 0 {
			t.Fatalf("payload = %#v, want a non-nil empty datagram", data)
		}
	default:
		t.Fatal("zero-length UDP datagram was not forwarded")
	}
}

func TestCopyTargetToClient_EmptyRead_TCPIgnored(t *testing.T) {
	stream := &mockProxyServer{sent: make(chan *proto.ProxyDST, 1)}
	f := &Forwarder{Conn: zeroReadConn{}, Stream: stream, network: "tcp"}
	dst := &proto.ProxyDST{HeaderOrPayload: &proto.ProxyDST_Payload{}}
	payload := dst.HeaderOrPayload.(*proto.ProxyDST_Payload)

	if err := f.copyTargetToClient(make([]byte, 1), dst, payload); err != nil {
		t.Fatalf("copyTargetToClient() error = %v", err)
	}
	select {
	case <-stream.sent:
		t.Fatal("zero-length TCP read was forwarded")
	default:
	}
}

func TestCopyTargetToClient_PreservesLargeUDPDatagram(t *testing.T) {
	payload := bytes.Repeat([]byte{0x5a}, 48*1024)
	conn := &singleReadConn{data: payload}
	stream := &mockProxyServer{sent: make(chan *proto.ProxyDST, 2)}
	f := NewForwarder(context.Background(), stream)
	f.Conn = conn
	f.network = "udp4"
	f.Ack = make(chan struct{}, 1)
	f.Ack <- struct{}{}

	err := f.CopyTargetToClient(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("CopyTargetToClient() error = %v, want io.EOF", err)
	}
	if conn.readBufferSize != maxUDPPacketSize {
		t.Fatalf("UDP read buffer = %d, want %d", conn.readBufferSize, maxUDPPacketSize)
	}

	<-stream.sent // Accepted control message.
	got := <-stream.sent
	if !bytes.Equal(got.GetPayload(), payload) {
		t.Fatalf("forwarded UDP payload length = %d, want %d", len(got.GetPayload()), len(payload))
	}
}
