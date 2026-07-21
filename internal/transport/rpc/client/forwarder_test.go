package client

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	proxy "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
)

// blockingRecvStream stays parked in RecvMsg until its context is canceled,
// which is what a real gRPC stream does while waiting for the next message.
// Only RecvMsg is meaningful; the embedded interface satisfies the rest and is
// never called on this path.
type blockingRecvStream struct {
	proxy.Proxy_ProxyClient
	ctx      context.Context
	entered  chan struct{}
	finished atomic.Bool
}

func (s *blockingRecvStream) RecvMsg(any) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.ctx.Done()
	s.finished.Store(true)
	return s.ctx.Err()
}

// TestForwarder_CopyTargetToSRCWaitsForReceiveGoroutine is a regression guard
// for a write-after-return hazard.
//
// The receive loop runs in its own goroutine and writes decoded payloads to the
// caller's io.Writer. If CopyTargetToSRC returns on ctx.Done() without waiting,
// that goroutine keeps running and keeps writing to a writer the caller now
// considers finished — free to close, reuse, or read. It is only safe today
// because every production caller happens to pass a net.Conn.
//
// This was caught by the end-to-end test, where the writer is a buffer.
func TestForwarder_CopyTargetToSRCWaitsForReceiveGoroutine(t *testing.T) {
	sessionCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := &blockingRecvStream{ctx: sessionCtx, entered: make(chan struct{}, 1)}
	f := NewForwarder(sessionCtx, cancel, stream, io.Discard, nil)

	// A derived context, matching how Start runs the copies under an errgroup:
	// the group's context can be canceled while the stream context is still live.
	copyCtx, copyCancel := context.WithCancel(sessionCtx)
	defer copyCancel()

	done := make(chan error, 1)
	go func() { done <- f.CopyTargetToSRC(copyCtx) }()

	select {
	case <-stream.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("receive goroutine never reached RecvMsg")
	}

	copyCancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("CopyTargetToSRC did not return after its context was canceled")
	}

	if !stream.finished.Load() {
		t.Error("CopyTargetToSRC returned while its receive goroutine was still " +
			"blocked in RecvMsg; that goroutine can still write to the caller's writer")
	}
}

// TestForwarder_CopyTargetToSRCReturnsReceiveError verifies the normal path is
// unaffected by the wait: an error from the receive loop is still propagated
// directly rather than being swallowed.
func TestForwarder_CopyTargetToSRCReturnsReceiveError(t *testing.T) {
	sessionCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A stream whose context is already canceled returns immediately.
	closedCtx, closeNow := context.WithCancel(context.Background())
	closeNow()
	stream := &blockingRecvStream{ctx: closedCtx, entered: make(chan struct{}, 1)}
	f := NewForwarder(sessionCtx, cancel, stream, io.Discard, nil)

	done := make(chan error, 1)
	go func() { done <- f.CopyTargetToSRC(sessionCtx) }()

	select {
	case err := <-done:
		if err == nil {
			t.Error("CopyTargetToSRC() error = nil, want the receive error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CopyTargetToSRC did not return on a receive error")
	}
}
