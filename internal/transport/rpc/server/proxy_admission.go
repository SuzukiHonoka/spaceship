package server

import (
	"sync/atomic"

	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
)

func newProxyAdmission(cfg config.ProxySessions, users config.Users) *admission {
	return newAdmission(admissionLimits{
		maxConcurrent:        cfg.MaxConcurrent,
		maxConcurrentPerUser: cfg.MaxConcurrentPerUser,
		rate:                 cfg.SessionsPerSecond,
		ratePerUser:          cfg.SessionsPerUser,
		burst:                cfg.Burst,
		burstPerUser:         cfg.BurstPerUser,
	}, users)
}

// ProxySessionStats is a process-wide snapshot exposed by the loopback
// management endpoint. Active is a gauge; all other fields are monotonic.
type ProxySessionStats struct {
	RequestsTotal                   uint64 `json:"requests_total"`
	AdmittedTotal                   uint64 `json:"admitted_total"`
	Active                          int64  `json:"active"`
	HandshakeTimeoutsTotal          uint64 `json:"handshake_timeouts_total"`
	RejectedGlobalConcurrencyTotal  uint64 `json:"rejected_global_concurrency_total"`
	RejectedPerUserConcurrencyTotal uint64 `json:"rejected_per_user_concurrency_total"`
	RejectedGlobalRateTotal         uint64 `json:"rejected_global_rate_total"`
	RejectedPerUserRateTotal        uint64 `json:"rejected_per_user_rate_total"`
}

type proxySessionCounters struct {
	requests                  atomic.Uint64
	admitted                  atomic.Uint64
	active                    atomic.Int64
	handshakeTimeouts         atomic.Uint64
	rejectedGlobalConcurrency atomic.Uint64
	rejectedUserConcurrency   atomic.Uint64
	rejectedGlobalRate        atomic.Uint64
	rejectedUserRate          atomic.Uint64
}

var globalProxySessionCounters proxySessionCounters

// ProxySessionStatistics returns a lock-free operational snapshot.
func ProxySessionStatistics() ProxySessionStats {
	return ProxySessionStats{
		RequestsTotal:                   globalProxySessionCounters.requests.Load(),
		AdmittedTotal:                   globalProxySessionCounters.admitted.Load(),
		Active:                          globalProxySessionCounters.active.Load(),
		HandshakeTimeoutsTotal:          globalProxySessionCounters.handshakeTimeouts.Load(),
		RejectedGlobalConcurrencyTotal:  globalProxySessionCounters.rejectedGlobalConcurrency.Load(),
		RejectedPerUserConcurrencyTotal: globalProxySessionCounters.rejectedUserConcurrency.Load(),
		RejectedGlobalRateTotal:         globalProxySessionCounters.rejectedGlobalRate.Load(),
		RejectedPerUserRateTotal:        globalProxySessionCounters.rejectedUserRate.Load(),
	}
}

func recordProxyAdmissionRejection(reason admissionRejection) {
	switch reason {
	case admissionRejectedGlobalConcurrency:
		globalProxySessionCounters.rejectedGlobalConcurrency.Add(1)
	case admissionRejectedUserConcurrency:
		globalProxySessionCounters.rejectedUserConcurrency.Add(1)
	case admissionRejectedGlobalRate:
		globalProxySessionCounters.rejectedGlobalRate.Add(1)
	case admissionRejectedUserRate:
		globalProxySessionCounters.rejectedUserRate.Add(1)
	}
}
