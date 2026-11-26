//go:build linux && !android

package tun

import (
	"net/netip"
	"testing"
)

func TestAutoRedirectSpec_MergesExcludeIntervals(t *testing.T) {
	spec := buildAutoRedirectSpec(Config{
		BypassMark: 0x5350,
		RouteExcludeAddress: []netip.Prefix{
			netip.MustParsePrefix("10.0.0.0/24"),
			netip.MustParsePrefix("10.0.1.0/24"), // adjacent to 10.0.0.0/24 -> should merge
			netip.MustParsePrefix("192.168.0.0/16"),
			netip.MustParsePrefix("fd00::/16"),
		},
	}, "spaceship0", 12345, 0x5351)

	elems4 := prefixesToNFTIntervalElements4(spec.excludeV4)
	// 3 disjoint intervals: 10.0.0.0/23, 127.0.0.0/8, 192.168.0.0/16 -> 6 boundary elements
	if len(elems4) != 6 {
		t.Fatalf("v4 interval elements = %d, want 6 (merged 10.0.0.0/23 + 127.0.0.0/8 + 192.168.0.0/16)", len(elems4))
	}
	elems6 := prefixesToNFTIntervalElements6(spec.excludeV6)
	// 2 disjoint intervals: ::1/128, fd00::/16 -> 4 boundary elements
	if len(elems6) != 4 {
		t.Fatalf("v6 interval elements = %d, want 4", len(elems6))
	}
	if pickAutoRedirectTUNMark(0x5351) == 0x5351 {
		t.Fatal("pickAutoRedirectTUNMark must avoid colliding with BypassMark")
	}
}
