package http

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeError(t *testing.T) {
	t.Run("nil error is a no-op", func(t *testing.T) {
		var buf bytes.Buffer
		ServeError(&buf, nil)
		if buf.Len() != 0 {
			t.Errorf("wrote %q for nil error, want nothing", buf.String())
		}
	})

	t.Run("io.EOF is a no-op", func(t *testing.T) {
		var buf bytes.Buffer
		ServeError(&buf, io.EOF)
		if buf.Len() != 0 {
			t.Errorf("wrote %q for io.EOF, want nothing", buf.String())
		}
	})

	t.Run("ResponseWriter gets 503", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ServeError(rec, errors.New("boom"))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
	})

	t.Run("plain io.Writer gets raw 503 message", func(t *testing.T) {
		var buf bytes.Buffer
		ServeError(&buf, errors.New("boom"))
		if !strings.Contains(buf.String(), "503") {
			t.Errorf("response %q does not contain 503 status line", buf.String())
		}
	})

	t.Run("nil writer does not panic", func(t *testing.T) {
		ServeError(nil, errors.New("boom"))
	})

	t.Run("context.Canceled is a no-op", func(t *testing.T) {
		var buf bytes.Buffer
		ServeError(&buf, context.Canceled)
		if buf.Len() != 0 {
			t.Errorf("wrote %q for context.Canceled, want nothing", buf.String())
		}
	})
}

func TestServeProxyError_Writes503WithHost(t *testing.T) {
	var buf bytes.Buffer
	ServeProxyError(&buf, "199.96.58.85:443", errors.New("server ack timeout"))
	if !strings.Contains(buf.String(), "503") {
		t.Fatalf("response %q missing 503", buf.String())
	}
}

func TestServeProxyError_CanceledSkipsResponse(t *testing.T) {
	var buf bytes.Buffer
	ServeProxyError(&buf, "example.com:443", context.Canceled)
	if buf.Len() != 0 {
		t.Fatalf("wrote %q for canceled, want empty", buf.String())
	}
}

func TestBuildRemoteAddr(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		scheme   string
		wantHost string
		wantAddr string
		wantErr  bool
	}{
		{"host with port", "example.com:8080", "", "example.com", "example.com:8080", false},
		{"host without port defaults to 80", "example.com", "", "example.com", "example.com:80", false},
		{"scheme-derived port", "example.com", "http", "example.com", "example.com:80", false},
		{"bracketed ipv6 without port", "[2001:db8::1]", "http", "2001:db8::1", "[2001:db8::1]:80", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/", nil)
			req.Host = tt.host
			if tt.scheme != "" {
				req.URL.Scheme = tt.scheme
			}
			host, addr, err := BuildRemoteAddr(req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if host != tt.wantHost || addr != tt.wantAddr {
				t.Errorf("BuildRemoteAddr() = (%q, %q), want (%q, %q)", host, addr, tt.wantHost, tt.wantAddr)
			}
		})
	}
}

func TestWriteForwardRequest(t *testing.T) {
	const body = "request body"
	req := httptest.NewRequest(http.MethodPost, "http://example.com:8080/search?q=spaceship&page=1", strings.NewReader(body))
	req.Header.Set("Connection", "keep-alive, X-Remove")
	req.Header.Set("Expect", "100-continue")
	req.Header.Set("Proxy-Authorization", "Basic secret")
	req.Header.Set("X-Remove", "hop value")
	req.Header.Add("X-Test", "one")
	req.Header.Add("X-Test", "two")

	var buf bytes.Buffer
	if err := writeForwardRequest(&buf, req, "example.com"); err != nil {
		t.Fatalf("writeForwardRequest() error = %v", err)
	}

	raw := buf.String()
	if !strings.HasPrefix(raw, "POST /search?q=spaceship&page=1 HTTP/1.1\r\n") {
		t.Fatalf("forwarded request line = %q", raw)
	}
	if strings.Contains(raw, "Proxy-Authorization:") || strings.Contains(raw, "X-Remove:") || strings.Contains(raw, "Expect:") {
		t.Fatalf("forwarded request leaked hop/proxy headers: %q", raw)
	}

	parsed, err := http.ReadRequest(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("forwarded request is not valid HTTP: %v\n%s", err, raw)
	}
	defer parsed.Body.Close()
	gotBody, err := io.ReadAll(parsed.Body)
	if err != nil {
		t.Fatalf("read forwarded body: %v", err)
	}
	if string(gotBody) != body {
		t.Fatalf("forwarded body = %q, want %q", gotBody, body)
	}
	if parsed.Host != "example.com:8080" {
		t.Fatalf("forwarded Host = %q", parsed.Host)
	}
	if !parsed.Close {
		t.Fatal("forwarded request must close the origin connection")
	}
	if got := parsed.Header.Values("X-Test"); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("forwarded X-Test values = %q", got)
	}
}

func TestWriteForwardRequestReencodesChunkedBody(t *testing.T) {
	const body = "chunked request body"
	req := httptest.NewRequest(http.MethodPost, "http://example.com/upload", strings.NewReader(body))
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}

	var buf bytes.Buffer
	if err := writeForwardRequest(&buf, req, "example.com"); err != nil {
		t.Fatalf("writeForwardRequest() error = %v", err)
	}
	if !strings.Contains(buf.String(), "Transfer-Encoding: chunked\r\n") {
		t.Fatalf("chunked framing missing:\n%s", buf.String())
	}

	parsed, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(buf.Bytes())))
	if err != nil {
		t.Fatalf("parse forwarded chunked request: %v", err)
	}
	defer parsed.Body.Close()
	got, err := io.ReadAll(parsed.Body)
	if err != nil {
		t.Fatalf("read chunked body: %v", err)
	}
	if string(got) != body {
		t.Fatalf("chunked body = %q, want %q", got, body)
	}
}
