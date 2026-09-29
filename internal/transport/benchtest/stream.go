// Package benchtest provides matched workload drivers for transport benchmarks.
// It is imported only by tests, not by the application.
package benchtest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type proxy interface {
	Proxy(context.Context, string, chan<- string, io.Writer, io.Reader) error
}

// Stream measures continuous echoed traffic without waiting for each chunk's
// response. One operation is a 1 MiB application write; MB/s counts both legs.
// This complements, but does not replace, stop-and-wait RTT benchmarks.
func Stream(b *testing.B, p proxy, addr string) {
	b.Helper()
	const size = 1 << 20
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	r, w := io.Pipe()
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = r.CloseWithError(ctx.Err()) })
	defer stop()
	sink := &byteSink{done: make(chan struct{}, 1)}
	sink.remaining.Store(4 * size)
	errCh := make(chan error, 1)
	go func() {
		err := p.Proxy(ctx, addr, make(chan string, 1), sink, r)
		_ = r.CloseWithError(io.ErrClosedPipe)
		errCh <- err
	}()
	write := func(payload []byte) {
		b.Helper()
		if _, err := w.Write(payload); err != nil {
			b.Fatal(err)
		}
	}
	wait := func() {
		b.Helper()
		select {
		case <-sink.done:
		case err := <-errCh:
			b.Fatalf("proxy ended before echo completed: %v", err)
		case <-ctx.Done():
			b.Fatal(ctx.Err())
		}
	}
	payload := bytes.Repeat([]byte{0x5a}, size)
	for range 4 {
		write(payload)
	}
	wait()
	sink.remaining.Store(int64(b.N) * size)
	b.SetBytes(2 * size)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		write(payload)
	}
	wait()
	b.StopTimer()
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/s")
	_ = w.Close()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, io.EOF) {
			b.Fatal(err)
		}
	case <-ctx.Done():
		b.Fatal(ctx.Err())
	}
}

// byteSink signals once after the exact expected echo length arrives, avoiding
// per-chunk timers/channels in the continuous-stream measurement. The remaining
// count is reset only after consuming done and before sending the next batch.
type byteSink struct {
	remaining atomic.Int64
	done      chan struct{}
}

func (s *byteSink) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	left := s.remaining.Add(-int64(len(p)))
	if left < 0 {
		return 0, errors.New("echo exceeded expected byte count")
	}
	if left == 0 {
		s.done <- struct{}{}
	}
	return len(p), nil
}
