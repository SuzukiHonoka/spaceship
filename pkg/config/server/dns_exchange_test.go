package server

import (
	"strings"
	"testing"
)

func TestNormalizeDNSExchangeDefaults(t *testing.T) {
	cfg, err := NormalizeDNSExchange(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConcurrent != DefaultDNSMaxConcurrent ||
		cfg.MaxConcurrentPerUser != DefaultDNSMaxConcurrentPerUser ||
		cfg.QueriesPerSecond != DefaultDNSQueriesPerSecond ||
		cfg.QueriesPerUser != DefaultDNSQueriesPerUser ||
		cfg.Burst != DefaultDNSBurst ||
		cfg.BurstPerUser != DefaultDNSBurstPerUser {
		t.Fatalf("NormalizeDNSExchange(nil) = %+v", cfg)
	}
}

func TestNormalizeDNSExchangePreservesValidValues(t *testing.T) {
	raw := &DNSExchange{
		MaxConcurrent:        20,
		MaxConcurrentPerUser: 10,
		QueriesPerSecond:     40,
		QueriesPerUser:       15,
		Burst:                12,
		BurstPerUser:         4,
	}
	got, err := NormalizeDNSExchange(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != *raw {
		t.Fatalf("NormalizeDNSExchange() = %+v, want %+v", got, *raw)
	}
}

func TestNormalizeDNSExchangeClampsOmittedPerUserDefaultsToCustomGlobals(t *testing.T) {
	cfg, err := NormalizeDNSExchange(&DNSExchange{
		MaxConcurrent:    2,
		QueriesPerSecond: 3,
		Burst:            1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConcurrentPerUser != 2 ||
		cfg.QueriesPerUser != 3 ||
		cfg.BurstPerUser != 1 {
		t.Fatalf("NormalizeDNSExchange() = %+v", cfg)
	}
}

func TestNormalizeDNSExchangeCapsImplicitBurstsAtReducedRates(t *testing.T) {
	cfg, err := NormalizeDNSExchange(&DNSExchange{
		QueriesPerSecond: 5,
		QueriesPerUser:   2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Burst != 5 || cfg.BurstPerUser != 2 {
		t.Fatalf("implicit rate-adjusted bursts = (%d, %d), want (5, 2)", cfg.Burst, cfg.BurstPerUser)
	}

	explicit, err := NormalizeDNSExchange(&DNSExchange{
		QueriesPerSecond: 5,
		QueriesPerUser:   2,
		Burst:            20,
		BurstPerUser:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Burst != 20 || explicit.BurstPerUser != 10 {
		t.Fatalf("explicit bursts = (%d, %d), want (20, 10)", explicit.Burst, explicit.BurstPerUser)
	}
}

func TestNormalizeDNSExchangeRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name string
		cfg  DNSExchange
		want string
	}{
		{
			name: "negative concurrency",
			cfg:  DNSExchange{MaxConcurrent: -1},
			want: "max_concurrent",
		},
		{
			name: "excessive global concurrency",
			cfg:  DNSExchange{MaxConcurrent: maxDNSConcurrent + 1},
			want: "max_concurrent",
		},
		{
			name: "excessive rate",
			cfg:  DNSExchange{QueriesPerSecond: maxDNSRate + 1},
			want: "queries_per_second",
		},
		{
			name: "per-user concurrency exceeds global",
			cfg: DNSExchange{
				MaxConcurrent:        1,
				MaxConcurrentPerUser: 2,
			},
			want: "must not exceed max_concurrent",
		},
		{
			name: "per-user rate exceeds global",
			cfg: DNSExchange{
				QueriesPerSecond: 1,
				QueriesPerUser:   2,
			},
			want: "must not exceed queries_per_second",
		},
		{
			name: "per-user burst exceeds global",
			cfg: DNSExchange{
				Burst:        1,
				BurstPerUser: 2,
			},
			want: "must not exceed burst",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizeDNSExchange(&tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NormalizeDNSExchange() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}
