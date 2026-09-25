package rpc

import (
	"context"
	"crypto/tls"
	"math"
	"net"
	"runtime"
	"sync/atomic"
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

	// connBufferSize is the per-connection read/write batching buffer. The
	// gRPC default is 32KiB; a tunnel that moves full transport-buffer chunks
	// benefits from a larger batch so HTTP/2 DATA frames coalesce into fewer
	// syscalls on the control connection.
	connBufferSize = 512 * 1024

	// initialStreamWindow / initialConnWindow raise HTTP/2 flow-control above
	// the 64KiB spec default so several in-flight payload chunks (up to the
	// transport buffer) do not stall waiting for WINDOW_UPDATE on loopback
	// or LAN links. BDP estimation remains enabled (StaticWindowSize is unset).
	initialStreamWindow = 4 << 20  // 4 MiB
	initialConnWindow   = 16 << 20 // 16 MiB

	// keepaliveTime is how often an idle connection is pinged to detect a peer
	// that vanished without a FIN (NAT rebinding, silent middlebox drop).
	keepaliveTime = 30 * time.Second

	// keepaliveMinTime is the smallest client ping interval the server tolerates
	// before returning GOAWAY. Must stay below keepaliveTime or well-behaved
	// clients would be disconnected for pinging too often.
	keepaliveMinTime = 10 * time.Second

	// MaxConcurrentStreams caps in-flight streams per connection. Each proxied
	// connection is one stream, so without this a single authenticated client
	// can allocate unbounded server goroutines and buffers (the grpc-go default
	// is math.MaxUint32). The TUN config uses the same exported value to ensure
	// its connection pool has enough aggregate stream capacity.
	MaxConcurrentStreams uint32 = 4096
)

var DefaultCurvePreferences = []tls.CurveID{
	tls.X25519,
	tls.CurveP256,
}

// payloadBufferPool returns a buffer pool with a tier sized for this process's
// transport buffer.
//
// gRPC's default tiers are 256B/4KB/16KB/32KB/1MB. A payload chunk is a full
// transport buffer plus protobuf framing; without an exact tier those chunks
// fall into the next stock size (often the 1MB slab). Adding a tier at
// GetBufferSize()+overhead keeps allocations proportional to the payload, and
// the 64KB/128KB tiers do the same for chunks that arrive split across HTTP/2
// frames and are coalesced. The pool does not zero buffers; see
// dirtyTieredPool for why that is safe and why it matters.
//
// Must be called after the config has been applied, so that
// transport.GetBufferSize reflects the configured value. The returned pool is
// also stored for BufferPool() so forwarders/codec share the same tiers without
// racing on experimental.SetDefaultBufferPool (unsafe under parallel Dial/Serve).
func payloadBufferPool() mem.BufferPool {
	sizes := []int{256, 4 * 1024, 16 * 1024, 32 * 1024, 64 * 1024, 128 * 1024, 1024 * 1024}
	// Exact tiers for a raw transport-buffer read (with in-place protobuf
	// header reserve) and for a framed payload chunk so neither Get falls
	// into the 1MB slab.
	buf := transport.GetBufferSize()
	sizes = append(sizes, buf, buf+maxProtobufBytesHeader, buf+MessageFramingOverhead)
	pool := newDirtyTieredPool(sizes...)
	setBufferPool(pool)
	return pool
}

var bufferPool atomic.Value // mem.BufferPool

func setBufferPool(pool mem.BufferPool) {
	bufferPool.Store(pool)
}

// BufferPool returns the sized tiered pool last installed by DialOptions /
// ServerOptions, or gRPC's default pool if neither has run yet.
func BufferPool() mem.BufferPool {
	if p, ok := bufferPool.Load().(mem.BufferPool); ok && p != nil {
		return p
	}
	return mem.DefaultBufferPool()
}

// dialContext dials the control connection to the spaceship server.
//
// This deliberately uses "tcp" rather than transport.DialNetwork: the ipv6
// setting governs egress to proxied destinations, not how we reach our own
// server. Forcing IPv4 here would break a v6-only server endpoint for an
// operator who merely wanted IPv6 destinations blocked.
func dialContext(ctx context.Context, addr string) (net.Conn, error) {
	return transport.NewOutboundDialer(GeneralTimeout).DialContext(ctx, "tcp", addr)
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
	pool := payloadBufferPool()
	return []grpc.DialOption{
		grpc.WithKeepaliveParams(clientKeepaliveParams()),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.DefaultConfig,
			MinConnectTimeout: GeneralTimeout,
		}),
		grpc.WithContextDialer(dialContext),
		grpc.WithWriteBufferSize(connBufferSize),
		grpc.WithReadBufferSize(connBufferSize),
		grpc.WithInitialWindowSize(initialStreamWindow),
		grpc.WithInitialConnWindowSize(initialConnWindow),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(MaxMessageSize),
			grpc.MaxCallSendMsgSize(MaxMessageSize),
			// Force the tunnel-specialized codec on every call. Name() is still
			// "proto", so the content-type and on-wire framing stay compatible
			// with stock protobuf peers; only the local encode/decode path
			// changes.
			grpc.ForceCodecV2(proxyCodec{}),
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
		experimental.WithBufferPool(pool),
	}
}

func streamWorkerCount() uint32 {
	workers := runtime.GOMAXPROCS(0)
	if int64(workers) > math.MaxUint32 {
		return math.MaxUint32
	}
	// GOMAXPROCS is positive and the upper bound above proves the conversion.
	return uint32(workers) // #nosec G115 -- explicitly bounds the conversion
}

// ServerOptions returns the base server options. See DialOptions for why this
// is a function.
func ServerOptions() []grpc.ServerOption {
	pool := payloadBufferPool()
	return []grpc.ServerOption{
		grpc.ReadBufferSize(connBufferSize),
		grpc.WriteBufferSize(connBufferSize),
		grpc.InitialWindowSize(initialStreamWindow),
		grpc.InitialConnWindowSize(initialConnWindow),
		grpc.MaxRecvMsgSize(MaxMessageSize),
		grpc.MaxSendMsgSize(MaxMessageSize),
		grpc.MaxConcurrentStreams(MaxConcurrentStreams),
		// Reuse a fixed pool of goroutines for stream handling rather than
		// spawning one per stream. GOMAXPROCS (container-aware since Go 1.25) is
		// the value upstream benchmarks found most performant.
		grpc.NumStreamWorkers(streamWorkerCount()),
		grpc.KeepaliveParams(serverKeepaliveParams()),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             keepaliveMinTime,
			PermitWithoutStream: true,
		}),
		grpc.ConnectionTimeout(GeneralTimeout),
		grpc.ForceServerCodecV2(proxyCodec{}),
		experimental.BufferPool(pool),
		// WaitForHandlers is deliberately NOT set. Signal-driven shutdown calls
		// Stop so every transport is closed immediately, even if an application
		// handler is still blocked in a cancellation-unaware target dial.
	}
}
