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

// Address returns the resolver endpoint to query.
//
// Server may be a bare host ("1.1.1.1", "::1", "resolver.example") or already
// carry a port ("127.0.0.1:5353"). Bare hosts get the standard port 53 appended;
// anything that already parses as host:port is used verbatim, so a resolver on a
// non-standard port — a local stub resolver, a container sidecar — is reachable.
func (s *DNS) Address() string {
	switch s.Type {
	case TypeDefault, TypeCommon:
		if _, _, err := net.SplitHostPort(s.Server); err == nil {
			return s.Server
		}
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
