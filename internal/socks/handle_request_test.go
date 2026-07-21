package socks

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
)

func TestHandleRequest_BindUnsupported(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, &Config{})

	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()
	defer clientSide.Close()

	errCh := make(chan error, 1)
	go func() {
		req := &Request{Command: BindCommand, DestAddr: &AddrSpec{IP: net.ParseIP("127.0.0.1"), Port: 1}}
		errCh <- s.handleRequest(req, serverSide)
	}()

	// BIND reply: VER REP RSV ATYP ADDR PORT
	head := make([]byte, 4)
	if _, err := io.ReadFull(clientSide, head); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if head[1] != commandNotSupported {
		t.Fatalf("reply code = %d, want commandNotSupported(%d)", head[1], commandNotSupported)
	}
	// drain rest of IPv4 zero address reply
	_, _ = io.ReadFull(clientSide, make([]byte, 6))

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("handleRequest(bind) error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handleRequest(bind) timed out")
	}
}

func TestHandleRequest_UnsupportedCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, &Config{})

	var buf bytes.Buffer
	req := &Request{Command: 0x99, DestAddr: &AddrSpec{IP: net.ParseIP("127.0.0.1"), Port: 1}}
	err := s.handleRequest(req, &bufWriter{&buf})
	if err == nil {
		t.Fatal("handleRequest accepted unsupported command without error")
	}
	if buf.Len() < 2 || buf.Bytes()[1] != commandNotSupported {
		t.Fatalf("reply = %v, want commandNotSupported", buf.Bytes())
	}
}

// bufWriter satisfies ConnWriter for reply-only paths.
type bufWriter struct{ *bytes.Buffer }

func (b *bufWriter) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}
}

func TestHandleConnect_NoRoute(t *testing.T) {
	// Install routes without a matching default for this host.
	if err := router.SetRoutes(router.Routes{
		{MatchType: router.TypeExact, Sources: []string{"other.example"}, Destination: router.EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = router.SetRoutes(router.Routes{
			{MatchType: router.TypeDefault, Destination: router.EgressDirect},
		})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, &Config{})

	var buf bytes.Buffer
	req := &Request{
		Command:  ConnectCommand,
		DestAddr: &AddrSpec{FQDN: "missing.example", Port: 80},
	}
	if err := s.handleConnect(ctx, &bufWriter{&buf}, req); err != nil {
		t.Fatalf("handleConnect error = %v", err)
	}
	if buf.Len() < 2 || buf.Bytes()[1] != ruleFailure {
		t.Fatalf("reply = %v, want ruleFailure", buf.Bytes())
	}
}

func TestHandleConnect_SuccessDirect(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		{MatchType: router.TypeDefault, Destination: router.EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}

	// TCP echo target.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, &Config{})

	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()
	defer clientSide.Close()

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var port uint16
	for _, ch := range portStr {
		if ch < '0' || ch > '9' {
			continue
		}
		port = port*10 + uint16(ch-'0')
	}

	// After SOCKS reply, remaining clientSide reads/writes are the tunnel.
	// handleConnect reads uploads from req.bufConn and writes downloads to conn.
	// So: reply goes to clientSide; we must drain reply from clientSide, then
	// writes on clientSide are discarded by proxy reader... Actually:
	//   route.Proxy(..., conn /*dst*/, req.bufConn /*src*/)
	// downloads: target → conn (serverSide → clientSide)
	// uploads: bufConn → target
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	req := &Request{
		Command:  ConnectCommand,
		DestAddr: &AddrSpec{IP: net.ParseIP(host), Port: port},
		bufConn:  pr,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.handleConnect(ctx, serverSide, req)
	}()

	if err := clientSide.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(clientSide, head); err != nil {
		t.Fatalf("read reply head: %v", err)
	}
	if head[1] != successReply {
		t.Fatalf("reply code = %d, want success", head[1])
	}
	if head[3] == ipv4Address {
		if _, err := io.ReadFull(clientSide, make([]byte, 6)); err != nil {
			t.Fatalf("drain bind addr: %v", err)
		}
	}

	payload := []byte("connect-unit")
	if _, err := pw.Write(payload); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(clientSide, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo = %q, want %q", got, payload)
	}

	_ = pw.Close()
	_ = serverSide.Close()
	cancel()
	select {
	case <-errCh:
	case <-time.After(3 * time.Second):
		t.Fatal("handleConnect did not return")
	}
}
