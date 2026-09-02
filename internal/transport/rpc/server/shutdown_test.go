package server

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
)

const http2ClientPreface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

// emptySettingsFrame is a zero-length SETTINGS frame on stream 0.
var emptySettingsFrame = []byte{0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00}

// establishHTTP2 drives the client half of the HTTP/2 handshake by hand and
// returns once the server has acknowledged our SETTINGS. At that point grpc-go
// has registered the transport and its reader loop is parked in ReadFrame,
// which is the state a live proxy client leaves behind.
//
// Using raw frames rather than a grpc client conn keeps the peer under the
// test's control: after this returns nothing ever reads from the socket again.
func establishHTTP2(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, http2ClientPreface); err != nil {
		t.Fatalf("write client preface: %v", err)
	}
	if _, err := conn.Write(emptySettingsFrame); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	header := make([]byte, 9)
	for {
		if _, err := io.ReadFull(conn, header); err != nil {
			t.Fatalf("read frame header: %v", err)
		}
		length := int(header[0])<<16 | int(header[1])<<8 | int(header[2])
		frameType, flags := header[3], header[4]
		if length > 0 {
			if _, err := io.ReadFull(conn, make([]byte, length)); err != nil {
				t.Fatalf("read frame payload: %v", err)
			}
		}
		// SETTINGS with the ACK flag: the server consumed our frame, so the
		// transport is registered and serving.
		if frameType == 0x04 && flags&0x01 != 0 {
			break
		}
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
}

// TestServerCancelDoesNotWaitForSilentPeers pins the shutdown contract: a stop
// signal force-closes established transports instead of draining them.
//
// Releases up to v2.1.7 called grpc's GracefulStop first, which only sends
// GOAWAY. A peer that is connected but no longer reading — a proxy client on a
// dropped mobile link, a NAT entry that vanished — never answers it, so
// grpc.Server.stop blocked in `for len(s.conns) != 0 { s.cv.Wait() }` while its
// reader loops sat in ReadFrame, and `systemctl restart` waited for the
// fallback timer instead of for the process.
func TestServerCancelDoesNotWaitForSilentPeers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := NewServer(ctx, config.Users{{UUID: "silent-peer-user"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.serve(listener) }()

	const peers = 4
	for range peers {
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		establishHTTP2(t, conn)
	}

	started := time.Now()
	cancel()
	select {
	case err := <-serveErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("serve() error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("shutdown took %s, want at most 2s", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server drained silent peers instead of force-closing them")
	}
}
