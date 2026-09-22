package direct

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// opensslSpeedSizes mirrors `openssl speed`. Small sizes → ops/s (code path);
// large sizes → MB/s (bulk copy through Direct.Proxy).
var opensslSpeedSizes = []int{16, 64, 256, 1024, 8192, 16384, 1 << 20}

func BenchmarkDirect_Proxy(b *testing.B) {
	echoAddr := startEcho(b)
	d := New()
	for _, size := range opensslSpeedSizes {
		b.Run(fmt.Sprintf("%d", size), func(b *testing.B) {
			srcReader, srcWriter := io.Pipe()
			dst := newChunkGate(size)
			errCh := make(chan error, 1)
			go func() {
				errCh <- d.Proxy(context.Background(), echoAddr, make(chan string, 1), dst, srcReader)
			}()

			payload := bytes.Repeat([]byte{0x5a}, size)
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
			if err := <-errCh; err != nil && err != io.EOF {
				b.Fatal(err)
			}
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

func startEcho(b *testing.B) string {
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
