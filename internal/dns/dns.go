package dns

import (
	"context"
	"fmt"
	"net"
	"time"
)

type DNS struct {
	Type
	Server string
}

func (s *DNS) SetDefault() {
	if s.Type != TypeCommon {
		panic("not implemented")
	}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: 5 * time.Second,
			}
			return d.DialContext(ctx, network, fmt.Sprintf("%s:53", s.Server))
		},
	}
}
