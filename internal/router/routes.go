package router

import (
	"fmt"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

type Routes []*Route

func (r Routes) GenerateCache() error {
	for _, route := range r {
		if err := route.GenerateCache(); err != nil {
			return err
		}
	}
	return nil
}

func (r Routes) GetRoute(dst string) (transport.Transport, error) {
	for _, route := range r {
		if route.Match(dst) {
			table.Set(dst, route.Destination)
			//log.Printf("route cached: %s -> %s", dst, route.Destination)
			return route.Destination.GetTransport()
		}
	}
	return nil, fmt.Errorf("route not found: %s -> nil", dst)
}
