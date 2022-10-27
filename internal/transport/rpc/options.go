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
		Time:    10 * time.Second,
		Timeout: 5 * time.Second,
	}),
	grpc.WithConnectParams(grpc.ConnectParams{
		//value of first 3 fields is from backoff.DefaultConfig
		Backoff: backoff.Config{
			BaseDelay:  1.0 * time.Second,
			Multiplier: 1.6,
			Jitter:     0.2,
			MaxDelay:   5 * time.Second,
		},
		MinConnectTimeout: 5 * time.Second,
	}),
	grpc.WithContextDialer(func(ctx context.Context, s string) (net.Conn, error) {
		return (&net.Dialer{
			Timeout: 5 * time.Second,
		}).DialContext(ctx, "tcp", s)
	}),
	grpc.WithUserAgent("spaceship/" + manifest.VersionCode),
}

var ServerOptions = []grpc.ServerOption{
	// without r/w buffer for less delay
	grpc.ReadBufferSize(0),
	grpc.WriteBufferSize(0),
	grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
		MinTime: 10 * time.Second,
	}),
	grpc.ConnectionTimeout(5 * time.Second),
}
