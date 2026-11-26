package redirect

import (
	"context"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
)

func TestServer_ServeConnHijacksTCPDNS(t *testing.T) {
	var hijacked atomic.Int32
	srv, err := New(context.Background(), &Config{
		TCPDNSHandler: func(c net.Conn) {
			hijacked.Add(1)
			_, _ = c.Write([]byte("dns-ok"))
		},
		DNSHijackAddresses: []netip.Addr{netip.MustParseAddr("198.18.0.2")},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = srv.Close() }()

	// Case 1: destination port 53
	srv.resolveDestination = func(net.Conn) (*net.TCPAddr, error) {
		return &net.TCPAddr{IP: net.IPv4(8, 8, 8, 8), Port: 53}, nil
	}
	c1, s1 := net.Pipe()
	done1 := make(chan error, 1)
	go func() { done1 <- srv.ServeConn(s1) }()
	buf := make([]byte, 16)
	n, err := c1.Read(buf)
	if err != nil || string(buf[:n]) != "dns-ok" {
		t.Fatalf("port 53 hijack read = (%q, %v), want dns-ok", buf[:n], err)
	}
	_ = c1.Close()
	if err := <-done1; err != nil {
		t.Fatalf("ServeConn port 53: %v", err)
	}

	// Case 2: custom virtual DNS IP on non-53 port
	srv.resolveDestination = func(net.Conn) (*net.TCPAddr, error) {
		return &net.TCPAddr{IP: net.IPv4(198, 18, 0, 2), Port: 5353}, nil
	}
	c2, s2 := net.Pipe()
	done2 := make(chan error, 1)
	go func() { done2 <- srv.ServeConn(s2) }()
	n, err = c2.Read(buf)
	if err != nil || string(buf[:n]) != "dns-ok" {
		t.Fatalf("custom DNS IP hijack read = (%q, %v), want dns-ok", buf[:n], err)
	}
	_ = c2.Close()
	if err := <-done2; err != nil {
		t.Fatalf("ServeConn custom DNS IP: %v", err)
	}

	if got := hijacked.Load(); got != 2 {
		t.Fatalf("hijacked count = %d, want 2", got)
	}
}
