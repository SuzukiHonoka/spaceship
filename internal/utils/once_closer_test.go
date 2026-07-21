package utils

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

type countCloser struct {
	n   atomic.Int32
	err error
}

func (c *countCloser) Close() error {
	c.n.Add(1)
	return c.err
}

func (c *countCloser) Read(p []byte) (int, error)  { return 0, io.EOF }
func (c *countCloser) Write(p []byte) (int, error) { return len(p), nil }

func TestOnceNetConnIdempotentClose(t *testing.T) {
	// net.Conn requires the full interface — use a real pipe.
	c1, c2 := net.Pipe()
	defer c2.Close()

	oc := OnceNetConn(c1)
	if err := oc.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := oc.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// Third close on underlying would error without once; wrapper stays nil err.
	if err := oc.Close(); err != nil {
		t.Fatalf("third Close: %v", err)
	}

	// Double-wrap is a no-op identity.
	if OnceNetConn(oc) != oc {
		t.Fatal("OnceNetConn should not re-wrap")
	}
}

func TestOnceReadWriteCloserIdempotentClose(t *testing.T) {
	raw := &countCloser{}
	oc := OnceReadWriteCloser(raw)
	if err := oc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := oc.Close(); err != nil {
		t.Fatal(err)
	}
	if got := raw.n.Load(); got != 1 {
		t.Fatalf("Close count = %d, want 1", got)
	}
}

func TestOnceReadWriteCloserPropagatesFirstError(t *testing.T) {
	want := errors.New("boom")
	raw := &countCloser{err: want}
	oc := OnceReadWriteCloser(raw)
	if err := oc.Close(); !errors.Is(err, want) {
		t.Fatalf("first Close = %v, want %v", err, want)
	}
	// Subsequent closes return the first error, not a second call.
	if err := oc.Close(); !errors.Is(err, want) {
		t.Fatalf("second Close = %v, want %v", err, want)
	}
	if got := raw.n.Load(); got != 1 {
		t.Fatalf("Close count = %d, want 1", got)
	}
}

func TestOnceNetConnConcurrentClose(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c2.Close()
	oc := OnceNetConn(c1)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = oc.Close()
		}()
	}
	wg.Wait()
}
