package router

import (
	"errors"
	"sync"
	"testing"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

// Smoke: domain + exact routing, normalized cache keys, and concurrent lookups.
func TestSmoke_RoutingAndCacheKeys(t *testing.T) {
	if err := SetRoutes(Routes{
		{
			Sources:     []string{"Blocked.Example"},
			Destination: EgressBlock,
			MatchType:   TypeExact,
		},
		{
			Sources:     []string{"google.com"},
			Destination: EgressDirect,
			MatchType:   TypeDomain,
		},
		{
			Sources:     []string{"www.github.com"},
			Destination: EgressDirect,
			MatchType:   TypeDomain,
		},
		CloneRoute(RouteServerDefault),
	}); err != nil {
		t.Fatal(err)
	}

	// Case / trailing-dot variants of the same host must resolve identically
	// and share a single cache key after the first lookup.
	for _, host := range []string{"WWW.GOOGLE.COM", "www.google.com.", "www.google.com"} {
		tr, err := GetRoute(host)
		if err != nil {
			t.Fatalf("GetRoute(%q) error = %v", host, err)
		}
		if tr.String() != "direct" {
			t.Fatalf("GetRoute(%q) = %s, want direct", host, tr)
		}
		_ = tr.Close()
	}

	// Normalized key is stored once.
	if _, ok := table.Get("www.google.com"); !ok {
		t.Fatal("expected cache hit for normalized key www.google.com")
	}
	if _, ok := table.Get("WWW.GOOGLE.COM"); ok {
		t.Fatal("raw mixed-case key must not be stored separately")
	}

	// Exact match is case-insensitive; block egress surfaces ErrBlocked.
	_, err := GetRoute("blocked.example")
	if !errors.Is(err, transport.ErrBlocked) {
		t.Fatalf("GetRoute(blocked.example) = %v, want ErrBlocked", err)
	}

	// Subdomain of a more-specific domain rule.
	tr, err := GetRoute("cdn.www.github.com")
	if err != nil {
		t.Fatalf("GetRoute(cdn.www.github.com) = %v", err)
	}
	if tr.String() != "direct" {
		t.Fatalf("cdn.www.github.com = %s, want direct", tr)
	}
	_ = tr.Close()

	// Sibling of www.github.com falls through to default (direct), not the domain rule.
	r := &Route{Sources: []string{"www.github.com"}, MatchType: TypeDomain}
	_ = r.GenerateCache()
	if r.Match("api.github.com") {
		t.Fatal("api.github.com must not match domain rule www.github.com")
	}
	tr, err = GetRoute("api.github.com")
	if err != nil {
		t.Fatalf("api.github.com should hit default: %v", err)
	}
	if tr.String() != "direct" {
		t.Fatalf("api.github.com = %s, want direct (default)", tr)
	}
	_ = tr.Close()
}

func TestSmoke_ConcurrentGetRoute(t *testing.T) {
	if err := SetRoutes(Routes{
		{Sources: []string{"example.com"}, Destination: EgressDirect, MatchType: TypeDomain},
		CloneRoute(RouteServerDefault),
	}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	hosts := []string{
		"Example.com", "example.com.", "a.example.com", "B.Example.COM",
		"other.test", "OTHER.test.",
	}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			host := hosts[i%len(hosts)]
			tr, err := GetRoute(host)
			if err != nil {
				t.Errorf("GetRoute(%q) = %v", host, err)
				return
			}
			_ = tr.Close()
		}(i)
	}
	wg.Wait()
}

func TestCloneRoute_IndependentCache(t *testing.T) {
	src := &Route{
		Sources:     []string{"a.com"},
		Destination: EgressDirect,
		MatchType:   TypeExact,
	}
	if err := src.GenerateCache(); err != nil {
		t.Fatal(err)
	}
	cloned := CloneRoute(src)
	if len(cloned.cache.ExactMap) != 0 {
		t.Fatal("clone must not share populated match cache")
	}
	cloned.Sources[0] = "b.com"
	if src.Sources[0] != "a.com" {
		t.Fatal("clone must deep-copy Sources")
	}
}

func TestSetRoutesRejectsInvalidGeneration(t *testing.T) {
	if err := SetRoutes(Routes{
		{Destination: EgressDirect, MatchType: TypeDefault},
	}); err != nil {
		t.Fatal(err)
	}

	// Populate the host cache before attempting a failed reload.
	tr, err := GetRoute("cached.example")
	if err != nil {
		t.Fatal(err)
	}
	_ = tr.Close()

	if err := SetRoutes(Routes{
		{Sources: []string{"["}, Destination: EgressBlackHole, MatchType: TypeRegex},
	}); err == nil {
		t.Fatal("SetRoutes() accepted an invalid regular expression")
	}

	tr, err = GetRoute("cached.example")
	if err != nil {
		t.Fatalf("failed reload replaced the previous route set: %v", err)
	}
	defer tr.Close()
	if tr.String() != "direct" {
		t.Fatalf("route after failed reload = %s, want direct", tr)
	}
}

func TestSetRoutesClonesCallerState(t *testing.T) {
	route := &Route{
		Sources:     []string{"stable.example"},
		Destination: EgressDirect,
		MatchType:   TypeExact,
	}
	if err := SetRoutes(Routes{route}); err != nil {
		t.Fatal(err)
	}

	route.Sources[0] = "mutated.example"
	route.Destination = EgressBlackHole

	tr, err := GetRoute("stable.example")
	if err != nil {
		t.Fatalf("caller mutation changed installed route: %v", err)
	}
	defer tr.Close()
	if tr.String() != "direct" {
		t.Fatalf("installed route = %s, want direct", tr)
	}
}

func TestConcurrentRouteReloadAndLookup(t *testing.T) {
	direct := Routes{{Destination: EgressDirect, MatchType: TypeDefault}}
	blackhole := Routes{{Destination: EgressBlackHole, MatchType: TypeDefault}}
	if err := SetRoutes(direct); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				tr, err := GetRoute("reload.example")
				if err != nil {
					t.Errorf("GetRoute() during reload: %v", err)
					return
				}
				if got := tr.String(); got != "direct" && got != "blackHole" {
					t.Errorf("GetRoute() during reload = %s", got)
					_ = tr.Close()
					return
				}
				_ = tr.Close()
			}
		}()
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			routes := direct
			if i%2 == 1 {
				routes = blackhole
			}
			if err := SetRoutes(routes); err != nil {
				t.Errorf("SetRoutes() during reload: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if err := GenerateCache(); err != nil {
				t.Errorf("GenerateCache() during reload: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

func TestAddRoutesBuildsCacheAndInvalidatesHostCache(t *testing.T) {
	if err := SetRoutes(Routes{{Destination: EgressDirect, MatchType: TypeDefault}}); err != nil {
		t.Fatal(err)
	}
	tr, err := GetRoute("priority.example")
	if err != nil {
		t.Fatal(err)
	}
	_ = tr.Close()

	if err := AddToFirstRoute(&Route{
		Sources:     []string{"priority.example"},
		Destination: EgressBlackHole,
		MatchType:   TypeExact,
	}); err != nil {
		t.Fatal(err)
	}
	tr, err = GetRoute("priority.example")
	if err != nil {
		t.Fatal(err)
	}
	if got := tr.String(); got != "blackHole" {
		_ = tr.Close()
		t.Fatalf("prepended route = %s, want blackHole", got)
	}
	_ = tr.Close()

	if err := SetRoutes(nil); err != nil {
		t.Fatal(err)
	}
	if err := AddToLastRoute(&Route{
		Sources:     []string{"last.example"},
		Destination: EgressDirect,
		MatchType:   TypeExact,
	}); err != nil {
		t.Fatal(err)
	}
	tr, err = GetRoute("last.example")
	if err != nil {
		t.Fatal(err)
	}
	_ = tr.Close()

	if err := AddToLastRoute(nil); err == nil {
		t.Fatal("AddToLastRoute(nil) accepted a nil route")
	}
}
