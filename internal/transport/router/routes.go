package router

import (
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"log"
)

type Routes []*Route

func (r Routes) GenerateCache() {
	for _, route := range r {
		route.GenerateCache()
	}
}

func (r Routes) GetRoute(dst string) transport.Transport {
	if route, ok := table.Get(dst); ok {
		//log.Printf("cache hit: %s -> %s", dst, route)
		return route.GetTransport()
	}
	if len(r) == 0 {
		return nil
	}
	for _, route := range r {
		if route.Match(dst) {
			table.Set(dst, route.Destination)
			//log.Printf("route cached: %s -> %s", dst, route.Destination)
			return route.Destination.GetTransport()
		}
	}
	log.Printf("route not found: %s -> nil", dst)
	return nil
}
