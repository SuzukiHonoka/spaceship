package router

var (
	RoutesCache Routes
	table       = &syncedRoutesTable{
		RoutesTable: make(RoutesTable),
	}
)
