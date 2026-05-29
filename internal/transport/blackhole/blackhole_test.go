package blackhole

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

func TestBlackHole_Basics(t *testing.T) {
	bh := New()

	if bh.String() != TransportName {
		t.Errorf("String() = %v, want %v", bh.String(), TransportName)
	}

	if err := bh.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}

	if _, err := bh.Dial("tcp", "127.0.0.1:80"); err == nil {
		t.Errorf("Dial() expected error, got nil")
	}
}

func TestBlackHole_Proxy_EOF(t *testing.T) {
	bh := New()
	ctx := context.Background()
	localAddr := make(chan string, 1)

	src := bytes.NewReader([]byte("test data"))
	var dst bytes.Buffer

	err := bh.Proxy(ctx, "test", localAddr, &dst, src)
	if err != nil {
		t.Errorf("Proxy() with EOF expected nil error, got %v", err)
	}

	if addr := <-localAddr; addr != "127.0.0.1:0" {
		t.Errorf("localAddr = %v, want 127.0.0.1:0", addr)
	}

	// Wait for channel close
	if _, ok := <-localAddr; ok {
		t.Errorf("localAddr channel should be closed")
	}
}

func TestBlackHole_Proxy_ContextCancel(t *testing.T) {
	bh := New()
	ctx, cancel := context.WithCancel(context.Background())
	localAddr := make(chan string, 1)

	// A reader that blocks forever
	r, w := io.Pipe()
	defer w.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- bh.Proxy(ctx, "test", localAddr, nil, r)
	}()

	// Read the local addr
	<-localAddr

	// Cancel context to interrupt blocked read
	cancel()

	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Errorf("Proxy() expected context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Errorf("Proxy() did not return after context cancellation")
	}
}
