package rpc_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
)

const (
	muxFanoutSessions = 80
	// streamWindow is how many chunks one session may have in flight. The sink
	// never blocks: the writer waits for an echo before exceeding this, so a
	// full HTTP/2 window cannot stall the benchmark with unread credits.
	streamWindow          = 4
	muxFanoutDriveTimeout = 30 * time.Second
)

// BenchmarkEndToEnd_MuxFanout measures 80 concurrent echoed sessions — ten
// times a mux of 8 — while varying how many persistent HTTP/2 connections
// carry them.
//
// mux=1, mux=8, and mux=80 are warm pools. The pool grows only after every
// connection reaches MaxConcurrentStreams (4096), so these runs must stay at
// the configured size. mux=0 is the legacy path: each session dials and holds
// its own connection.
//
// rtt is stop-and-wait and records each chunk's round trip (avg/p50/p99).
// stream keeps up to four chunks in flight per session. ops/s is parallel
// rounds per second; chunks/s counts every session. cpu% is process user+sys
// over wall time for the timed section, including the in-process server and
// echo, so it can exceed 100 on more than one core. Both shapes run on
// loopback. That does not saturate the 16 MiB HTTP/2 connection window; a
// high-RTT path is where sharing one TCP connection starts to cap throughput.
// pool-conns and streams/conn record what the pool actually did.
func BenchmarkEndToEnd_MuxFanout(b *testing.B) {
	quietLogs(b)
	routeAllDirect(b)
	echoAddr := startTCPEcho(b)
	serverAddr := startProxyServer(b)

	shapes := []struct {
		name string
		size int
		wait bool
	}{
		{name: "rtt", size: 32 * 1024, wait: true},
		{name: "stream", size: 256 * 1024, wait: false},
	}
	for _, shape := range shapes {
		b.Run(shape.name, func(b *testing.B) {
			for _, mux := range []uint8{1, 8, 80, 0} {
				b.Run(fmt.Sprintf("mux=%d/sessions=%d", mux, muxFanoutSessions), func(b *testing.B) {
					benchmarkMuxFanout(b, serverAddr, echoAddr, mux, muxFanoutSessions, shape.size, shape.wait)
				})
			}
		})
	}
}

func benchmarkMuxFanout(b *testing.B, serverAddr, echoAddr string, mux uint8, sessions, size int, wait bool) {
	b.Helper()
	connectClientWithMux(b, serverAddr, mux)
	ctx, cancel := context.WithCancel(context.Background())
	b.Cleanup(cancel)

	payload := bytes.Repeat([]byte{0x5a}, size)
	opened := openMuxSessions(b, ctx, echoAddr, sessions, size, wait)
	const warmN = 4
	if err := driveMuxSessions(opened, payload, warmN, wait, nil); err != nil {
		b.Fatal(err)
	}
	// ResetTimer drops metrics recorded before it, so publish the pool
	// snapshot only after the timed section. The snapshot itself is taken
	// once the sessions are open: the timed writes do not check out more.
	poolConns, streamsPerConn := muxPoolSnapshot(b, mux, sessions)
	var samples []time.Duration
	if wait {
		samples = make([]time.Duration, sessions*b.N)
	}

	b.SetBytes(int64(sessions) * int64(size) * 2)
	b.ReportAllocs()
	b.ResetTimer()
	cpuBefore := procCPU()
	if err := driveMuxSessions(opened, payload, b.N, wait, samples); err != nil {
		b.Fatal(err)
	}
	b.StopTimer()
	reportMuxFanoutCost(b, sessions, cpuBefore, samples)
	b.ReportMetric(poolConns, "pool-conns")
	b.ReportMetric(streamsPerConn, "streams/conn")
}

func reportMuxFanoutCost(b *testing.B, sessions int, cpuBefore time.Duration, samples []time.Duration) {
	b.Helper()
	elapsed := b.Elapsed()
	if elapsed > 0 {
		seconds := elapsed.Seconds()
		b.ReportMetric(float64(b.N)/seconds, "ops/s")
		b.ReportMetric(float64(b.N*sessions)/seconds, "chunks/s")
		b.ReportMetric(float64(procCPU()-cpuBefore)/float64(elapsed)*100, "cpu%")
	}
	if len(samples) > 0 {
		reportRTT(b, samples)
	}
}

func procCPU() time.Duration {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	return time.Duration(usage.Utime.Nano()) + time.Duration(usage.Stime.Nano())
}

// muxSession is one proxied echo flow checked out from the pool on its own.
type muxSession struct {
	w    *io.PipeWriter
	gate *chunkGate
	pipe *chunkPipeline
}

func openMuxSessions(b *testing.B, ctx context.Context, echoAddr string, sessions, size int, wait bool) []muxSession {
	b.Helper()
	opened := make([]muxSession, 0, sessions)
	b.Cleanup(func() {
		for _, s := range opened {
			_ = s.w.Close()
		}
	})
	for range sessions {
		c, err := client.New()
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { _ = c.Close() })

		reader, writer := io.Pipe()
		s := muxSession{w: writer}
		var dst io.Writer
		if wait {
			s.gate = newChunkGate(size)
			dst = s.gate
		} else {
			s.pipe = newChunkPipeline(size, streamWindow)
			dst = s.pipe
		}
		errCh := make(chan error, 1)
		go func() {
			errCh <- c.Proxy(ctx, echoAddr, make(chan string, 1), dst, reader)
		}()
		// Close unblocks Proxy. The buffered send lets the goroutine exit
		// even when the benchmark does not read this error.
		b.Cleanup(func() {
			_ = writer.Close()
			select {
			case <-errCh:
			case <-time.After(5 * time.Second):
			}
		})
		opened = append(opened, s)
	}
	return opened
}

// driveMuxSessions writes rounds of payload on every session. Stop-and-wait
// sessions ack each chunk. Streaming sessions ack only often enough to keep
// streamWindow chunks in flight. One round is the benchmark iteration: ns/op
// is wall time for every session to finish that chunk, and MB/s counts both
// echo legs.
func driveMuxSessions(opened []muxSession, payload []byte, rounds int, wait bool, samples []time.Duration) error {
	if rounds == 0 {
		return nil
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- driveMuxSessionsBody(opened, payload, rounds, wait, samples)
	}()
	timer := time.NewTimer(muxFanoutDriveTimeout)
	defer timer.Stop()
	select {
	case err := <-errCh:
		return err
	case <-timer.C:
		for _, s := range opened {
			_ = s.w.Close()
		}
		return fmt.Errorf("mux fanout: %d sessions did not finish %d rounds within %s", len(opened), rounds, muxFanoutDriveTimeout)
	}
}

func driveMuxSessionsBody(opened []muxSession, payload []byte, rounds int, wait bool, samples []time.Duration) error {
	errCh := make(chan error, len(opened))
	for i, s := range opened {
		var part []time.Duration
		if samples != nil {
			part = samples[i*rounds : (i+1)*rounds]
		}
		go func() {
			errCh <- driveOneSession(s, payload, rounds, wait, part)
		}()
	}
	for range opened {
		if err := <-errCh; err != nil {
			return err
		}
	}
	return nil
}

func driveOneSession(s muxSession, payload []byte, rounds int, wait bool, samples []time.Duration) error {
	inFlight := 0
	recorded := 0
	for range rounds {
		if !wait && inFlight == streamWindow {
			if err := s.pipe.waitEcho(muxFanoutDriveTimeout); err != nil {
				return err
			}
			inFlight--
		}
		var start time.Time
		if wait {
			start = time.Now()
		}
		if _, err := s.w.Write(payload); err != nil {
			return err
		}
		if wait {
			if err := s.gate.WaitChunk(); err != nil {
				return err
			}
			if samples != nil {
				samples[recorded] = time.Since(start)
				recorded++
			}
			continue
		}
		inFlight++
	}
	for inFlight > 0 {
		if err := s.pipe.waitEcho(muxFanoutDriveTimeout); err != nil {
			return err
		}
		inFlight--
	}
	return nil
}

func muxPoolSnapshot(b *testing.B, mux uint8, sessions int) (poolConns, streamsPerConn float64) {
	b.Helper()
	total, _, load := client.GetConnectionSummary()
	if mux == 0 {
		if total != 0 {
			b.Fatalf("unpooled client left %d connections in the pool", total)
		}
		return 0, 1
	}
	if total != int(mux) || int(load) != sessions {
		b.Fatalf("pool mux=%d sessions=%d: connections=%d load=%d", mux, sessions, total, load)
	}
	return float64(total), float64(load) / float64(total)
}

// chunkPipeline counts echoed chunks and returns one credit per chunk. The
// credit buffer is the pipeline window, so Write does not block when the
// benchmark writer keeps at most that many chunks unacked.
type chunkPipeline struct {
	size    int64
	pending atomic.Int64
	echo    chan struct{}
}

func newChunkPipeline(size, window int) *chunkPipeline {
	return &chunkPipeline{
		size: int64(size),
		echo: make(chan struct{}, window),
	}
}

func (p *chunkPipeline) waitEcho(d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-p.echo:
		return nil
	case <-timer.C:
		return fmt.Errorf("mux fanout: timed out waiting for an echoed chunk")
	}
}

func (p *chunkPipeline) Write(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	prev := p.pending.Add(int64(len(buf))) - int64(len(buf))
	cur := prev + int64(len(buf))
	for range cur/p.size - prev/p.size {
		select {
		case p.echo <- struct{}{}:
		default:
			return 0, fmt.Errorf("mux fanout: echo pipeline exceeded %d chunks", cap(p.echo))
		}
	}
	return len(buf), nil
}
