package router

import (
	"testing"
)

func TestAddToFirstAndLastRoute(t *testing.T) {
	// Start without a default so later AddToLast can append a catch-all.
	if err := SetRoutes(Routes{
		{
			MatchType:   TypeExact,
			Sources:     []string{"seed.example"},
			Destination: EgressDirect,
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Prepend a more specific blackhole rule.
	blockExact := &Route{
		MatchType:   TypeExact,
		Sources:     []string{"blocked.example"},
		Destination: EgressBlackHole,
	}
	if err := AddToFirstRoute(blockExact); err != nil {
		t.Fatalf("AddToFirstRoute: %v", err)
	}

	tr, err := GetRoute("blocked.example")
	if err != nil {
		t.Fatalf("GetRoute blocked: %v", err)
	}
	if tr.String() != "blackHole" {
		t.Fatalf("route = %s, want blackHole", tr)
	}
	_ = tr.Close()

	// Append a catch-all default after the exact rules.
	if err := AddToLastRoute(&Route{
		MatchType:   TypeDefault,
		Destination: EgressDirect,
	}); err != nil {
		t.Fatalf("AddToLastRoute: %v", err)
	}

	tr, err = GetRoute("other.example")
	if err != nil {
		t.Fatalf("GetRoute other: %v", err)
	}
	if tr.String() != "direct" {
		t.Fatalf("route = %s, want direct", tr)
	}
	_ = tr.Close()

	// Prepended rule still wins over default.
	tr, err = GetRoute("blocked.example")
	if err != nil {
		t.Fatalf("GetRoute blocked after default: %v", err)
	}
	if tr.String() != "blackHole" {
		t.Fatalf("route = %s, want blackHole", tr)
	}
	_ = tr.Close()
}

func TestGetRouteObservedPrefersEarlierRule(t *testing.T) {
	if err := SetRoutes(Routes{
		{MatchType: TypeCIDR, Sources: []string{"203.0.113.0/24"}, Destination: EgressBlackHole},
		{MatchType: TypeExact, Sources: []string{"sniff.example"}, Destination: EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = SetRoutes(Routes{{MatchType: TypeDefault, Destination: EgressDirect}})
	})

	tr, err := GetRouteObserved("203.0.113.10", "sniff.example")
	if err != nil {
		t.Fatal(err)
	}
	if tr.String() != "blackHole" {
		t.Fatalf("cidr-first route = %s, want blackHole", tr)
	}
	_ = tr.Close()

	tr, err = GetRouteObserved("198.51.100.10", "sniff.example")
	if err != nil {
		t.Fatal(err)
	}
	if tr.String() != "direct" {
		t.Fatalf("name route = %s, want direct", tr)
	}
	_ = tr.Close()

	// A second lookup hits the observed cache and still returns direct.
	tr, err = GetRouteObserved("198.51.100.10", "sniff.example")
	if err != nil {
		t.Fatal(err)
	}
	if tr.String() != "direct" {
		t.Fatalf("cached name route = %s, want direct", tr)
	}
	_ = tr.Close()

	if _, err = GetRouteObserved("198.51.100.10", ""); err == nil {
		t.Fatal("ip with no recovered name matched a domain rule")
	}
}

func TestGenerateCacheRebuilds(t *testing.T) {
	if err := SetRoutes(Routes{
		{MatchType: TypeExact, Sources: []string{"A.Example.COM"}, Destination: EgressDirect},
		{MatchType: TypeDefault, Destination: EgressBlackHole},
	}); err != nil {
		t.Fatal(err)
	}
	if err := GenerateCache(); err != nil {
		t.Fatalf("GenerateCache: %v", err)
	}
	tr, err := GetRoute("a.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if tr.String() != "direct" {
		t.Fatalf("route = %s, want direct", tr)
	}
	_ = tr.Close()
}

func TestAddToFirstRouteInvalid(t *testing.T) {
	err := AddToFirstRoute(&Route{
		MatchType: TypeRegex,
		Sources:   []string{"["}, // invalid regex
	})
	if err == nil {
		t.Fatal("AddToFirstRoute accepted invalid regex")
	}
}
