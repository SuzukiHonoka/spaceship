package router

import "github.com/SuzukiHonoka/spaceship/v2/internal/transport"

var (
	routesCache Routes
	table       = newSyncedRoutesTable(maxCacheSize)
)

func AddToFirstRoute(r *Route) {
	rs := Routes{r}
	routesCache = append(rs, routesCache...)
}

func AddToLastRoute(r *Route) {
	routesCache = append(routesCache, r)
}

func GetRoute(dst string) (transport.Transport, error) {
	return routesCache.GetRoute(dst)
}

func GenerateCache() error {
	table.Reset()
	return routesCache.GenerateCache()
}

func SetRoutes(r Routes) {
	table.Reset()
	routesCache = r
}
