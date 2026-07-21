package http

import (
	"net/http"
	"testing"
)

func TestFilterContains(t *testing.T) {
	f := Filter{"Connection", "Keep-Alive"}
	if !f.Contains("connection") {
		t.Error("Contains should be case-insensitive for Connection")
	}
	if !f.Contains("Keep-Alive") {
		t.Error("Contains missed Keep-Alive")
	}
	if f.Contains("Authorization") {
		t.Error("Contains false positive for Authorization")
	}
}

func TestRemoveHopHeaders(t *testing.T) {
	h := make(http.Header)
	h.Set("Connection", "Upgrade, Te")
	h.Set("Upgrade", "websocket")
	h.Set("Te", "trailers")
	h.Set("X-Custom", "keep")
	h.Set("Keep-Alive", "timeout=5")

	hopHeaders.RemoveHopHeaders(h)

	if h.Get("Upgrade") != "" || h.Get("Te") != "" || h.Get("Keep-Alive") != "" {
		t.Fatalf("hop headers still present: %v", h)
	}
	if h.Get("X-Custom") != "keep" {
		t.Fatalf("custom header lost: %v", h)
	}
}
