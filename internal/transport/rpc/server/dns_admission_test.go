package server

import (
	"testing"

	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
)

func normalizedDNSLimits(t *testing.T, raw config.DNSExchange) config.DNSExchange {
	t.Helper()
	limits, err := config.NormalizeDNSExchange(&raw)
	if err != nil {
		t.Fatal(err)
	}
	return limits
}

func TestDNSAdmissionEnforcesAndReleasesConcurrencyLimits(t *testing.T) {
	users := config.Users{{UUID: "alice"}, {UUID: "bob"}}

	t.Run("global", func(t *testing.T) {
		admission := newDNSAdmission(normalizedDNSLimits(t, config.DNSExchange{
			MaxConcurrent:        1,
			MaxConcurrentPerUser: 1,
			QueriesPerSecond:     100,
			QueriesPerUser:       100,
			Burst:                10,
			BurstPerUser:         10,
		}), users)

		release, rejection := admission.acquire("alice")
		if rejection != dnsAdmissionAllowed {
			t.Fatalf("first acquire rejection = %v", rejection)
		}
		if _, rejection := admission.acquire("bob"); rejection != dnsRejectedGlobalConcurrency {
			t.Fatalf("second acquire rejection = %v, want global concurrency", rejection)
		}
		release()
		release() // A lease is safe to release defensively more than once.
		if release, rejection = admission.acquire("bob"); rejection != dnsAdmissionAllowed {
			t.Fatalf("acquire after release rejection = %v", rejection)
		}
		release()
	})

	t.Run("per user", func(t *testing.T) {
		admission := newDNSAdmission(normalizedDNSLimits(t, config.DNSExchange{
			MaxConcurrent:        2,
			MaxConcurrentPerUser: 1,
			QueriesPerSecond:     100,
			QueriesPerUser:       100,
			Burst:                10,
			BurstPerUser:         10,
		}), users)

		releaseAlice, rejection := admission.acquire("alice")
		if rejection != dnsAdmissionAllowed {
			t.Fatalf("alice acquire rejection = %v", rejection)
		}
		if _, rejection := admission.acquire("alice"); rejection != dnsRejectedUserConcurrency {
			t.Fatalf("second alice acquire rejection = %v, want per-user concurrency", rejection)
		}
		releaseBob, rejection := admission.acquire("bob")
		if rejection != dnsAdmissionAllowed {
			t.Fatalf("bob acquire rejection = %v", rejection)
		}
		releaseAlice()
		releaseBob()
	})
}

func TestDNSAdmissionEnforcesRateLimitsWithoutCrossUserTokenLoss(t *testing.T) {
	users := config.Users{{UUID: "alice"}, {UUID: "bob"}}

	t.Run("per user", func(t *testing.T) {
		admission := newDNSAdmission(normalizedDNSLimits(t, config.DNSExchange{
			MaxConcurrent:        4,
			MaxConcurrentPerUser: 2,
			QueriesPerSecond:     100,
			QueriesPerUser:       1,
			Burst:                4,
			BurstPerUser:         1,
		}), users)

		release, rejection := admission.acquire("alice")
		if rejection != dnsAdmissionAllowed {
			t.Fatalf("first alice acquire rejection = %v", rejection)
		}
		release()
		if _, rejection := admission.acquire("alice"); rejection != dnsRejectedUserRate {
			t.Fatalf("second alice acquire rejection = %v, want per-user rate", rejection)
		}

		// Alice's rejected request must not consume a global token needed by Bob.
		release, rejection = admission.acquire("bob")
		if rejection != dnsAdmissionAllowed {
			t.Fatalf("bob acquire after alice rejection = %v", rejection)
		}
		release()
	})

	t.Run("global", func(t *testing.T) {
		admission := newDNSAdmission(normalizedDNSLimits(t, config.DNSExchange{
			MaxConcurrent:        4,
			MaxConcurrentPerUser: 2,
			QueriesPerSecond:     1,
			QueriesPerUser:       1,
			Burst:                1,
			BurstPerUser:         1,
		}), users)

		release, rejection := admission.acquire("alice")
		if rejection != dnsAdmissionAllowed {
			t.Fatalf("alice acquire rejection = %v", rejection)
		}
		release()
		if _, rejection := admission.acquire("bob"); rejection != dnsRejectedGlobalRate {
			t.Fatalf("bob acquire rejection = %v, want global rate", rejection)
		}
		if tokens := admission.users["bob"].rate.Tokens(); tokens < 0.99 {
			t.Fatalf("global rejection consumed Bob's per-user token: %f remain", tokens)
		}
	})
}

func TestDNSAdmissionBoundsUnknownDirectHandlerIdentity(t *testing.T) {
	admission := newDNSAdmission(normalizedDNSLimits(t, config.DNSExchange{
		MaxConcurrent:        2,
		MaxConcurrentPerUser: 1,
		QueriesPerSecond:     10,
		QueriesPerUser:       10,
		Burst:                2,
		BurstPerUser:         1,
	}), config.Users{{UUID: "known"}})

	release, rejection := admission.acquire("")
	if rejection != dnsAdmissionAllowed {
		t.Fatalf("unknown identity acquire rejection = %v", rejection)
	}
	if _, rejection := admission.acquire("not-in-auth-map"); rejection != dnsRejectedUserConcurrency {
		t.Fatalf("shared unknown identity rejection = %v, want per-user concurrency", rejection)
	}
	release()
}

func TestRecordDNSAdmissionRejectionCounters(t *testing.T) {
	before := DNSExchangeStatistics()
	for _, reason := range []dnsAdmissionRejection{
		dnsRejectedGlobalConcurrency,
		dnsRejectedUserConcurrency,
		dnsRejectedGlobalRate,
		dnsRejectedUserRate,
	} {
		recordDNSAdmissionRejection(reason)
	}
	after := DNSExchangeStatistics()

	if after.RejectedGlobalConcurrencyTotal-before.RejectedGlobalConcurrencyTotal != 1 ||
		after.RejectedPerUserConcurrencyTotal-before.RejectedPerUserConcurrencyTotal != 1 ||
		after.RejectedGlobalRateTotal-before.RejectedGlobalRateTotal != 1 ||
		after.RejectedPerUserRateTotal-before.RejectedPerUserRateTotal != 1 {
		t.Fatalf("rejection counter deltas: before=%+v after=%+v", before, after)
	}
}
