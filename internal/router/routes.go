package router

import (
	"fmt"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

type Routes []*Route

func (r Routes) GenerateCache() error {
	for i, route := range r {
		if route == nil {
			return fmt.Errorf("route %d is nil", i)
		}
		if err := route.GenerateCache(); err != nil {
			return fmt.Errorf("route %d: %w", i, err)
		}
	}
	return nil
}

func (r Routes) GetRoute(dst string) (transport.Transport, error) {
	// dst is expected to already be a normalizeRouteKey result when called from
	// the package GetRoute entrypoint; normalize again so direct callers are safe.
	key := normalizeRouteKey(dst)
	for i, route := range r {
		if route == nil {
			return nil, fmt.Errorf("route %d is nil", i)
		}
		if route.Match(key) {
			table.Set(key, route.Destination)
			//log.Printf("route cached: %s -> %s", key, route.Destination)
			return route.Destination.GetTransport()
		}
	}
	return nil, fmt.Errorf("route not found: %s -> nil", key)
}

// getObserved matches name rules against nameKey and CIDR rules against ipKey.
// The caller holds routesMu for reading.
func (r Routes) getObserved(cacheKey, ipKey, nameKey string) (transport.Transport, error) {
	for i, route := range r {
		if route == nil {
			return nil, fmt.Errorf("route %d is nil", i)
		}
		if matchObserved(route, ipKey, nameKey) {
			table.Set(cacheKey, route.Destination)
			return route.Destination.GetTransport()
		}
	}
	return nil, fmt.Errorf("route not found: %s -> nil", nameKey)
}

func matchObserved(route *Route, ipKey, nameKey string) bool {
	switch route.MatchType {
	case TypeCIDR:
		return route.Match(ipKey)
	case TypeDefault:
		return true
	default:
		return route.Match(nameKey) || route.Match(ipKey)
	}
}
