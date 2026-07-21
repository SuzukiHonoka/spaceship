package transport

import (
	"io"
	"net"
	"testing"
)

func TestCloseWriteOrClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, _ := ln.Accept()
		if c != nil {
			_, _ = io.Copy(io.Discard, c)
			_ = c.Close()
		}
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	CloseWriteOrClose(c)
	_ = c.Close()

	pr, pw := io.Pipe()
	CloseWriteOrClose(pw)
	CloseWriteOrClose(pr)
	CloseWriteOrClose(42) // non-closer no-op
}

func TestCloseAll(t *testing.T) {
	pr, pw := io.Pipe()
	CloseAll(pr, pw, "skip", nil)
}

type countCloser struct {
	n int
}

func (c *countCloser) Close() error {
	c.n++
	return nil
}

func TestCloseAllDedupesSameCloser(t *testing.T) {
	c := &countCloser{}
	// CONNECT path: src and dst are the same client conn.
	CloseAll(c, c, c)
	if c.n != 1 {
		t.Fatalf("Close count = %d, want 1", c.n)
	}
}
