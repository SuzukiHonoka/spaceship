package rpc

import (
	"context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/keepalive"
	"net"
	"spaceship/internal/config/manifest"
	"time"
)

var DialOptions = []grpc.DialOption{
	grpc.WithKeepaliveParams(keepalive.ClientParameters{
		Time:                10 * time.Second,
		Timeout:             3 * time.Second,
		PermitWithoutStream: true,
	}),
	grpc.WithConnectParams(grpc.ConnectParams{
		Backoff:           backoff.DefaultConfig,
		MinConnectTimeout: 3 * time.Second,
	}),
	grpc.WithContextDialer(func(ctx context.Context, s string) (net.Conn, error) {
		return (&net.Dialer{
			Timeout: 3 * time.Second,
		}).DialContext(ctx, "tcp", s)
	}),
	grpc.WithUserAgent("spaceship/" + manifest.VersionCode),
}

var ServerOptions = []grpc.ServerOption{
	// without r/w buffer for less delay
	grpc.ReadBufferSize(0),
	grpc.WriteBufferSize(0),
	grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
		MinTime:             5 * time.Second,
		PermitWithoutStream: true,
	}),
	grpc.ConnectionTimeout(3 * time.Second),
}
