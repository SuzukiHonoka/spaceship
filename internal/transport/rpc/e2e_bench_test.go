package rpc_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
)

var opensslSpeedSizes = []int{16, 64, 256, 1024, 8192, 16384, 1 << 20}

// BenchmarkEndToEnd_TCPTunnel sweeps openssl-style chunk sizes over one
// long-lived client→gRPC→server→echo session. ops/s = chunk round-trips/sec
// (code/path efficiency); MB/s = tunneled throughput at that chunk size.
func BenchmarkEndToEnd_TCPTunnel(b *testing.B) {
	oldWriter := log.Writer()
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(oldWriter) })

	routeAllDirect(b)

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

	connectClient(b, startProxyServer(b))
	c, err := client.New()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = c.Close() })

	for _, size := range opensslSpeedSizes {
		b.Run(fmt.Sprintf("%d", size), func(b *testing.B) {
			srcReader, srcWriter := io.Pipe()
			dst := newChunkGate(size)
			errCh := make(chan error, 1)
			go func() {
				errCh <- c.Proxy(
					context.Background(),
					ln.Addr().String(),
					make(chan string, 1),
					dst,
					srcReader,
				)
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
			elapsed := time.Since(start).Seconds()
			if elapsed > 0 {
				b.ReportMetric(float64(b.N)/elapsed, "ops/s")
			}

			_ = srcWriter.Close()
			if err := <-errCh; err != nil && err != io.EOF {
				b.Fatal(err)
			}
		})
	}
}

// BenchmarkEndToEnd_TCPTunnel_Latency measures per-chunk RTT over one long-lived
// client→gRPC→server→echo session and reports avg/p50/p99 in microseconds.
func BenchmarkEndToEnd_TCPTunnel_Latency(b *testing.B) {
	oldWriter := log.Writer()
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(oldWriter) })

	routeAllDirect(b)

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

	connectClient(b, startProxyServer(b))
	c, err := client.New()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = c.Close() })

	for _, size := range opensslSpeedSizes {
		b.Run(fmt.Sprintf("%d", size), func(b *testing.B) {
			srcReader, srcWriter := io.Pipe()
			dst := newChunkGate(size)
			errCh := make(chan error, 1)
			go func() {
				errCh <- c.Proxy(
					context.Background(),
					ln.Addr().String(),
					make(chan string, 1),
					dst,
					srcReader,
				)
			}()

			payload := bytes.Repeat([]byte{0x5a}, size)
			samples := make([]time.Duration, b.N)
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				start := time.Now()
				if _, err := srcWriter.Write(payload); err != nil {
					b.Fatal(err)
				}
				if err := dst.WaitChunk(); err != nil {
					b.Fatal(err)
				}
				samples[i] = time.Since(start)
			}
			b.StopTimer()
			reportRTT(b, samples)

			_ = srcWriter.Close()
			if err := <-errCh; err != nil && err != io.EOF {
				b.Fatal(err)
			}
		})
	}
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

func reportRTT(b *testing.B, samples []time.Duration) {
	b.Helper()
	if len(samples) == 0 {
		return
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	b.ReportMetric(float64(sum)/float64(len(sorted))/float64(time.Microsecond), "avg-µs")
	b.ReportMetric(float64(sorted[len(sorted)/2])/float64(time.Microsecond), "p50-µs")
	b.ReportMetric(float64(sorted[percentileIndex(len(sorted), 99)])/float64(time.Microsecond), "p99-µs")
}

func percentileIndex(n, p int) int {
	if n <= 1 {
		return 0
	}
	i := (p*(n-1) + 99) / 100
	if i >= n {
		return n - 1
	}
	return i
}
