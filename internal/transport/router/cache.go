package router

var (
	routesCache Routes
	table       = &syncedRoutesTable{
		RoutesTable: make(RoutesTable),
	}
)

func GetCount() int {
	return len(routesCache)
}

func AddToFirstRoute(r *Route) {
	rs := Routes{r}
	routesCache = append(rs, routesCache...)
}

func AddToLastRoute(r *Route) {
	routesCache = append(routesCache, r)
}

func GetRoutes() Routes {
	return routesCache
}

func SetRoutes(r Routes) {
	routesCache = r
}
