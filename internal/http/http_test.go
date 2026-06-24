package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestServer_ProxyAuth(t *testing.T) {
	cfg := &Config{
		Credentials: StaticCredentials{"user": "pass"},
	}
	s := New(context.Background(), cfg)

	handler := s.proxyAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 1. No auth
	req := httptest.NewRequest("CONNECT", "example.com:443", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusProxyAuthRequired {
		t.Errorf("expected StatusProxyAuthRequired, got %v", rr.Code)
	}

	// 2. Wrong auth
	req = httptest.NewRequest("CONNECT", "example.com:443", nil)
	req.SetBasicAuth("user", "wrong")
	// SetBasicAuth sets "Authorization" header, but we need "Proxy-Authorization"
	req.Header.Set("Proxy-Authorization", req.Header.Get("Authorization"))
	req.Header.Del("Authorization")

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusProxyAuthRequired {
		t.Errorf("expected StatusProxyAuthRequired for wrong pass, got %v", rr.Code)
	}

	// 3. Correct auth
	req = httptest.NewRequest("CONNECT", "example.com:443", nil)
	req.SetBasicAuth("user", "pass")
	req.Header.Set("Proxy-Authorization", req.Header.Get("Authorization"))
	req.Header.Del("Authorization")

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected StatusOK for correct auth, got %v", rr.Code)
	}
}

func TestServer_ListenAndServe_Cancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, &Config{})

	// Run in a goroutine
	done := make(chan error, 1)
	go func() {
		done <- s.ListenAndServe("tcp", "127.0.0.1:0")
	}()

	// Wait a bit for it to start
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for server to stop")
	}
}

func TestParseBasicAuth(t *testing.T) {
	tests := []struct {
		name string
		auth string
		user string
		pass string
		ok   bool
	}{
		{"valid", "Basic dXNlcjpwYXNz", "user", "pass", true},
		{"invalid prefix", "Digest dXNlcjpwYXNz", "", "", false},
		{"invalid base64", "Basic invalid!!!", "", "", false},
		{"missing colon", "Basic dXNlcg==", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, pass, ok := parseBasicAuth(tt.auth)
			if ok != tt.ok {
				t.Errorf("ok = %v, want %v", ok, tt.ok)
			}
			if ok {
				if user != tt.user || pass != tt.pass {
					t.Errorf("user:pass = %v:%v, want %v:%v", user, pass, tt.user, tt.pass)
				}
			}
		})
	}
}

func TestServer_Handle_NotConnect(t *testing.T) {
	s := New(context.Background(), &Config{})
	req := httptest.NewRequest("GET", "http://example.com", nil)
	rr := httptest.NewRecorder()
	s.Handle(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected StatusServiceUnavailable for GET with no route, got %v", rr.Code)
	}
}
