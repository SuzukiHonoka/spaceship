package router

import (
	"sync"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

var (
	routesMu    sync.RWMutex
	routesCache Routes
	table       = newSyncedRoutesTable(maxCacheSize)
)

func AddToFirstRoute(r *Route) {
	routesMu.Lock()
	defer routesMu.Unlock()
	rs := Routes{r}
	routesCache = append(rs, routesCache...)
}

func AddToLastRoute(r *Route) {
	routesMu.Lock()
	defer routesMu.Unlock()
	routesCache = append(routesCache, r)
}

func GetRoute(dst string) (transport.Transport, error) {
	if route, ok := table.Get(dst); ok {
		return route.GetTransport()
	}
	routesMu.RLock()
	defer routesMu.RUnlock()
	return routesCache.GetRoute(dst)
}

func GenerateCache() error {
	table.Reset()
	routesMu.RLock()
	defer routesMu.RUnlock()
	return routesCache.GenerateCache()
}

func SetRoutes(r Routes) {
	table.Reset()
	routesMu.Lock()
	defer routesMu.Unlock()
	routesCache = r
}
