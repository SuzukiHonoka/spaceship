package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

// opensslSpeedSizes mirrors `openssl speed` block sizes. Small sizes stress
// per-chunk code path (ops/s); large sizes stress bulk copy (MB/s).
var opensslSpeedSizes = []int{16, 64, 256, 1024, 8192, 16384, 1 << 20}

// strip WriterTo/ReaderFrom so io.CopyBuffer takes the real buffered path
// (bytes.Reader.WriteTo(io.Discard) is a no-touch fast path and fakes MB/s).
type readerOnly struct{ r io.Reader }

func (r readerOnly) Read(p []byte) (int, error) { return r.r.Read(p) }

type writerOnly struct{ w io.Writer }

func (w writerOnly) Write(p []byte) (int, error) { return w.w.Write(p) }

func BenchmarkCopyWithContext(b *testing.B) {
	for _, size := range opensslSpeedSizes {
		b.Run(fmt.Sprintf("%d", size), func(b *testing.B) {
			payload := bytes.Repeat([]byte{0x5a}, size)
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			start := time.Now()
			for range b.N {
				if err := CopyWithContext(
					context.Background(),
					nil,
					writerOnly{io.Discard},
					readerOnly{bytes.NewReader(payload)},
					DirectionIn,
				); err != nil {
					b.Fatal(err)
				}
			}
			reportOpsPerSec(b, start)
		})
	}
}

// BenchmarkCopyWithContext_Bidirectional keeps one TCP echo session open and
// pumps size-byte chunks (openssl-style). ops/s = chunk round-trips/sec.
func BenchmarkCopyWithContext_Bidirectional(b *testing.B) {
	echoAddr := startBenchEcho(b)
	for _, size := range opensslSpeedSizes {
		b.Run(fmt.Sprintf("%d", size), func(b *testing.B) {
			conn, err := net.Dial("tcp", echoAddr)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = conn.Close() })

			srcReader, srcWriter := io.Pipe()
			dst := newChunkGate(size)
			ctx, cancel := context.WithCancel(context.Background())
			b.Cleanup(cancel)

			var group errgroup.Group
			group.Go(func() error {
				defer CloseWriteOrClose(conn)
				return CopyWithContext(ctx, cancel, conn, srcReader, DirectionOut)
			})
			group.Go(func() error {
				defer cancel()
				return CopyWithContext(ctx, func() { _ = conn.Close() }, dst, conn, DirectionIn)
			})

			payload := bytes.Repeat([]byte{0xa5}, size)
			// Wire volume: size out + size in per op.
			b.SetBytes(int64(size) * 2)
			b.ReportAllocs()
			b.ResetTimer()
			start := time.Now()
			for range b.N {
				if _, err := srcWriter.Write(payload); err != nil {
					b.Fatal(err)
				}
				if err := dst.WaitChunk(); err != nil {
					b.Fatal(err)
				}
			}
			reportOpsPerSec(b, start)

			_ = srcWriter.Close()
			_ = group.Wait()
		})
	}
}

func reportOpsPerSec(b *testing.B, start time.Time) {
	b.Helper()
	elapsed := time.Since(start).Seconds()
	if elapsed > 0 {
		b.ReportMetric(float64(b.N)/elapsed, "ops/s")
	}
}

func startBenchEcho(b *testing.B) string {
	b.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().String()
}

// chunkGate counts received bytes and signals once per size-byte chunk.
// Used so a long-lived Proxy/Copy session can measure openssl-style ops/s.
type chunkGate struct {
	size   int64
	n      atomic.Int64
	chunks chan struct{}
}

func newChunkGate(size int) *chunkGate {
	return &chunkGate{
		size:   int64(size),
		chunks: make(chan struct{}, 64),
	}
}

func (g *chunkGate) Write(p []byte) (int, error) {
	prev := g.n.Add(int64(len(p))) - int64(len(p))
	cur := prev + int64(len(p))
	from := prev / g.size
	to := cur / g.size
	for range to - from {
		g.chunks <- struct{}{}
	}
	return len(p), nil
}

func (g *chunkGate) WaitChunk() error {
	select {
	case <-g.chunks:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timeout waiting for %d-byte chunk", g.size)
	}
}
