package router

import (
	"net/netip"
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

// GetRouteObserved selects a route when dialHost is the IP the client asked
// to reach and name is a hostname recovered from that connection's first
// flight. CIDR rules match the IP. Exact, domain, and regex rules match the
// name, and still match the IP so a literal address in those rules keeps
// working. The first matching rule wins. An empty name, or a name that is
// itself an IP, selects the same route as GetRoute(dialHost).
func GetRouteObserved(dialHost, name string) (transport.Transport, error) {
	ipKey := normalizeRouteKey(dialHost)
	nameKey := normalizeRouteKey(name)
	if nameKey == "" || nameKey == ipKey {
		return GetRoute(ipKey)
	}
	if _, err := netip.ParseAddr(nameKey); err == nil {
		return GetRoute(ipKey)
	}
	key := nameKey + "\x00" + ipKey
	routesMu.RLock()
	defer routesMu.RUnlock()
	if route, ok := table.Get(key); ok {
		return route.GetTransport()
	}
	return routesCache.getObserved(key, ipKey, nameKey)
}

// MatchEgress is the egress GetRouteObserved would select, without constructing
// its transport. Front ends that already hold an admitted proxy session use it
// so a proxy match does not check another connection out of the pool.
func MatchEgress(dialHost, name string) (Egress, error) {
	ipKey := normalizeRouteKey(dialHost)
	nameKey := normalizeRouteKey(name)
	if nameKey == "" || nameKey == ipKey {
		return matchEgress(ipKey)
	}
	if _, err := netip.ParseAddr(nameKey); err == nil {
		return matchEgress(ipKey)
	}
	key := nameKey + "\x00" + ipKey
	routesMu.RLock()
	defer routesMu.RUnlock()
	if egress, ok := table.Get(key); ok {
		return egress, nil
	}
	return routesCache.findEgress(key, nameKey, func(route *Route) bool {
		return matchObserved(route, ipKey, nameKey)
	})
}

func matchEgress(key string) (Egress, error) {
	routesMu.RLock()
	defer routesMu.RUnlock()
	if egress, ok := table.Get(key); ok {
		return egress, nil
	}
	return routesCache.findEgress(key, key, func(route *Route) bool { return route.Match(key) })
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
