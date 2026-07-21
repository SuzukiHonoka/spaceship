package rpc

import (
	"context"
	"crypto/tls"
	"net"
	"runtime"
	"slices"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/experimental"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/mem"
)

const (
	GeneralTimeout = 15 * time.Second

	// MaxMessageSize bounds a single gRPC message in either direction. A payload
	// chunk is one full transport buffer wrapped in a ProxySRC/ProxyDST envelope,
	// so this must stay above the configured transport buffer size — see
	// MaxTransportBufferSize, which config validation enforces.
	MaxMessageSize = 4 * 1024 * 1024

	// MessageFramingOverhead reserves room for the protobuf field tag and varint
	// length prefix wrapped around a payload chunk, plus the gRPC message header.
	// Used to size buffer-pool tiers and to bound the transport buffer.
	MessageFramingOverhead = 1024

	// MaxTransportBufferSize is the largest transport buffer that still produces
	// a message within MaxMessageSize.
	MaxTransportBufferSize = MaxMessageSize - MessageFramingOverhead

	// connBufferSize is the per-connection read/write batching buffer. This is
	// also the gRPC default; it is stated explicitly because SharedWriteBuffer
	// changes how the write side is allocated and the pairing matters.
	connBufferSize = 32 * 1024

	// keepaliveTime is how often an idle connection is pinged to detect a peer
	// that vanished without a FIN (NAT rebinding, silent middlebox drop).
	keepaliveTime = 30 * time.Second

	// keepaliveMinTime is the smallest client ping interval the server tolerates
	// before returning GOAWAY. Must stay below keepaliveTime or well-behaved
	// clients would be disconnected for pinging too often.
	keepaliveMinTime = 10 * time.Second

	// maxConcurrentStreams caps in-flight streams per connection. Each proxied
	// connection is one stream, so without this a single authenticated client
	// can allocate unbounded server goroutines and buffers (the grpc-go default
	// is math.MaxUint32). Generous enough that legitimate clients never hit it.
	maxConcurrentStreams = 4096
)

var DefaultCurvePreferences = []tls.CurveID{
	tls.X25519,
	tls.CurveP256,
}

// payloadBufferPool returns a buffer pool with a tier sized for this process's
// transport buffer.
//
// gRPC's default tiers are 256B/4KB/16KB/32KB/1MB. A payload chunk is a full
// transport buffer (32KB by default) plus protobuf framing, which lands just
// past the 32KB tier — so by default every in-flight message would take a 1MB
// slab. Adding an exact tier keeps those allocations proportional to the
// payload instead.
//
// Must be called after the config has been applied, so that
// transport.GetBufferSize reflects the configured value.
func payloadBufferPool() mem.BufferPool {
	sizes := []int{256, 4 * 1024, 16 * 1024, 32 * 1024, 1024 * 1024}
	sizes = append(sizes, transport.GetBufferSize()+MessageFramingOverhead)
	slices.Sort(sizes)
	return mem.NewTieredBufferPool(slices.Compact(sizes)...)
}

// dialContext dials the control connection to the spaceship server.
//
// This deliberately uses "tcp" rather than transport.DialNetwork: the ipv6
// setting governs egress to proxied destinations, not how we reach our own
// server. Forcing IPv4 here would break a v6-only server endpoint for an
// operator who merely wanted IPv6 destinations blocked.
func dialContext(ctx context.Context, addr string) (net.Conn, error) {
	return (&net.Dialer{Timeout: GeneralTimeout}).DialContext(ctx, "tcp", addr)
}

// clientKeepaliveParams and serverKeepaliveParams are separate functions purely
// so tests can assert the invariants that matter — grpc.DialOption and
// grpc.ServerOption values are opaque once constructed, so the parameters would
// otherwise be unverifiable.
func clientKeepaliveParams() keepalive.ClientParameters {
	return keepalive.ClientParameters{
		Time:                keepaliveTime,
		Timeout:             GeneralTimeout,
		PermitWithoutStream: true,
	}
}

func serverKeepaliveParams() keepalive.ServerParameters {
	return keepalive.ServerParameters{
		// MaxConnectionAge and MaxConnectionAgeGrace are deliberately left zero
		// (infinite). They exist to force periodic reconnection for L4
		// load-balancer rebalancing; in a tunnel they would sever every proxied
		// session still open at the age limit, regardless of activity. Dead peers
		// are detected by the keepalive pings instead.
		Time:    keepaliveTime,
		Timeout: GeneralTimeout,
	}
}

// DialOptions returns the base client dial options. It is a function rather
// than a package-level slice so each caller gets an independent slice (appending
// to a shared slice risks aliasing its backing array) and so buffer-pool tiers
// reflect the applied config.
func DialOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithKeepaliveParams(clientKeepaliveParams()),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.DefaultConfig,
			MinConnectTimeout: GeneralTimeout,
		}),
		grpc.WithContextDialer(dialContext),
		grpc.WithWriteBufferSize(connBufferSize),
		grpc.WithReadBufferSize(connBufferSize),
		// One write buffer per connection instead of per stream. A proxy
		// multiplexes many streams onto few connections, so this keeps write
		// buffer memory flat as concurrent connections grow.
		grpc.WithSharedWriteBuffer(true),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(MaxMessageSize),
			grpc.MaxCallSendMsgSize(MaxMessageSize),
			// WaitForReady is deliberately NOT set. With it, an RPC blocks until
			// the channel becomes READY, so an unreachable server turns every
			// proxied request into an indefinite hang instead of a clean error
			// the SOCKS/HTTP client can act on. Fail-fast only applies once a
			// connection attempt has actually failed — RPCs issued while the
			// channel is CONNECTING still wait for it.
		),
		grpc.WithDisableServiceConfig(),
		// No WithUserAgent: gRPC's default "grpc-go/<version>" is
		// indistinguishable from any other Go gRPC client, whereas advertising
		// the product name is a gratuitous fingerprint anywhere TLS is
		// terminated or logged upstream (e.g. an nginx reverse proxy).
		experimental.WithBufferPool(payloadBufferPool()),
	}
}

// ServerOptions returns the base server options. See DialOptions for why this
// is a function.
func ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ReadBufferSize(connBufferSize),
		grpc.WriteBufferSize(connBufferSize),
		grpc.SharedWriteBuffer(true),
		grpc.MaxRecvMsgSize(MaxMessageSize),
		grpc.MaxSendMsgSize(MaxMessageSize),
		grpc.MaxConcurrentStreams(maxConcurrentStreams),
		// Reuse a fixed pool of goroutines for stream handling rather than
		// spawning one per stream. GOMAXPROCS (container-aware since Go 1.25) is
		// the value upstream benchmarks found most performant.
		grpc.NumStreamWorkers(uint32(runtime.GOMAXPROCS(0))),
		grpc.KeepaliveParams(serverKeepaliveParams()),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             keepaliveMinTime,
			PermitWithoutStream: true,
		}),
		grpc.ConnectionTimeout(GeneralTimeout),
		experimental.BufferPool(payloadBufferPool()),
		// WaitForHandlers is deliberately NOT set. ListenAndServe already bounds
		// shutdown by racing GracefulStop against a timeout and falling back to
		// Stop; making Stop itself wait for handlers would make that fallback
		// unbounded, which is the opposite of what it exists for.
	}
}
