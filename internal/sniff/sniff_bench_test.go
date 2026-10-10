package sniff

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// These benchmarks measure the cost added when a destination is an IP.
// A connection that already carries a hostname never calls Peek.

func BenchmarkClassify(b *testing.B) {
	nonHTTP := bytes.Repeat([]byte{0x11}, 1400)
	tlsHello := BuildClientHello("www.example.com")
	httpReq := []byte("GET / HTTP/1.1\r\nHost: www.example.com\r\n\r\n")

	b.Run("plain-read-1400", func(b *testing.B) {
		benchRead(b, nonHTTP)
	})
	b.Run("sniff-nonhttp-1400", func(b *testing.B) {
		benchPeek(b, nonHTTP)
	})
	b.Run("plain-read-tls", func(b *testing.B) {
		benchRead(b, tlsHello)
	})
	b.Run("sniff-tls", func(b *testing.B) {
		benchPeek(b, tlsHello)
	})
	b.Run("sniff-http", func(b *testing.B) {
		benchPeek(b, httpReq)
	})
	b.Run("pipe-plain-1400", func(b *testing.B) {
		benchPipe(b, nonHTTP, false)
	})
	b.Run("pipe-sniff-1400", func(b *testing.B) {
		benchPipe(b, nonHTTP, true)
	})
}

func BenchmarkFlow(b *testing.B) {
	for _, packets := range []int{1, 32, 256} {
		b.Run(packetName(packets)+"/plain", func(b *testing.B) {
			benchTCPFlow(b, packets, false)
		})
		b.Run(packetName(packets)+"/sniff", func(b *testing.B) {
			benchTCPFlow(b, packets, true)
		})
	}
}

func packetName(n int) string {
	switch n {
	case 1:
		return "1pkt"
	case 32:
		return "32pkt"
	default:
		return "256pkt"
	}
}

func benchRead(b *testing.B, packet []byte) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	for range b.N {
		if _, err := io.Copy(io.Discard, bytes.NewReader(packet)); err != nil {
			b.Fatal(err)
		}
	}
	reportRate(b, start, 1)
}

func benchPeek(b *testing.B, packet []byte) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	for range b.N {
		host, replay := Peek(bytes.NewReader(packet), time.Second)
		if _, err := io.Copy(io.Discard, replay); err != nil {
			b.Fatal(err)
		}
		if len(packet) > 0 && packet[0] == tlsHandshake && host == "" {
			b.Fatal("tls hello was not classified")
		}
	}
	reportRate(b, start, 1)
}

func benchPipe(b *testing.B, packet []byte, sniff bool) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	for range b.N {
		server, client := net.Pipe()
		errCh := make(chan error, 1)
		go func() {
			_, err := client.Write(packet)
			_ = client.Close()
			errCh <- err
		}()
		var src io.Reader = server
		if sniff {
			_, src = Peek(server, time.Second)
		}
		if _, err := io.Copy(io.Discard, src); err != nil {
			b.Fatal(err)
		}
		if err := <-errCh; err != nil {
			b.Fatal(err)
		}
		_ = server.Close()
	}
	reportRate(b, start, 1)
}

func benchTCPFlow(b *testing.B, packets int, sniff bool) {
	b.Helper()
	const size = 1400
	packet := bytes.Repeat([]byte{0x11}, size)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = ln.Close() })

	done := make(chan error, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFlow(conn, sniff, done)
		}
	}()

	b.SetBytes(int64(packets) * size)
	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	for range b.N {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			b.Fatal(err)
		}
		for range packets {
			if _, err := conn.Write(packet); err != nil {
				b.Fatal(err)
			}
		}
		if err := conn.Close(); err != nil {
			b.Fatal(err)
		}
		if err := <-done; err != nil {
			b.Fatal(err)
		}
	}
	reportRate(b, start, packets)
}

func serveFlow(conn net.Conn, sniff bool, done chan<- error) {
	defer func() { _ = conn.Close() }()
	var src io.Reader = conn
	if sniff {
		_, src = Peek(conn, time.Second)
	}
	_, err := io.Copy(io.Discard, src)
	done <- err
}

func reportRate(b *testing.B, start time.Time, packetsPerOp int) {
	b.Helper()
	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 {
		return
	}
	b.ReportMetric(float64(b.N)*float64(packetsPerOp)/elapsed, "pkt/s")
}
