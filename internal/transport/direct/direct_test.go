package direct

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

func TestDirect_Basics(t *testing.T) {
	d := New()
	if d.String() != TransportName {
		t.Errorf("String() = %v, want %v", d.String(), TransportName)
	}

	if err := d.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

func TestDirect_DialPacket(t *testing.T) {
	d := New().(transport.PacketDialer)
	pc, err := d.DialPacket("udp", ":0")
	if err != nil {
		t.Fatalf("DialPacket() error = %v", err)
	}
	defer pc.Close()

	if _, ok := pc.LocalAddr().(*net.UDPAddr); !ok {
		t.Errorf("DialPacket() did not return UDP addr")
	}
}

func TestDirect_Proxy(t *testing.T) {
	// Start a local echo server
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			_, _ = conn.Write(buf[:n])
		}
	}()

	d := New()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	localAddr := make(chan string, 1)

	reqData := []byte("hello spaceship")
	src := bytes.NewReader(reqData)
	var dst bytes.Buffer

	// Run proxy
	err = d.Proxy(ctx, ln.Addr().String(), localAddr, &dst, src)
	if err != nil {
		// Expect context deadline since the echo server keeps connection open
		// and we only sent "hello spaceship" which finishes reading on src,
		// but the proxy waits for io.Copy from dst to finish (which blocks on conn.Read).
		if !strings.Contains(err.Error(), "context deadline exceeded") {
			t.Errorf("Proxy() expected context deadline exceeded, got %v", err)
		}
	}

	addr := <-localAddr
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Errorf("localAddr = %v, expected 127.0.0.1:*", addr)
	}

	if dst.String() != string(reqData) {
		t.Errorf("Proxy() copied %q, want %q", dst.String(), string(reqData))
	}
}

func TestDirect_Proxy_DialError(t *testing.T) {
	d := New()
	ctx := context.Background()
	localAddr := make(chan string, 1)

	// Dial an invalid address
	err := d.Proxy(ctx, "256.256.256.256:80", localAddr, nil, nil)
	if err == nil {
		t.Errorf("Proxy() expected error for invalid dial")
	}

	// Verify localAddr is closed
	if _, ok := <-localAddr; ok {
		t.Errorf("localAddr channel not closed on error")
	}
}
