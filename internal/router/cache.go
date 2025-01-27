package router

import "github.com/SuzukiHonoka/spaceship/internal/transport"

var (
	routesCache Routes
	table       = new(syncedRoutesTable)
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
	return routesCache.GenerateCache()
}

func SetRoutes(r Routes) {
	routesCache = r
}
