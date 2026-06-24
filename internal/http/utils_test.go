package http

import (
	"bytes"
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
