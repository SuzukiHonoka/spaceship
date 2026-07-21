package client

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const poolTestUUID = "pool-integration-user"

// stubProxy is a minimal gRPC Proxy implementation used only by client package
// tests so we can exercise Init/New/Proxy/DialPacket without importing the
// real server package (which would create an import cycle via router).
type stubProxy struct {
	proto.UnimplementedProxyServer
}

func (s *stubProxy) Proxy(stream proto.Proxy_ProxyServer) error {
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	header := msg.GetHeader()
	if header == nil {
		return io.ErrUnexpectedEOF
	}
	if err := stream.Send(&proto.ProxyDST{
		Status: proto.ProxyStatus_Accepted,
		HeaderOrPayload: &proto.ProxyDST_Header{
			Header: &proto.ProxyDST_ProxyHeader{Addr: "127.0.0.1:0"},
		},
	}); err != nil {
		return err
	}
	switch header.GetNetwork() {
	case proto.Network_UDP:
		for {
			in, err := stream.Recv()
			if err != nil {
				return nil
			}
			if p := in.GetPayload(); len(p) > 0 {
				if err := stream.Send(&proto.ProxyDST{
					Status:          proto.ProxyStatus_Session,
					HeaderOrPayload: &proto.ProxyDST_Payload{Payload: p},
				}); err != nil {
					return err
				}
			}
		}
	default:
		// TCP: open a local echo and bridge, or just echo stream payloads.
		for {
			in, err := stream.Recv()
			if err != nil {
				_ = stream.Send(&proto.ProxyDST{Status: proto.ProxyStatus_EOF})
				return nil
			}
			if p := in.GetPayload(); len(p) > 0 {
				if err := stream.Send(&proto.ProxyDST{
					Status:          proto.ProxyStatus_Session,
					HeaderOrPayload: &proto.ProxyDST_Payload{Payload: p},
				}); err != nil {
					return err
				}
			}
		}
	}
}

func (s *stubProxy) DnsResolve(_ context.Context, req *proto.DnsRequest) (*proto.DnsResponse, error) {
	return &proto.DnsResponse{}, nil
}

func startStubProxy(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Accept any UUID via a permissive interceptor.
	s := grpc.NewServer(
		append(rpc.ServerOptions(),
			grpc.Creds(insecure.NewCredentials()),
			grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
				return handler(ctx, req)
			}),
			grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
				return handler(srv, ss)
			}),
		)...,
	)
	proto.RegisterProxyServer(s, &stubProxy{})
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(func() {
		s.Stop()
		_ = ln.Close()
	})
	return ln.Addr().String()
}

func TestPoolInitProxyAndStatus(t *testing.T) {
	addr := startStubProxy(t)

	if s := GetConnectionStatus(); s != "Connection pool not initialized" {
		t.Fatalf("status before init = %q", s)
	}
	if total, active, load := GetConnectionSummary(); total != 0 || active != 0 || load != 0 {
		t.Fatalf("summary before init = %d %d %d", total, active, load)
	}
	if d := GetConnectionDetails(); d != nil {
		t.Fatalf("details before init = %v", d)
	}
	LogConnectionStatus()

	if _, err := New(); err == nil {
		t.Fatal("New() succeeded before Init")
	}

	SetUUID(poolTestUUID)
	if err := Init(addr, "", false, 2, nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(Destroy)

	status := GetConnectionStatus()
	if status == "" || status == "Connection pool not initialized" {
		t.Fatalf("status after init = %q", status)
	}
	total, _, _ := GetConnectionSummary()
	if total != 2 {
		t.Fatalf("pool total = %d, want 2", total)
	}
	details := GetConnectionDetails()
	if len(details) != 2 {
		t.Fatalf("details len = %d, want 2", len(details))
	}
	LogConnectionStatus()

	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.String() != TransportName {
		t.Fatalf("String = %q", c.String())
	}
	if _, err := c.Dial("tcp", "127.0.0.1:1"); err == nil {
		t.Fatal("Dial should not be implemented")
	}

	localAddr := make(chan string, 1)
	var out bytes.Buffer
	payload := []byte("pool-proxy-payload")
	src := bytes.NewReader(payload)

	proxyErr := make(chan error, 1)
	go func() {
		proxyErr <- c.Proxy(context.Background(), "127.0.0.1:9", localAddr, &out, src)
	}()

	select {
	case la := <-localAddr:
		if la == "" {
			// Stub accepts without a real local bind; forwarder may still signal.
			t.Logf("local addr from Proxy: %q", la)
		}
	case err := <-proxyErr:
		// Proxy may finish before we observe localAddr if channel closed early.
		if err != nil {
			t.Fatalf("Proxy early error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Proxy handshake timeout")
	}

	select {
	case err := <-proxyErr:
		if err != nil {
			t.Fatalf("Proxy: %v", err)
		}
	case <-time.After(5 * time.Second):
		// drain if already taken
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	c2, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	pc, err := c2.DialPacket("udp", "127.0.0.1:53")
	if err != nil {
		t.Fatalf("DialPacket: %v", err)
	}
	defer pc.Close()

	_ = pc.LocalAddr()
	_ = pc.SetDeadline(time.Now().Add(3 * time.Second))
	_ = pc.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_ = pc.SetReadDeadline(time.Now().Add(3 * time.Second))

	msg := []byte("udp-pool")
	if _, err := pc.WriteTo(msg, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53}); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	buf := make([]byte, 64)
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if !bytes.Equal(buf[:n], msg) {
		t.Fatalf("udp echo = %q, want %q", buf[:n], msg)
	}

	if _, err := c2.DialPacket("tcp", "127.0.0.1:1"); err == nil {
		t.Fatal("DialPacket(tcp) should fail")
	}

	Destroy()
	Destroy()
}

func TestInitTLSMissingCA(t *testing.T) {
	err := Init("127.0.0.1:1", "example.com", true, 1, []string{"/no/such/ca.pem"})
	if err == nil {
		Destroy()
		t.Fatal("Init with missing CA succeeded")
	}
}

func TestNewParams(t *testing.T) {
	p := NewParams("127.0.0.1:9")
	if p.Addr != "127.0.0.1:9" {
		t.Fatalf("Addr = %s", p.Addr)
	}
}

func TestGrpcStateHelpers(t *testing.T) {
	// Exercise string helpers via empty wrappers status paths.
	var empty ConnWrappers
	if got := empty.GetDetailedStatus(); got != "No connections" {
		t.Fatalf("empty status = %q", got)
	}
	empty.LogStatus()
	if d := empty.GetConnectionDetails(); len(d) != 0 {
		t.Fatalf("details = %v", d)
	}
}

