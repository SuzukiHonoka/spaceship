package rpc

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/pkg/config/manifest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/keepalive"
)

const GeneralTimeout = 15 * time.Second

var DefaultCurvePreferences = []tls.CurveID{
	tls.X25519,
	tls.CurveP256,
}

var DialOptions = []grpc.DialOption{
	grpc.WithKeepaliveParams(keepalive.ClientParameters{
		Time:                10 * time.Second,
		Timeout:             GeneralTimeout,
		PermitWithoutStream: true,
	}),
	grpc.WithConnectParams(grpc.ConnectParams{
		Backoff:           backoff.DefaultConfig,
		MinConnectTimeout: GeneralTimeout,
	}),
	grpc.WithContextDialer(func(ctx context.Context, s string) (net.Conn, error) {
		return (&net.Dialer{
			Timeout: GeneralTimeout,
		}).DialContext(ctx, "tcp", s)
	}),
	grpc.WithWriteBufferSize(0),
	grpc.WithReadBufferSize(0),
	grpc.WithDisableServiceConfig(),
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
	grpc.ConnectionTimeout(GeneralTimeout),
}
