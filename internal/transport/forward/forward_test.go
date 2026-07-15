package forward

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

type mockDialer struct {
	addr string
	conn net.Conn
	err  error
}

func (m *mockDialer) Dial(network, addr string) (c net.Conn, err error) {
	if m.err != nil {
		return nil, m.err
	}
	m.addr = addr
	return m.conn, nil
}

func TestForward_Basics(t *testing.T) {
	f := New().(*Forward)
	if f.String() != TransportName {
		t.Errorf("String() = %v, want %v", f.String(), TransportName)
	}

	if err := f.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}

	_, err := f.Dial("tcp", "example.com:80")
	if err == nil || err.Error() != "forward: dialer not attached" {
		t.Errorf("Expected unattached dialer error, got %v", err)
	}

	// Attach
	md := &mockDialer{}
	f.Attach(md)
	_, err = f.Dial("tcp", "example.com:80")
	if err != nil && md.addr != "example.com:80" {
		t.Errorf("Attach failed to set dialer")
	}
}

func TestForward_Proxy(t *testing.T) {
	// Local echo server to act as the destination
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

	f := New().(*Forward)

	// Custom mock dialer that actually dials our echo server
	f.Attach(&mockDialer{
		conn: func() net.Conn {
			c, _ := net.Dial("tcp", ln.Addr().String())
			return c
		}(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	localAddr := make(chan string, 1)
	reqData := []byte("forward test")
	src := bytes.NewReader(reqData)
	var dst bytes.Buffer

	err = f.Proxy(ctx, ln.Addr().String(), localAddr, &dst, src)
	if err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}

	addr := <-localAddr
	if addr == "" {
		t.Errorf("Expected local addr, got empty string")
	}

	if dst.String() != string(reqData) {
		t.Errorf("Proxy() copied %q, want %q", dst.String(), string(reqData))
	}
}
