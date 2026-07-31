package server

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeProxySessionsDefaults(t *testing.T) {
	cfg, err := NormalizeProxySessions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConcurrent != DefaultProxyMaxConcurrent ||
		cfg.MaxConcurrentPerUser != DefaultProxyMaxConcurrentPerUser ||
		cfg.SessionsPerSecond != DefaultProxySessionsPerSecond ||
		cfg.SessionsPerUser != DefaultProxySessionsPerUser ||
		cfg.Burst != DefaultProxyBurst ||
		cfg.BurstPerUser != DefaultProxyBurstPerUser ||
		time.Duration(cfg.HandshakeTimeoutSeconds)*time.Second != DefaultProxyHandshakeTimeout {
		t.Fatalf("NormalizeProxySessions(nil) = %+v", cfg)
	}
}

func TestNormalizeProxySessionsPreservesValidValues(t *testing.T) {
	raw := &ProxySessions{
		MaxConcurrent:           20,
		MaxConcurrentPerUser:    10,
		SessionsPerSecond:       40,
		SessionsPerUser:         15,
		Burst:                   12,
		BurstPerUser:            4,
		HandshakeTimeoutSeconds: 7,
	}
	got, err := NormalizeProxySessions(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != *raw {
		t.Fatalf("NormalizeProxySessions() = %+v, want %+v", got, *raw)
	}
}

func TestNormalizeProxySessionsClampsOmittedPerUserDefaultsToCustomGlobals(t *testing.T) {
	cfg, err := NormalizeProxySessions(&ProxySessions{
		MaxConcurrent:     2,
		SessionsPerSecond: 3,
		Burst:             1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConcurrentPerUser != 2 ||
		cfg.SessionsPerUser != 3 ||
		cfg.BurstPerUser != 1 {
		t.Fatalf("NormalizeProxySessions() = %+v", cfg)
	}
}

func TestNormalizeProxySessionsCapsImplicitBurstsAtReducedRates(t *testing.T) {
	cfg, err := NormalizeProxySessions(&ProxySessions{
		SessionsPerSecond: 5,
		SessionsPerUser:   2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Burst != 5 || cfg.BurstPerUser != 2 {
		t.Fatalf("implicit rate-adjusted bursts = (%d, %d), want (5, 2)", cfg.Burst, cfg.BurstPerUser)
	}

	explicit, err := NormalizeProxySessions(&ProxySessions{
		SessionsPerSecond: 5,
		SessionsPerUser:   2,
		Burst:             20,
		BurstPerUser:      10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Burst != 20 || explicit.BurstPerUser != 10 {
		t.Fatalf("explicit bursts = (%d, %d), want (20, 10)", explicit.Burst, explicit.BurstPerUser)
	}
}

func TestNormalizeProxySessionsRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name string
		cfg  ProxySessions
		want string
	}{
		{
			name: "negative concurrency",
			cfg:  ProxySessions{MaxConcurrent: -1},
			want: "max_concurrent",
		},
		{
			name: "excessive global concurrency",
			cfg:  ProxySessions{MaxConcurrent: maxProxyConcurrent + 1},
			want: "max_concurrent",
		},
		{
			name: "excessive rate",
			cfg:  ProxySessions{SessionsPerSecond: maxProxyRate + 1},
			want: "new_sessions_per_second",
		},
		{
			name: "excessive handshake timeout",
			cfg:  ProxySessions{HandshakeTimeoutSeconds: maxProxyHandshakeSeconds + 1},
			want: "handshake_timeout_seconds",
		},
		{
			name: "per-user concurrency exceeds global",
			cfg: ProxySessions{
				MaxConcurrent:        1,
				MaxConcurrentPerUser: 2,
			},
			want: "must not exceed max_concurrent",
		},
		{
			name: "per-user rate exceeds global",
			cfg: ProxySessions{
				SessionsPerSecond: 1,
				SessionsPerUser:   2,
			},
			want: "must not exceed new_sessions_per_second",
		},
		{
			name: "per-user burst exceeds global",
			cfg: ProxySessions{
				Burst:        1,
				BurstPerUser: 2,
			},
			want: "must not exceed burst",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizeProxySessions(&tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NormalizeProxySessions() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}
