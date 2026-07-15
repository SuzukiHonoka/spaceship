package http

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
)

// Smoke: a reconstructed plain-proxy request is valid HTTP, including body
// framing, query strings, and hop-by-hop header removal.
func TestSmoke_ForwardRequest(t *testing.T) {
	body := strings.Repeat("x", 8*1024)
	req := httptest.NewRequest(http.MethodPost, "http://Example.COM:8080/upload?part=1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Proxy-Authorization", "Basic secret")
	req.Header.Set("X-Custom", "yes")
	req.ContentLength = int64(len(body))

	var out bytes.Buffer
	if err := writeForwardRequest(&out, req, "example.com"); err != nil {
		t.Fatalf("writeForwardRequest: %v", err)
	}

	raw := out.String()
	headerEnd := strings.Index(raw, "\r\n\r\n")
	if headerEnd < 0 {
		t.Fatal("missing header terminator")
	}
	headers := raw[:headerEnd]
	gotBody := raw[headerEnd+4:]

	if !strings.HasPrefix(headers, "POST /upload?part=1 HTTP/1.1") {
		t.Fatalf("request line = %q", headers)
	}
	if !strings.Contains(headers, "Host: Example.COM:8080") {
		t.Fatalf("missing Host header in %q", headers)
	}
	if !strings.Contains(headers, "X-Custom: yes") {
		t.Fatalf("missing custom header in %q", headers)
	}
	if strings.Contains(headers, "Proxy-Authorization") {
		t.Fatalf("hop-by-hop headers leaked: %q", headers)
	}
	if !strings.Contains(headers, "Connection: close") {
		t.Fatalf("forwarded request must close the origin connection: %q", headers)
	}
	if gotBody != body {
		t.Fatalf("body length = %d, want %d (headers must not corrupt body)", len(gotBody), len(body))
	}
	// Headers must fully precede body: first body byte appears only after \r\n\r\n.
	if idx := strings.Index(raw, body[:32]); idx < headerEnd {
		t.Fatalf("body content appears before header terminator at %d (headerEnd=%d)", idx, headerEnd)
	}
}

func TestHandleRequestDoesNotForwardPipelinedRequest(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		{Destination: router.EgressDirect, MatchType: router.TypeDefault},
	}); err != nil {
		t.Fatal(err)
	}

	firstHeaders := make(chan http.Header, 1)
	firstOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHeaders <- r.Header.Clone()
		_, _ = io.WriteString(w, "first response")
	}))
	defer firstOrigin.Close()

	secondRequest := make(chan struct{}, 1)
	secondOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondRequest <- struct{}{}
		_, _ = io.WriteString(w, "second response")
	}))
	defer secondOrigin.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxy := New(ctx, &Config{Credentials: StaticCredentials{"user": "pass"}})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyHTTP := &http.Server{Handler: proxy.proxyAuth(http.HandlerFunc(proxy.Handle))}
	go func() {
		_ = proxyHTTP.Serve(listener)
	}()
	defer func() {
		_ = proxyHTTP.Close()
		_ = listener.Close()
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	firstHost := strings.TrimPrefix(firstOrigin.URL, "http://")
	secondHost := strings.TrimPrefix(secondOrigin.URL, "http://")
	_, err = fmt.Fprintf(conn,
		"GET %s/first HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic dXNlcjpwYXNz\r\nConnection: keep-alive\r\n\r\n"+
			"GET %s/second HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n",
		firstOrigin.URL, firstHost, secondOrigin.URL, secondHost,
	)
	if err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read first proxy response: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "first response" {
		t.Fatalf("response body = %q", body)
	}
	if !response.Close {
		t.Fatal("proxy response did not close the client connection")
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("proxy connection remained open or returned extra data: %v", err)
	}

	select {
	case headers := <-firstHeaders:
		if headers.Get("Proxy-Authorization") != "" {
			t.Fatal("proxy credentials reached the origin")
		}
	case <-time.After(time.Second):
		t.Fatal("first origin did not receive the request")
	}

	select {
	case <-secondRequest:
		t.Fatal("pipelined request reached the second origin")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHandleRequestSupportsExpectContinue(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		{Destination: router.EgressDirect, MatchType: router.TypeDefault},
	}); err != nil {
		t.Fatal(err)
	}

	type receivedRequest struct {
		body   string
		expect string
	}
	received := make(chan receivedRequest, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received <- receivedRequest{body: string(body), expect: r.Header.Get("Expect")}
		_, _ = io.WriteString(w, "accepted")
	}))
	defer origin.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxy := New(ctx, &Config{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyHTTP := &http.Server{Handler: http.HandlerFunc(proxy.Handle)}
	go func() { _ = proxyHTTP.Serve(listener) }()
	defer func() {
		_ = proxyHTTP.Close()
		_ = listener.Close()
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	host := strings.TrimPrefix(origin.URL, "http://")
	const body = "request body"
	if _, err := fmt.Fprintf(conn,
		"POST %s/upload HTTP/1.1\r\nHost: %s\r\nExpect: 100-continue\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		origin.URL, host, len(body),
	); err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(conn)
	request := &http.Request{Method: http.MethodPost}
	interim, err := http.ReadResponse(reader, request)
	if err != nil {
		t.Fatalf("read 100 Continue: %v", err)
	}
	_ = interim.Body.Close()
	if interim.StatusCode != http.StatusContinue {
		t.Fatalf("interim status = %d, want 100", interim.StatusCode)
	}
	if _, err := io.WriteString(conn, body); err != nil {
		t.Fatal(err)
	}

	response, err := http.ReadResponse(reader, request)
	if err != nil {
		t.Fatalf("read final response: %v", err)
	}
	responseBody, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(responseBody) != "accepted" {
		t.Fatalf("final response = %d %q, want 200 accepted", response.StatusCode, responseBody)
	}

	select {
	case got := <-received:
		if got.body != body {
			t.Fatalf("origin body = %q, want %q", got.body, body)
		}
		if got.expect != "" {
			t.Fatalf("origin Expect header = %q, want removed", got.expect)
		}
	case <-time.After(time.Second):
		t.Fatal("origin did not receive the request")
	}
}

func TestListenAndServeReturnsContextCancellationOnShutdown(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	ctx, cancel := context.WithCancel(context.Background())
	server := New(ctx, &Config{})
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe("tcp", addr) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("HTTP server did not start: %v", dialErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ListenAndServe() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP server did not stop after context cancellation")
	}
}
