package server

import (
	"fmt"
	"time"
)

const (
	DefaultProxyMaxConcurrent        = 8192
	DefaultProxyMaxConcurrentPerUser = 4096
	DefaultProxySessionsPerSecond    = 8192
	DefaultProxySessionsPerUser      = 4096
	DefaultProxyBurst                = 8192
	DefaultProxyBurstPerUser         = 4096
	DefaultProxyHandshakeTimeout     = 10 * time.Second

	maxProxyConcurrent       = 1 << 16
	maxProxyRate             = 1_000_000
	maxProxyHandshakeSeconds = 5 * 60
)

// ProxySessions bounds authenticated streaming Proxy RPCs. Limits are applied
// across HTTP/2 connections, so opening additional transports cannot evade a
// user's quota. Zero-valued fields select safe defaults and cannot disable the
// protection.
type ProxySessions struct {
	MaxConcurrent           int `json:"max_concurrent,omitempty"`
	MaxConcurrentPerUser    int `json:"max_concurrent_per_user,omitempty"`
	SessionsPerSecond       int `json:"new_sessions_per_second,omitempty"`
	SessionsPerUser         int `json:"new_sessions_per_second_per_user,omitempty"`
	Burst                   int `json:"burst,omitempty"`
	BurstPerUser            int `json:"burst_per_user,omitempty"`
	HandshakeTimeoutSeconds int `json:"handshake_timeout_seconds,omitempty"`
}

// NormalizeProxySessions validates raw and fills zero-valued defaults.
func NormalizeProxySessions(raw *ProxySessions) (ProxySessions, error) {
	var cfg ProxySessions
	if raw != nil {
		cfg = *raw
	}

	var err error
	if cfg.MaxConcurrent, err = normalizeProxyLimit(
		"proxy_sessions.max_concurrent",
		cfg.MaxConcurrent,
		DefaultProxyMaxConcurrent,
		maxProxyConcurrent,
	); err != nil {
		return ProxySessions{}, err
	}
	if cfg.MaxConcurrentPerUser, err = normalizeProxyLimit(
		"proxy_sessions.max_concurrent_per_user",
		cfg.MaxConcurrentPerUser,
		min(DefaultProxyMaxConcurrentPerUser, cfg.MaxConcurrent),
		maxProxyConcurrent,
	); err != nil {
		return ProxySessions{}, err
	}
	if cfg.SessionsPerSecond, err = normalizeProxyLimit(
		"proxy_sessions.new_sessions_per_second",
		cfg.SessionsPerSecond,
		DefaultProxySessionsPerSecond,
		maxProxyRate,
	); err != nil {
		return ProxySessions{}, err
	}
	if cfg.SessionsPerUser, err = normalizeProxyLimit(
		"proxy_sessions.new_sessions_per_second_per_user",
		cfg.SessionsPerUser,
		min(DefaultProxySessionsPerUser, cfg.SessionsPerSecond),
		maxProxyRate,
	); err != nil {
		return ProxySessions{}, err
	}
	if cfg.Burst, err = normalizeProxyLimit(
		"proxy_sessions.burst",
		cfg.Burst,
		min(DefaultProxyBurst, cfg.SessionsPerSecond),
		maxProxyConcurrent,
	); err != nil {
		return ProxySessions{}, err
	}
	if cfg.BurstPerUser, err = normalizeProxyLimit(
		"proxy_sessions.burst_per_user",
		cfg.BurstPerUser,
		min(DefaultProxyBurstPerUser, cfg.Burst, cfg.SessionsPerUser),
		maxProxyConcurrent,
	); err != nil {
		return ProxySessions{}, err
	}
	if cfg.HandshakeTimeoutSeconds, err = normalizeProxyLimit(
		"proxy_sessions.handshake_timeout_seconds",
		cfg.HandshakeTimeoutSeconds,
		int(DefaultProxyHandshakeTimeout/time.Second),
		maxProxyHandshakeSeconds,
	); err != nil {
		return ProxySessions{}, err
	}

	if cfg.MaxConcurrentPerUser > cfg.MaxConcurrent {
		return ProxySessions{}, fmt.Errorf(
			"proxy_sessions.max_concurrent_per_user must not exceed max_concurrent: %d > %d",
			cfg.MaxConcurrentPerUser,
			cfg.MaxConcurrent,
		)
	}
	if cfg.SessionsPerUser > cfg.SessionsPerSecond {
		return ProxySessions{}, fmt.Errorf(
			"proxy_sessions.new_sessions_per_second_per_user must not exceed new_sessions_per_second: %d > %d",
			cfg.SessionsPerUser,
			cfg.SessionsPerSecond,
		)
	}
	if cfg.BurstPerUser > cfg.Burst {
		return ProxySessions{}, fmt.Errorf(
			"proxy_sessions.burst_per_user must not exceed burst: %d > %d",
			cfg.BurstPerUser,
			cfg.Burst,
		)
	}
	return cfg, nil
}

func normalizeProxyLimit(name string, value, fallback, maximum int) (int, error) {
	if value < 0 || value > maximum {
		return 0, fmt.Errorf("%s must be between 0 and %d: %d", name, maximum, value)
	}
	if value == 0 {
		return fallback, nil
	}
	return value, nil
}
