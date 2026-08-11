//go:build linux

package transport

import (
	"testing"

	"golang.org/x/sys/unix"
)

// verifyBypassMark falls back to IPv6 when a host has no IPv4, so the fallback
// must reach the same verdict as the primary family rather than silently
// reporting success. Both are probed directly because the fallback is otherwise
// unreachable on a dual-stack host.
func TestTrySetBypassMarkAgreesAcrossAddressFamilies(t *testing.T) {
	inetAttempted, inetErr := trySetBypassMark(unix.AF_INET, DefaultBypassMark)
	inet6Attempted, inet6Err := trySetBypassMark(unix.AF_INET6, DefaultBypassMark)

	if !inetAttempted && !inet6Attempted {
		t.Skip("no datagram socket family available to probe")
	}
	if !inetAttempted || !inet6Attempted {
		t.Skipf(
			"single-stack host (AF_INET probed=%t, AF_INET6 probed=%t); "+
				"cross-family agreement is not observable here",
			inetAttempted, inet6Attempted,
		)
	}
	if (inetErr == nil) != (inet6Err == nil) {
		t.Fatalf("family probes disagree: AF_INET = %v, AF_INET6 = %v", inetErr, inet6Err)
	}
	if err := verifyBypassMark(DefaultBypassMark); (err == nil) != (inetErr == nil) {
		t.Fatalf("verifyBypassMark() = %v, want agreement with per-family probe %v", err, inetErr)
	}
}

// unsupportedAddressFamily is rejected by socket(2) on every supported kernel.
// AF_UNIX is deliberately not used: its datagram sockets open successfully, so
// it would exercise the opposite branch.
const unsupportedAddressFamily = 0x7ffe

// A family that cannot be opened must report "not attempted" with a nil error,
// so verifyBypassMark keeps trying instead of mistaking it for granted
// capability — and never reports the socket error as a mark rejection.
func TestTrySetBypassMarkReportsUnopenableFamily(t *testing.T) {
	attempted, err := trySetBypassMark(unsupportedAddressFamily, DefaultBypassMark)
	if attempted {
		t.Fatalf("family %#x unexpectedly opened; the test no longer covers its branch", unsupportedAddressFamily)
	}
	if err != nil {
		t.Fatalf("unattempted probe returned error %v, want nil", err)
	}
}
