package http

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/sniff"
)

func freeHTTPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitHTTP(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("http proxy not listening on %s", addr)
}

func TestHandleConnect_RoundTrip(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		{MatchType: router.TypeDefault, Destination: router.EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}

	origin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = origin.Close() })
	go func() {
		c, err := origin.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.Copy(c, c)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxy := New(ctx, &Config{})

	addr := freeHTTPAddr(t)
	errCh := make(chan error, 1)
	go func() { errCh <- proxy.ListenAndServe("tcp", addr) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
		}
	})
	waitHTTP(t, addr)

	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	target := origin.Addr().String()
	if _, err := conn.Write([]byte("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := nethttp.ReadResponse(br, &nethttp.Request{Method: nethttp.MethodConnect})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	payload := []byte("connect-unit-http")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo = %q, want %q", got, payload)
	}
}

func TestHandleConnect_DecidedDirectClosedPort(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		{MatchType: router.TypeDefault, Destination: router.EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = router.SetRoutes(router.Routes{{
			MatchType:   router.TypeDefault,
			Destination: router.EgressDirect,
		}})
	})

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	target := closed.Addr().String()
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxy := New(ctx, &Config{})

	addr := freeHTTPAddr(t)
	errCh := make(chan error, 1)
	go func() { errCh <- proxy.ListenAndServe("tcp", addr) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
		}
	})
	waitHTTP(t, addr)

	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}

	request := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := nethttp.ReadResponse(br, &nethttp.Request{Method: nethttp.MethodConnect})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == nethttp.StatusOK {
		t.Fatal("CONNECT replied 200 before the dial failed")
	}
	if resp.StatusCode != nethttp.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

// A client may send tunnel bytes right after the CONNECT request without
// waiting for the 200 (TLS ClientHello, "optimistic" SOCKS/HTTP clients). When
// those bytes arrive in the same segments as the request, net/http has already
// buffered them when the handler hijacks the connection, so the hijacked
// bufio.Reader must be drained into the tunnel rather than discarded.
func TestHandleConnect_EarlyDataBufferedByHijack(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		{MatchType: router.TypeDefault, Destination: router.EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}

	origin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = origin.Close() })
	go func() {
		c, err := origin.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.Copy(c, c)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxy := New(ctx, &Config{})

	addr := freeHTTPAddr(t)
	errCh := make(chan error, 1)
	go func() { errCh <- proxy.ListenAndServe("tcp", addr) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
		}
	})
	waitHTTP(t, addr)

	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	target := origin.Addr().String()
	early := []byte("early-tunnel-bytes")
	request := []byte("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n")
	// One write, so the early bytes share a segment with the request headers
	// and land in net/http's read buffer.
	if _, err := conn.Write(append(request, early...)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := nethttp.ReadResponse(br, &nethttp.Request{Method: nethttp.MethodConnect})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	late := []byte("-then-more")
	if _, err := conn.Write(late); err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte(nil), early...), late...)
	got := make([]byte, len(want))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("read echo: %v (got %q so far)", err, got)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("echo = %q, want %q", got, want)
	}
}

func TestHandleConnectIP_SniffedNameReplayedToOriginalIP(t *testing.T) {
	hello := sniff.BuildClientHello("sniff.example")
	received := make(chan []byte, 1)
	origin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = origin.Close() })
	go func() {
		c, acceptErr := origin.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, len(hello))
		if _, readErr := io.ReadFull(c, buf); readErr != nil {
			return
		}
		received <- buf
	}()

	if err := router.SetRoutes(router.Routes{{
		MatchType:   router.TypeExact,
		Sources:     []string{"sniff.example"},
		Destination: router.EgressDirect,
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = router.SetRoutes(router.Routes{{
			MatchType:   router.TypeDefault,
			Destination: router.EgressDirect,
		}})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxy := New(ctx, &Config{})
	addr := freeHTTPAddr(t)
	errCh := make(chan error, 1)
	go func() { errCh <- proxy.ListenAndServe("tcp", addr) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
		}
	})
	waitHTTP(t, addr)

	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	target := origin.Addr().String()
	request := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := nethttp.ReadResponse(br, &nethttp.Request{Method: nethttp.MethodConnect})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if _, err := conn.Write(hello); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-received:
		if !bytes.Equal(got, hello) {
			t.Fatal("origin did not receive the replayed ClientHello")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("origin did not receive the replayed ClientHello")
	}
}

// An exact name rule does not match the CONNECT target IP, so the sniff path
// answers 200 before dialing. A closed port then fails the dial. The bytes
// after that 200 must not be a second HTTP response.
func TestHandleConnectIP_DialFailureAfterEstablishedIsNotHTTP(t *testing.T) {
	hello := sniff.BuildClientHello("sniff.example")
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	target := closed.Addr().String()
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}

	if err := router.SetRoutes(router.Routes{{
		MatchType:   router.TypeExact,
		Sources:     []string{"sniff.example"},
		Destination: router.EgressDirect,
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = router.SetRoutes(router.Routes{{
			MatchType:   router.TypeDefault,
			Destination: router.EgressDirect,
		}})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxy := New(ctx, &Config{})
	addr := freeHTTPAddr(t)
	errCh := make(chan error, 1)
	go func() { errCh <- proxy.ListenAndServe("tcp", addr) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
		}
	})
	waitHTTP(t, addr)

	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}

	request := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := nethttp.ReadResponse(br, &nethttp.Request{Method: nethttp.MethodConnect})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("status = %d, want 200 before the dial", resp.StatusCode)
	}
	if _, err := conn.Write(hello); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 256)
	n, readErr := br.Read(buf)
	got := buf[:n]
	if bytes.HasPrefix(got, []byte("HTTP/1.")) || bytes.Contains(got, []byte("503 Service Unavailable")) {
		t.Fatalf("bytes after 200 = %q, want no second HTTP response", got)
	}
	if n == 0 {
		var netErr net.Error
		if errors.As(readErr, &netErr) && netErr.Timeout() {
			t.Fatal("timed out reading after CONNECT 200")
		}
		if !errors.Is(readErr, syscall.ECONNRESET) {
			t.Fatalf("connection ended with %v, want a reset", readErr)
		}
	}
}

func TestHandleConnect_InvalidHost(t *testing.T) {
	s := New(context.Background(), &Config{})
	req := httptest.NewRequest(nethttp.MethodConnect, "http://example.com", nil)
	req.Host = "example.com" // no port → SplitHostPort fails
	req.URL.Host = "example.com"
	rr := httptest.NewRecorder()
	s.handleConnect(rr, req)
	if rr.Code != nethttp.StatusServiceUnavailable && rr.Code != nethttp.StatusBadRequest && rr.Body.Len() == 0 {
		// ServeError writes 503 by default for proxy errors.
		if rr.Code == 0 {
			t.Fatal("handleConnect wrote nothing for invalid host")
		}
	}
}

func TestHandle_BadRequestEmptyHost(t *testing.T) {
	s := New(context.Background(), &Config{})
	req := httptest.NewRequest(nethttp.MethodGet, "/", nil)
	req.URL.Host = ""
	rr := httptest.NewRecorder()
	s.Handle(rr, req)
	if rr.Code != nethttp.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rr.Code)
	}
}

func TestHandle_ConnectNoRoute(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		{MatchType: router.TypeExact, Sources: []string{"only.this"}, Destination: router.EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = router.SetRoutes(router.Routes{
			{MatchType: router.TypeDefault, Destination: router.EgressDirect},
		})
	})

	s := New(context.Background(), &Config{})
	// httptest recorder does not support Hijack, so route-miss path is what we hit first.
	req := httptest.NewRequest(nethttp.MethodConnect, "http://missing.example:443", nil)
	req.Host = "missing.example:443"
	req.URL.Host = "missing.example:443"
	rr := httptest.NewRecorder()
	s.handleConnect(rr, req)
	if rr.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503 for missing route", rr.Code)
	}
}
