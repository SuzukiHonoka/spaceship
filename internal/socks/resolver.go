package socks

import (
	"context"
	"net"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

// NameResolver is used to implement custom name resolution
type NameResolver interface {
	Resolve(ctx context.Context, name string) (context.Context, net.IP, error)
}

// DNSResolver uses the system DNS to resolve host names
type DNSResolver struct{}

func (d DNSResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	addrs, err := transport.OutboundResolver().LookupIPAddr(ctx, name)
	if err != nil {
		return ctx, nil, err
	}
	if len(addrs) == 0 {
		return ctx, nil, &net.DNSError{Name: name, Err: "no addresses"}
	}
	return ctx, addrs[0].IP, nil
}
