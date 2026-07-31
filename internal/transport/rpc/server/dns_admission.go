package server

import (
	"sync/atomic"

	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
)

type dnsAdmission = admission
type dnsAdmissionRejection = admissionRejection

const (
	dnsAdmissionAllowed          = admissionAllowed
	dnsRejectedGlobalConcurrency = admissionRejectedGlobalConcurrency
	dnsRejectedUserConcurrency   = admissionRejectedUserConcurrency
	dnsRejectedGlobalRate        = admissionRejectedGlobalRate
	dnsRejectedUserRate          = admissionRejectedUserRate
)

func newDNSAdmission(cfg config.DNSExchange, users config.Users) *dnsAdmission {
	return newAdmission(admissionLimits{
		maxConcurrent:        cfg.MaxConcurrent,
		maxConcurrentPerUser: cfg.MaxConcurrentPerUser,
		rate:                 cfg.QueriesPerSecond,
		ratePerUser:          cfg.QueriesPerUser,
		burst:                cfg.Burst,
		burstPerUser:         cfg.BurstPerUser,
	}, users)
}

// DNSExchangeStats is a process-wide snapshot exposed by the loopback
// management endpoint. Counters are monotonic for the process lifetime.
type DNSExchangeStats struct {
	RequestsTotal                   uint64 `json:"requests_total"`
	InvalidRequestsTotal            uint64 `json:"invalid_requests_total"`
	LegacyRequestsTotal             uint64 `json:"legacy_requests_total"`
	LegacyItemsTotal                uint64 `json:"legacy_items_total"`
	ForwardedTotal                  uint64 `json:"forwarded_total"`
	BlockedIPv6Total                uint64 `json:"blocked_ipv6_total"`
	UpstreamFailuresTotal           uint64 `json:"upstream_failures_total"`
	RejectedGlobalConcurrencyTotal  uint64 `json:"rejected_global_concurrency_total"`
	RejectedPerUserConcurrencyTotal uint64 `json:"rejected_per_user_concurrency_total"`
	RejectedGlobalRateTotal         uint64 `json:"rejected_global_rate_total"`
	RejectedPerUserRateTotal        uint64 `json:"rejected_per_user_rate_total"`
}

type dnsExchangeCounters struct {
	requests                  atomic.Uint64
	invalidRequests           atomic.Uint64
	legacyRequests            atomic.Uint64
	legacyItems               atomic.Uint64
	forwarded                 atomic.Uint64
	blockedIPv6               atomic.Uint64
	upstreamFailures          atomic.Uint64
	rejectedGlobalConcurrency atomic.Uint64
	rejectedUserConcurrency   atomic.Uint64
	rejectedGlobalRate        atomic.Uint64
	rejectedUserRate          atomic.Uint64
}

var globalDNSExchangeCounters dnsExchangeCounters

// DNSExchangeStatistics returns a consistent-enough atomic snapshot of DNS RPC
// activity. Individual fields may advance while the snapshot is being read,
// which is appropriate for monotonic operational counters.
func DNSExchangeStatistics() DNSExchangeStats {
	return DNSExchangeStats{
		RequestsTotal:                   globalDNSExchangeCounters.requests.Load(),
		InvalidRequestsTotal:            globalDNSExchangeCounters.invalidRequests.Load(),
		LegacyRequestsTotal:             globalDNSExchangeCounters.legacyRequests.Load(),
		LegacyItemsTotal:                globalDNSExchangeCounters.legacyItems.Load(),
		ForwardedTotal:                  globalDNSExchangeCounters.forwarded.Load(),
		BlockedIPv6Total:                globalDNSExchangeCounters.blockedIPv6.Load(),
		UpstreamFailuresTotal:           globalDNSExchangeCounters.upstreamFailures.Load(),
		RejectedGlobalConcurrencyTotal:  globalDNSExchangeCounters.rejectedGlobalConcurrency.Load(),
		RejectedPerUserConcurrencyTotal: globalDNSExchangeCounters.rejectedUserConcurrency.Load(),
		RejectedGlobalRateTotal:         globalDNSExchangeCounters.rejectedGlobalRate.Load(),
		RejectedPerUserRateTotal:        globalDNSExchangeCounters.rejectedUserRate.Load(),
	}
}

func recordDNSAdmissionRejection(reason dnsAdmissionRejection) {
	switch reason {
	case dnsRejectedGlobalConcurrency:
		globalDNSExchangeCounters.rejectedGlobalConcurrency.Add(1)
	case dnsRejectedUserConcurrency:
		globalDNSExchangeCounters.rejectedUserConcurrency.Add(1)
	case dnsRejectedGlobalRate:
		globalDNSExchangeCounters.rejectedGlobalRate.Add(1)
	case dnsRejectedUserRate:
		globalDNSExchangeCounters.rejectedUserRate.Add(1)
	}
}
