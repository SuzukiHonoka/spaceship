package utils

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

type recordingSOCKSForward struct {
	proxyAddress chan string
	target       chan string
}

func newRecordingSOCKSForward() *recordingSOCKSForward {
	return &recordingSOCKSForward{
		proxyAddress: make(chan string, 1),
		target:       make(chan string, 1),
	}
}

func (d *recordingSOCKSForward) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}

func (d *recordingSOCKSForward) DialContext(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if network != "tcp" {
		return nil, fmt.Errorf("unexpected proxy network %q", network)
	}
	d.proxyAddress <- address
	client, server := net.Pipe()
	go d.serveSOCKS(server)
	return client, nil
}

func (d *recordingSOCKSForward) serveSOCKS(conn net.Conn) {
	defer conn.Close()

	greeting := make([]byte, 3)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		return
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return
	}

	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return
	}
	var host string
	switch header[3] {
	case 1:
		raw := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(conn, raw); err != nil {
			return
		}
		host = net.IP(raw).String()
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			return
		}
		raw := make([]byte, int(size[0]))
		if _, err := io.ReadFull(conn, raw); err != nil {
			return
		}
		host = string(raw)
	case 4:
		raw := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(conn, raw); err != nil {
			return
		}
		host = net.IP(raw).String()
	default:
		return
	}
	var port [2]byte
	if _, err := io.ReadFull(conn, port[:]); err != nil {
		return
	}
	d.target <- net.JoinHostPort(host, fmt.Sprint(binary.BigEndian.Uint16(port[:])))
	_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 1})
}

func TestLoadProxyWithDialerUsesProvidedForwardPath(t *testing.T) {
	forward := newRecordingSOCKSForward()
	dialer, err := LoadProxyWithDialer("socks5://proxy.test:1080", forward)
	if err != nil {
		t.Fatal(err)
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		t.Fatalf("SOCKS dialer type %T does not preserve context cancellation", dialer)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := contextDialer.DialContext(ctx, "tcp", "target.test:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	select {
	case got := <-forward.proxyAddress:
		if got != "proxy.test:1080" {
			t.Fatalf("provided forward dialed %q, want proxy.test:1080", got)
		}
	case <-ctx.Done():
		t.Fatal("provided forward dialer was not called")
	}
	select {
	case got := <-forward.target:
		if got != "target.test:443" {
			t.Fatalf("SOCKS target = %q, want target.test:443", got)
		}
	case <-ctx.Done():
		t.Fatal("SOCKS handshake did not carry the target")
	}
}
