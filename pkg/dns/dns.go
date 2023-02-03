package dns

import (
	"context"
	"net"
	"time"
)

type DNS struct {
	Type
	Server string
}

func (s *DNS) SetDefault() {
	// typeCommon in Type is as empty string or "common"
	if s.Type != "" && s.Type != TypeCommon {
		panic("not implemented")
	}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: 5 * time.Second,
			}
			return d.DialContext(ctx, network, net.JoinHostPort(s.Server, "53"))
		},
	}
}
