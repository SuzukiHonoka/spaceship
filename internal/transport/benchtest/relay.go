package benchtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
)

// RelaySizes is the chunk-size sweep used by Relay sub-benchmarks: a 1 MiB
// bulk write exercises the copy path, while 16 KiB matches a TLS record and
// is what interactive protocols send in practice.
var RelaySizes = []int{16 << 10, 1 << 20}

// Relay measures the exact shape SOCKS, HTTP CONNECT, and redirect produce:
// the client is a real loopback TCP socket, the accepted side is wrapped with
// utils.OnceNetConn and handed to Proxy as both dst and src. TUN is a netstack
// connection and is not this path.
// Unlike Stream, which feeds an io.Pipe, this reaches the kernel socket on
// both ends and therefore measures the splice (Linux) or pooled-buffer copy
// path end to end, including the egress hop. One operation is a write of
// size bytes followed by its echo; MB/s counts both legs.
func Relay(b *testing.B, p proxy, addr string, size int) {
	b.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	proxySide, ok := <-accepted
	if !ok {
		b.Fatal("accept failed")
	}
	defer func() { _ = proxySide.Close() }()

	errCh := make(chan error, 1)
	go func() {
		conn := utils.OnceNetConn(proxySide)
		errCh <- p.Proxy(ctx, addr, make(chan string, 1), conn, conn)
	}()

	payload := bytes.Repeat([]byte{0x5a}, size)
	echo := make([]byte, size)
	// A reader goroutine drains the echo so the writer is never blocked by
	// loopback socket buffers, which would serialise the two legs.
	readErr := make(chan error, 1)
	rounds := make(chan int, 1)
	go func() {
		for n := range rounds {
			for range n {
				if _, err := io.ReadFull(client, echo); err != nil {
					readErr <- err
					return
				}
			}
			readErr <- nil
		}
	}()
	run := func(n int) {
		b.Helper()
		rounds <- n
		for range n {
			if _, err := client.Write(payload); err != nil {
				b.Fatal(err)
			}
		}
		select {
		case err := <-readErr:
			if err != nil {
				b.Fatal(err)
			}
		case err := <-errCh:
			b.Fatalf("proxy ended before echo completed: %v", err)
		case <-ctx.Done():
			b.Fatal(ctx.Err())
		}
	}

	run(4)
	b.SetBytes(int64(2 * size))
	b.ReportAllocs()
	b.ResetTimer()
	run(b.N)
	b.StopTimer()
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/s")
	close(rounds)

	_ = client.(*net.TCPConn).CloseWrite()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, io.EOF) {
			b.Fatal(err)
		}
	case <-ctx.Done():
		b.Fatal(ctx.Err())
	}
}

// RelaySweep runs Relay once per RelaySizes entry as sub-benchmarks.
func RelaySweep(b *testing.B, p proxy, addr string) {
	b.Helper()
	for _, size := range RelaySizes {
		b.Run(fmt.Sprintf("%d", size), func(b *testing.B) {
			Relay(b, p, addr, size)
		})
	}
}
