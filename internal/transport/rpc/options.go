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
		Time:                30 * time.Second,
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
	grpc.WithWriteBufferSize(32 * 1024),
	grpc.WithReadBufferSize(32 * 1024),
	grpc.WithDefaultCallOptions(
		grpc.MaxCallRecvMsgSize(4*1024*1024),
		grpc.WaitForReady(true),
	),
	grpc.WithDisableServiceConfig(),
	grpc.WithUserAgent("spaceship/" + manifest.VersionCode),
}

var ServerOptions = []grpc.ServerOption{
	grpc.ReadBufferSize(32 * 1024),
	grpc.WriteBufferSize(32 * 1024),
	grpc.MaxRecvMsgSize(4 * 1024 * 1024),
	grpc.KeepaliveParams(keepalive.ServerParameters{
		MaxConnectionAge:      30 * time.Minute,
		MaxConnectionAgeGrace: 30 * time.Second,
		Time:                  30 * time.Second,
		Timeout:               GeneralTimeout,
	}),
	grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
		MinTime:             10 * time.Second,
		PermitWithoutStream: true,
	}),
	grpc.ConnectionTimeout(GeneralTimeout),
}
