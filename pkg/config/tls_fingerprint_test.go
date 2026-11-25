package config

import (
	"testing"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

func TestApplyTLSFingerprintValidation(t *testing.T) {
	t.Cleanup(func() {
		transport.EnableIPv6()
	})

	t.Run("valid client fingerprint", func(t *testing.T) {
		cfg, err := NewFromString(`{
			"role": "client",
			"log": "skip",
			"uuid": "00000000-0000-0000-0000-000000000001",
			"tls": true,
			"tls_fingerprint": "firefox"
		}`)
		if err != nil {
			t.Fatalf("NewFromString() error = %v", err)
		}
		if cfg.TLSFingerprint != "firefox" {
			t.Fatalf("TLSFingerprint = %q, want firefox", cfg.TLSFingerprint)
		}
		if err := cfg.Apply(); err != nil {
			t.Fatalf("Apply() error = %v", err)
		}
	})

	t.Run("invalid client fingerprint rejected", func(t *testing.T) {
		cfg, err := NewFromString(`{
			"role": "client",
			"log": "skip",
			"uuid": "00000000-0000-0000-0000-000000000001",
			"tls": true,
			"tls_fingerprint": "not-a-browser"
		}`)
		if err != nil {
			t.Fatalf("NewFromString() error = %v", err)
		}
		if err := cfg.Apply(); err == nil {
			t.Fatal("Apply() expected error for unsupported tls_fingerprint, got nil")
		}
	})

	t.Run("server role rejects tls_fingerprint", func(t *testing.T) {
		cfg, err := NewFromString(`{
			"role": "server",
			"log": "skip",
			"tls_fingerprint": "chrome"
		}`)
		if err != nil {
			t.Fatalf("NewFromString() error = %v", err)
		}
		if err := cfg.Apply(); err == nil {
			t.Fatal("Apply() expected error when tls_fingerprint is set on server role, got nil")
		}
	})
}
