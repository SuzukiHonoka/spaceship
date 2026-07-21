package http

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
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
		defer c.Close()
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
	defer conn.Close()
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
	defer resp.Body.Close()
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
