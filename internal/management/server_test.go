package management

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIPIsLoopback(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"127.0.0.1:19999", true},
		{"127.0.0.1", true},
		{"[::1]:19999", true},
		{"::1", true},
		{"8.8.8.8:53", false},
		{"0.0.0.0:19999", false},
		{"example.com:80", false},
		{"localhost:80", false}, // hostnames are not loopback IPs
		{"garbage", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ipIsLoopback(tt.in); got != tt.want {
			t.Errorf("ipIsLoopback(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestHostHeaderAllowed(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.1:19999", true},
		{"[::1]:19999", true},
		{"localhost", true},
		{"localhost:19999", true},
		{"LocalHost:19999", true}, // case-insensitive
		{"evil.com", false},
		{"169.254.169.254", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := hostHeaderAllowed(tt.in); got != tt.want {
			t.Errorf("hostHeaderAllowed(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestLoopbackGuard(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		host       string
		wantStatus int
	}{
		{"loopback ok", "127.0.0.1:5555", "127.0.0.1:19999", http.StatusOK},
		{"localhost host ok", "127.0.0.1:5555", "localhost:19999", http.StatusOK},
		{"non-loopback remote", "8.8.8.8:5555", "127.0.0.1:19999", http.StatusForbidden},
		{"rebinding host", "127.0.0.1:5555", "evil.com", http.StatusForbidden},
		{"ipv6 loopback ok", "[::1]:5555", "[::1]:19999", http.StatusOK},
	}

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	guard := loopbackGuard(ok)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
			req.RemoteAddr = tt.remoteAddr
			req.Host = tt.host
			rec := httptest.NewRecorder()
			guard.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestHandleStats_OK(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	rec := httptest.NewRecorder()
	handleStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var resp StatsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid StatsResponse JSON: %v", err)
	}
}

func TestHandleStats_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/stats", nil)
	rec := httptest.NewRecorder()
	handleStats(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestHandleHealth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("health body not valid JSON: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want ok", body.Status)
	}
}

func TestHandleHealth_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/api/health", nil)
	rec := httptest.NewRecorder()
	handleHealth(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestStart_RejectsNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:19999", "8.8.8.8:19999", "example.com:80"} {
		if err := Start(context.Background(), addr); err == nil {
			t.Errorf("Start(%q) = nil, want error for non-loopback bind", addr)
		}
	}
}

// TestServe_EndToEnd binds a real loopback listener and exercises the full
// server (timeouts, guard, routes) plus graceful shutdown on context cancel.
func TestServe_EndToEnd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- serve(ctx, ln) }()

	base := "http://" + ln.Addr().String()
	client := &http.Client{Timeout: 2 * time.Second}

	// Health endpoint should be reachable and return 200.
	resp, err := client.Get(base + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health status = %d, want 200 (body=%s)", resp.StatusCode, body)
	}

	// Stats endpoint should return JSON.
	resp, err = client.Get(base + "/api/stats")
	if err != nil {
		t.Fatalf("GET /api/stats: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("stats status = %d, want 200", resp.StatusCode)
	}

	// A forged (non-loopback) Host header must be rejected even over a loopback conn.
	req, _ := http.NewRequest(http.MethodGet, base+"/api/stats", nil)
	req.Host = "evil.com"
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET with forged host: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("forged-host status = %d, want 403", resp.StatusCode)
	}

	// Cancel context -> graceful shutdown -> serve returns nil.
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("serve() returned %v, want nil after shutdown", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve() did not return after context cancellation")
	}
}

// TestStart_BindsAndShutsDown covers the Start() success path: validate, bind a
// real ephemeral loopback port, serve, and shut down cleanly on cancel.
func TestStart_BindsAndShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Start(ctx, "127.0.0.1:0") }()

	time.Sleep(100 * time.Millisecond) // let it bind and serve
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Start() = %v, want nil after shutdown", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start() did not return after context cancellation")
	}
}

// failingResponseWriter fails every Write, to exercise the encode error branch.
type failingResponseWriter struct{ header http.Header }

func (f *failingResponseWriter) Header() http.Header        { return f.header }
func (f *failingResponseWriter) Write([]byte) (int, error)  { return 0, io.ErrClosedPipe }
func (f *failingResponseWriter) WriteHeader(statusCode int) {}

func TestHandleStats_EncodeErrorIsHandled(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	w := &failingResponseWriter{header: make(http.Header)}
	// Must not panic even when the response writer fails.
	handleStats(w, req)
}
