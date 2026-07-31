package server

import "fmt"

const (
	DefaultDNSMaxConcurrent        = 1024
	DefaultDNSMaxConcurrentPerUser = 256
	DefaultDNSQueriesPerSecond     = 4096
	DefaultDNSQueriesPerUser       = 1024
	DefaultDNSBurst                = 1024
	DefaultDNSBurstPerUser         = 256

	maxDNSConcurrent = 1 << 16
	maxDNSRate       = 1_000_000
)

// DNSExchange bounds the authenticated raw-wire DNS RPC used by TUN DNS
// hijacking. Zero-valued fields select safe defaults; limits cannot be
// disabled because each admitted request owns an upstream resolver exchange.
type DNSExchange struct {
	MaxConcurrent        int `json:"max_concurrent,omitempty"`
	MaxConcurrentPerUser int `json:"max_concurrent_per_user,omitempty"`
	QueriesPerSecond     int `json:"queries_per_second,omitempty"`
	QueriesPerUser       int `json:"queries_per_second_per_user,omitempty"`
	Burst                int `json:"burst,omitempty"`
	BurstPerUser         int `json:"burst_per_user,omitempty"`
}

// NormalizeDNSExchange validates raw and fills zero-valued defaults.
func NormalizeDNSExchange(raw *DNSExchange) (DNSExchange, error) {
	var cfg DNSExchange
	if raw != nil {
		cfg = *raw
	}

	var err error
	if cfg.MaxConcurrent, err = normalizeDNSLimit(
		"dns_exchange.max_concurrent",
		cfg.MaxConcurrent,
		DefaultDNSMaxConcurrent,
		maxDNSConcurrent,
	); err != nil {
		return DNSExchange{}, err
	}
	if cfg.MaxConcurrentPerUser, err = normalizeDNSLimit(
		"dns_exchange.max_concurrent_per_user",
		cfg.MaxConcurrentPerUser,
		min(DefaultDNSMaxConcurrentPerUser, cfg.MaxConcurrent),
		maxDNSConcurrent,
	); err != nil {
		return DNSExchange{}, err
	}
	if cfg.QueriesPerSecond, err = normalizeDNSLimit(
		"dns_exchange.queries_per_second",
		cfg.QueriesPerSecond,
		DefaultDNSQueriesPerSecond,
		maxDNSRate,
	); err != nil {
		return DNSExchange{}, err
	}
	if cfg.QueriesPerUser, err = normalizeDNSLimit(
		"dns_exchange.queries_per_second_per_user",
		cfg.QueriesPerUser,
		min(DefaultDNSQueriesPerUser, cfg.QueriesPerSecond),
		maxDNSRate,
	); err != nil {
		return DNSExchange{}, err
	}
	if cfg.Burst, err = normalizeDNSLimit(
		"dns_exchange.burst",
		cfg.Burst,
		min(DefaultDNSBurst, cfg.QueriesPerSecond),
		maxDNSConcurrent,
	); err != nil {
		return DNSExchange{}, err
	}
	if cfg.BurstPerUser, err = normalizeDNSLimit(
		"dns_exchange.burst_per_user",
		cfg.BurstPerUser,
		min(DefaultDNSBurstPerUser, cfg.Burst, cfg.QueriesPerUser),
		maxDNSConcurrent,
	); err != nil {
		return DNSExchange{}, err
	}

	if cfg.MaxConcurrentPerUser > cfg.MaxConcurrent {
		return DNSExchange{}, fmt.Errorf(
			"dns_exchange.max_concurrent_per_user must not exceed max_concurrent: %d > %d",
			cfg.MaxConcurrentPerUser,
			cfg.MaxConcurrent,
		)
	}
	if cfg.QueriesPerUser > cfg.QueriesPerSecond {
		return DNSExchange{}, fmt.Errorf(
			"dns_exchange.queries_per_second_per_user must not exceed queries_per_second: %d > %d",
			cfg.QueriesPerUser,
			cfg.QueriesPerSecond,
		)
	}
	if cfg.BurstPerUser > cfg.Burst {
		return DNSExchange{}, fmt.Errorf(
			"dns_exchange.burst_per_user must not exceed burst: %d > %d",
			cfg.BurstPerUser,
			cfg.Burst,
		)
	}
	return cfg, nil
}

func normalizeDNSLimit(name string, value, fallback, maximum int) (int, error) {
	if value < 0 || value > maximum {
		return 0, fmt.Errorf("%s must be between 0 and %d: %d", name, maximum, value)
	}
	if value == 0 {
		return fallback, nil
	}
	return value, nil
}
