package utils

import (
	"errors"
	"testing"
)

type mockCloser struct {
	err error
}

func (m *mockCloser) Close() error {
	return m.err
}

func TestClose(t *testing.T) {
	// Should not panic
	Close(&mockCloser{nil})
	Close(&mockCloser{errors.New("fail")})
}

func TestPrettyByteSize(t *testing.T) {
	tests := []struct {
		b    float64
		want string
	}{
		{512, "512B"},
		{1024, "1.00KiB"},
		{1024 * 1024, "1.00MiB"},
		{1.5 * 1024 * 1024 * 1024, "1.50GiB"},
	}
	for _, tt := range tests {
		if got := PrettyByteSize(tt.b); got != tt.want {
			t.Errorf("PrettyByteSize(%v) = %v, want %v", tt.b, got, tt.want)
		}
	}
}

func TestSplitHostPort(t *testing.T) {
	tests := []struct {
		s       string
		wantH   string
		wantP   uint16
		wantErr bool
	}{
		{"localhost:80", "localhost", 80, false},
		{"127.0.0.1:443", "127.0.0.1", 443, false},
		{"[::1]:8080", "::1", 8080, false},
		{"localhost:99999", "", 0, true},
		{"localhost", "", 0, true},
		{"localhost:abc", "", 0, true},
	}
	for _, tt := range tests {
		h, p, err := SplitHostPort(tt.s)
		if (err != nil) != tt.wantErr {
			t.Errorf("SplitHostPort(%q) error = %v, wantErr %v", tt.s, err, tt.wantErr)
			continue
		}
		if !tt.wantErr {
			if h != tt.wantH || p != tt.wantP {
				t.Errorf("SplitHostPort(%q) = %v, %v; want %v, %v", tt.s, h, p, tt.wantH, tt.wantP)
			}
		}
	}
}
