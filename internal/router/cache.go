package router

import (
	"sync"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
)

var (
	routesMu      sync.RWMutex
	routesCache   Routes
	routesVersion uint64
	table         = newSyncedRoutesTable(maxCacheSize)
)

func AddToFirstRoute(r *Route) error {
	prepared, err := prepareRoutes(Routes{r})
	if err != nil {
		return err
	}
	routesMu.Lock()
	defer routesMu.Unlock()
	routesCache = append(prepared, routesCache...)
	routesVersion++
	table.Reset()
	return nil
}

func AddToLastRoute(r *Route) error {
	prepared, err := prepareRoutes(Routes{r})
	if err != nil {
		return err
	}
	routesMu.Lock()
	defer routesMu.Unlock()
	routesCache = append(routesCache, prepared...)
	routesVersion++
	table.Reset()
	return nil
}

// normalizeRouteKey returns the canonical cache/match key for a destination.
// Hostnames are lowercased and trailing FQDN dots stripped; empty input is kept
// as-is so callers still get a useful error from GetRoute.
func normalizeRouteKey(dst string) string {
	if key := utils.NormalizeHost(dst); key != "" {
		return key
	}
	return dst
}

func GetRoute(dst string) (transport.Transport, error) {
	key := normalizeRouteKey(dst)
	routesMu.RLock()
	defer routesMu.RUnlock()
	if route, ok := table.Get(key); ok {
		return route.GetTransport()
	}
	return routesCache.GetRoute(key)
}

// AnyRouteSupportsUDP reports whether any installed route has an egress capable
// of carrying UDP. When none can, SOCKS5 UDP ASSOCIATE is refused up front so
// clients fall back to TCP rather than holding an association whose every
// datagram would be dropped at dial time.
func AnyRouteSupportsUDP() bool {
	routesMu.RLock()
	defer routesMu.RUnlock()
	for _, route := range routesCache {
		if route != nil && route.Destination.SupportsUDP() {
			return true
		}
	}
	return false
}

func GenerateCache() error {
	for {
		routesMu.RLock()
		snapshot := cloneRoutes(routesCache)
		version := routesVersion
		routesMu.RUnlock()

		if err := snapshot.GenerateCache(); err != nil {
			return err
		}

		routesMu.Lock()
		if version != routesVersion {
			routesMu.Unlock()
			continue
		}
		routesCache = snapshot
		routesVersion++
		table.Reset()
		routesMu.Unlock()
		return nil
	}
}

func SetRoutes(r Routes) error {
	prepared, err := prepareRoutes(r)
	if err != nil {
		return err
	}

	routesMu.Lock()
	defer routesMu.Unlock()
	routesCache = prepared
	routesVersion++
	table.Reset()
	return nil
}

func prepareRoutes(routes Routes) (Routes, error) {
	prepared := cloneRoutes(routes)
	if err := prepared.GenerateCache(); err != nil {
		return nil, err
	}
	return prepared, nil
}

func cloneRoutes(routes Routes) Routes {
	cloned := make(Routes, len(routes))
	for i, route := range routes {
		cloned[i] = CloneRoute(route)
	}
	return cloned
}
