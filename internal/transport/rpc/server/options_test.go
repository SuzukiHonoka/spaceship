package server

import (
	"strings"
	"testing"

	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
)

func TestNormalizeServerOptionsDefaultsAndOverrides(t *testing.T) {
	defaults, err := normalizeServerOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.dnsExchange.MaxConcurrent != config.DefaultDNSMaxConcurrent {
		t.Fatalf("default max concurrency = %d", defaults.dnsExchange.MaxConcurrent)
	}
	if defaults.proxySessions.MaxConcurrent != config.DefaultProxyMaxConcurrent {
		t.Fatalf(
			"default proxy max concurrency = %d",
			defaults.proxySessions.MaxConcurrent,
		)
	}

	custom, err := normalizeServerOptions([]Option{
		WithDNSExchangeLimits(&config.DNSExchange{
			MaxConcurrent:        8,
			MaxConcurrentPerUser: 2,
			QueriesPerSecond:     20,
			QueriesPerUser:       5,
			Burst:                8,
			BurstPerUser:         2,
		}),
		WithProxySessionLimits(&config.ProxySessions{
			MaxConcurrent:           16,
			MaxConcurrentPerUser:    4,
			SessionsPerSecond:       40,
			SessionsPerUser:         10,
			Burst:                   16,
			BurstPerUser:            4,
			HandshakeTimeoutSeconds: 3,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if custom.dnsExchange.MaxConcurrent != 8 ||
		custom.dnsExchange.MaxConcurrentPerUser != 2 ||
		custom.dnsExchange.QueriesPerSecond != 20 ||
		custom.dnsExchange.QueriesPerUser != 5 {
		t.Fatalf("custom options = %+v", custom.dnsExchange)
	}
	if custom.proxySessions.MaxConcurrent != 16 ||
		custom.proxySessions.MaxConcurrentPerUser != 4 ||
		custom.proxySessions.SessionsPerSecond != 40 ||
		custom.proxySessions.HandshakeTimeoutSeconds != 3 {
		t.Fatalf("custom proxy options = %+v", custom.proxySessions)
	}
}

func TestNormalizeServerOptionsRejectsInvalidOptions(t *testing.T) {
	if _, err := normalizeServerOptions([]Option{nil}); err == nil ||
		!strings.Contains(err.Error(), "option 0 is nil") {
		t.Fatalf("nil option error = %v", err)
	}
	if _, err := normalizeServerOptions([]Option{
		WithDNSExchangeLimits(&config.DNSExchange{MaxConcurrent: -1}),
	}); err == nil || !strings.Contains(err.Error(), "max_concurrent") {
		t.Fatalf("invalid DNS limits error = %v", err)
	}
	if _, err := normalizeServerOptions([]Option{
		WithProxySessionLimits(&config.ProxySessions{HandshakeTimeoutSeconds: -1}),
	}); err == nil || !strings.Contains(err.Error(), "handshake_timeout_seconds") {
		t.Fatalf("invalid proxy limits error = %v", err)
	}
}
