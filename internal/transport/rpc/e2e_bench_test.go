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

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/server"
	serverconfig "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	mdns "github.com/miekg/dns"
)

var opensslSpeedSizes = []int{16, 64, 256, 1024, 8192, 16384, 1 << 20}

// BenchmarkEndToEnd_TCPTunnel sweeps openssl-style chunk sizes over one
// long-lived client→gRPC→server→echo session. ops/s = chunk round-trips/sec
// (code/path efficiency); MB/s = tunneled throughput at that chunk size.
func BenchmarkEndToEnd_TCPTunnel(b *testing.B) {
	quietLogs(b)
	routeAllDirect(b)
	echoAddr := startTCPEcho(b)
	connectClient(b, startProxyServer(b))
	c := benchClient(b)

	for _, size := range opensslSpeedSizes {
		b.Run(fmt.Sprintf("%d", size), func(b *testing.B) {
			srcReader, srcWriter := io.Pipe()
			dst := newChunkGate(size)
			errCh := make(chan error, 1)
			go func() {
				errCh <- c.Proxy(
					context.Background(),
					echoAddr,
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
	quietLogs(b)
	routeAllDirect(b)
	echoAddr := startTCPEcho(b)
	connectClient(b, startProxyServer(b))
	c := benchClient(b)

	for _, size := range opensslSpeedSizes {
		b.Run(fmt.Sprintf("%d", size), func(b *testing.B) {
			srcReader, srcWriter := io.Pipe()
			dst := newChunkGate(size)
			errCh := make(chan error, 1)
			go func() {
				errCh <- c.Proxy(
					context.Background(),
					echoAddr,
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

// BenchmarkEndToEnd_TCPTunnelParallel runs several long-lived sessions at once
// over one pooled gRPC connection, the shape of a browser or TUN client. MB/s is
// aggregate echoed throughput; it exposes contention that one session cannot.
func BenchmarkEndToEnd_TCPTunnelParallel(b *testing.B) {
	quietLogs(b)
	routeAllDirect(b)
	echoAddr := startTCPEcho(b)
	connectClient(b, startProxyServer(b))
	c := benchClient(b)

	const size = 32 * 1024
	for _, sessions := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("sessions=%d", sessions), func(b *testing.B) {
			runEchoSessions(b, c, echoAddr, size, sessions)
		})
	}
}

// BenchmarkEndToEnd_TCPTunnelBufferSize sweeps the configurable transport
// buffer (config "buffer", in KiB) across its range. The buffer sizes every
// transport read and the pooled payload buffers, so each size gets its own
// server and client. Two shapes: one session moving 1MiB writes, where a
// larger buffer means fewer, larger messages; and eight sessions moving 32KiB
// writes, where most of a large buffer goes unused on each read.
func BenchmarkEndToEnd_TCPTunnelBufferSize(b *testing.B) {
	quietLogs(b)
	routeAllDirect(b)
	echoAddr := startTCPEcho(b)
	defaultSize := transport.GetBufferSize()
	b.Cleanup(func() { transport.SetBufferSize(uint16(defaultSize / 1024)) }) // #nosec G115 -- default is 256KiB

	for _, kib := range []uint16{16, 64, 256, 1024, rpc.MaxTransportBufferSize / 1024} {
		b.Run(fmt.Sprintf("buffer=%dKiB", kib), func(b *testing.B) {
			transport.SetBufferSize(kib)
			connectClient(b, startProxyServer(b))
			c := benchClient(b)

			for _, shape := range []struct {
				name     string
				size     int
				sessions int
			}{
				{"bulk=1MiB/sessions=1", 1 << 20, 1},
				{"chunk=32KiB/sessions=8", 32 * 1024, 8},
			} {
				b.Run(shape.name, func(b *testing.B) {
					runEchoSessions(b, c, echoAddr, shape.size, shape.sessions)
				})
			}
		})
	}
}

// runEchoSessions opens sessions long-lived tunnels to echoAddr and splits
// b.N round trips of size bytes between them.
func runEchoSessions(b *testing.B, c *client.Client, echoAddr string, size, sessions int) {
	b.Helper()
	type session struct {
		w     *io.PipeWriter
		gate  *chunkGate
		errCh chan error
	}
	all := make([]session, sessions)
	for i := range all {
		r, w := io.Pipe()
		all[i] = session{w: w, gate: newChunkGate(size), errCh: make(chan error, 1)}
		go func(s session) {
			s.errCh <- c.Proxy(context.Background(), echoAddr, make(chan string, 1), s.gate, r)
		}(all[i])
	}

	payload := bytes.Repeat([]byte{0x5a}, size)
	var next atomic.Int64
	b.SetBytes(int64(size) * 2)
	b.ReportAllocs()
	b.ResetTimer()
	errs := make(chan error, sessions)
	for _, s := range all {
		go func(s session) {
			for next.Add(1) <= int64(b.N) {
				if _, err := s.w.Write(payload); err != nil {
					errs <- err
					return
				}
				if err := s.gate.WaitChunk(); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}(s)
	}
	for range all {
		if err := <-errs; err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()

	for _, s := range all {
		_ = s.w.Close()
		if err := <-s.errCh; err != nil && err != io.EOF {
			b.Fatal(err)
		}
	}
}

// BenchmarkEndToEnd_TCPSessionSetup measures opening a tunneled TCP session,
// moving one byte each way, and closing it: the per-connection cost that
// dominates short HTTP requests rather than bulk transfer.
func BenchmarkEndToEnd_TCPSessionSetup(b *testing.B) {
	quietLogs(b)
	routeAllDirect(b)
	echoAddr := startTCPEcho(b)
	connectClient(b, startProxyServer(b))
	c := benchClient(b)

	samples := make([]time.Duration, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		start := time.Now()
		r, w := io.Pipe()
		gate := newChunkGate(1)
		errCh := make(chan error, 1)
		go func() {
			errCh <- c.Proxy(context.Background(), echoAddr, make(chan string, 1), gate, r)
		}()
		if _, err := w.Write([]byte{0x5a}); err != nil {
			b.Fatal(err)
		}
		if err := gate.WaitChunk(); err != nil {
			b.Fatal(err)
		}
		_ = w.Close()
		if err := <-errCh; err != nil && err != io.EOF {
			b.Fatal(err)
		}
		samples[i] = time.Since(start)
	}
	b.StopTimer()
	reportRTT(b, samples)
}

// BenchmarkEndToEnd_UDPTunnel measures datagram round trips over one UDP
// association: client DialPacket → gRPC stream → server → UDP echo and back.
// SOCKS5 UDP ASSOCIATE (QUIC, games, DNS) rides this path.
func BenchmarkEndToEnd_UDPTunnel(b *testing.B) {
	quietLogs(b)
	routeAllDirect(b)
	echoAddr := startUDPEcho(b)
	connectClient(b, startProxyServer(b))
	c := benchClient(b)
	target, err := net.ResolveUDPAddr("udp", echoAddr)
	if err != nil {
		b.Fatal(err)
	}

	for _, size := range []int{64, 512, 1400, 8192} {
		b.Run(fmt.Sprintf("%d", size), func(b *testing.B) {
			pc, err := c.DialPacket("udp", echoAddr)
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = pc.Close() }()

			payload := bytes.Repeat([]byte{0x5a}, size)
			buf := make([]byte, 65535)
			samples := make([]time.Duration, b.N)
			b.SetBytes(int64(size) * 2)
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				start := time.Now()
				if _, err := pc.WriteTo(payload, target); err != nil {
					b.Fatal(err)
				}
				if err := pc.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
					b.Fatal(err)
				}
				n, _, err := pc.ReadFrom(buf)
				if err != nil {
					b.Fatal(err)
				}
				if n != size {
					b.Fatalf("echoed %d bytes, want %d", n, size)
				}
				samples[i] = time.Since(start)
			}
			b.StopTimer()
			reportRTT(b, samples)
		})
	}
}

// BenchmarkEndToEnd_DNSExchange measures the raw-wire DNS RPC that TUN DNS
// hijacking and listen_dns use, against a local upstream resolver.
func BenchmarkEndToEnd_DNSExchange(b *testing.B) {
	quietLogs(b)
	// Lift admission limits: this measures the RPC path, not the rate limiter.
	unlimited := &serverconfig.DNSExchange{
		MaxConcurrent:        1 << 16,
		MaxConcurrentPerUser: 1 << 16,
		QueriesPerSecond:     1_000_000,
		QueriesPerUser:       1_000_000,
		Burst:                1 << 16,
		BurstPerUser:         1 << 16,
	}
	connectClient(b, startProxyServerWithResolverOptions(
		b, startTestResolver(b), server.WithDNSExchangeLimits(unlimited),
	))
	c := benchClient(b)

	query := new(mdns.Msg)
	query.SetQuestion("known.test.", mdns.TypeA)
	wire, err := query.Pack()
	if err != nil {
		b.Fatal(err)
	}

	b.Run("serial", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := c.DnsExchange(context.Background(), wire, proto.Network_UDP, false); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := c.DnsExchange(context.Background(), wire, proto.Network_UDP, false); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
}

func quietLogs(b *testing.B) {
	b.Helper()
	oldWriter := log.Writer()
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(oldWriter) })
}

// startTCPEcho runs a TCP echo target and returns its address.
func startTCPEcho(b *testing.B) string {
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

func benchClient(b *testing.B) *client.Client {
	b.Helper()
	c, err := client.New()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = c.Close() })
	return c
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
