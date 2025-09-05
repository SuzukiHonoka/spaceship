package dns

import (
	"context"
	"fmt"
	"net"
	"time"
)

var DefaultTimeout = 3 * time.Second

type DNS struct {
	Type
	Server string
}

func (s *DNS) String() string {
	return fmt.Sprintf("dns(type=%s, server=%s)", s.Type, s.Server)
}

func (s *DNS) Address() string {
	switch s.Type {
	case TypeDefault, TypeCommon:
		return net.JoinHostPort(s.Server, "53")
	}
	return ""
}

func (s *DNS) SetDefault() error {
	switch s.Type {
	case TypeDefault, TypeCommon:
		net.DefaultResolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{
					Timeout: DefaultTimeout,
				}
				return d.DialContext(ctx, network, s.Address())
			},
		}
	default:
		return fmt.Errorf("dns: type %s not implemented, abort setting default", s.Type)
	}
	return nil
}
