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
