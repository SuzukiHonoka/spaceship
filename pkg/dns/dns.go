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

func (s *DNS) SetDefault() error {
	switch s.Type {
	case TypeDefault, TypeCommon:
		net.DefaultResolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{
					Timeout: 3 * time.Second,
				}
				return d.DialContext(ctx, network, net.JoinHostPort(s.Server, "53"))
			},
		}
	default:
		return fmt.Errorf("dns: type %s not implemented, abort setting default", s.Type)
	}
	return nil
}
