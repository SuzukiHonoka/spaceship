package client

import (
	"context"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"google.golang.org/grpc"
)

var (
	staticCallOpts  = []grpc.CallOption{grpc.StaticMethod()}
	proxyStreamDesc = grpc.StreamDesc{
		StreamName:    "Proxy",
		ServerStreams: true,
		ClientStreams: true,
	}
	proxyPacketStreamDesc = grpc.StreamDesc{
		StreamName:    "ProxyPacket",
		ServerStreams: true,
		ClientStreams: true,
	}
)

// DynamicProxyClient wraps a gRPC connection to use configurable service names.
type DynamicProxyClient struct {
	conn                  grpc.ClientConnInterface
	dnsResolveName        string
	dnsExchangeName       string
	proxyMethodName       string
	proxyPacketMethodName string
}

// Ensure DynamicProxyClient implements proto.ProxyClient interface
var _ proto.ProxyClient = (*DynamicProxyClient)(nil)

// NewDynamicProxyClient creates a client that respects the configured service name.
// Method names are resolved once at construction time to avoid per-call overhead.
func NewDynamicProxyClient(conn grpc.ClientConnInterface) *DynamicProxyClient {
	return &DynamicProxyClient{
		conn:                  conn,
		dnsResolveName:        rpc.GetDnsResolveMethodName(),
		dnsExchangeName:       rpc.GetDnsExchangeMethodName(),
		proxyMethodName:       rpc.GetProxyMethodName(),
		proxyPacketMethodName: rpc.GetProxyPacketMethodName(),
	}
}

func withStaticMethod(opts []grpc.CallOption) []grpc.CallOption {
	if len(opts) == 0 {
		return staticCallOpts
	}
	merged := make([]grpc.CallOption, 1+len(opts))
	merged[0] = grpc.StaticMethod()
	copy(merged[1:], opts)
	return merged
}

// DnsResolve calls DnsResolve using the configured service name
func (c *DynamicProxyClient) DnsResolve(ctx context.Context, in *proto.DnsRequest, opts ...grpc.CallOption) (*proto.DnsResponse, error) {
	out := new(proto.DnsResponse)
	err := c.conn.Invoke(ctx, c.dnsResolveName, in, out, withStaticMethod(opts)...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DnsExchange calls DnsExchange using the configured service name.
func (c *DynamicProxyClient) DnsExchange(ctx context.Context, in *proto.DnsExchangeRequest, opts ...grpc.CallOption) (*proto.DnsExchangeResponse, error) {
	out := new(proto.DnsExchangeResponse)
	err := c.conn.Invoke(ctx, c.dnsExchangeName, in, out, withStaticMethod(opts)...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Proxy calls Proxy using the configured service name
func (c *DynamicProxyClient) Proxy(ctx context.Context, opts ...grpc.CallOption) (grpc.BidiStreamingClient[proto.ProxySRC, proto.ProxyDST], error) {
	stream, err := c.conn.NewStream(ctx, &proxyStreamDesc, c.proxyMethodName, withStaticMethod(opts)...)
	if err != nil {
		return nil, err
	}

	return &grpc.GenericClientStream[proto.ProxySRC, proto.ProxyDST]{ClientStream: stream}, nil
}

// ProxyPacket calls ProxyPacket using the configured service name.
func (c *DynamicProxyClient) ProxyPacket(ctx context.Context, opts ...grpc.CallOption) (grpc.BidiStreamingClient[proto.PacketFrame, proto.PacketFrame], error) {
	methodName := c.proxyPacketMethodName
	if methodName == "" {
		methodName = rpc.GetProxyPacketMethodName()
	}
	stream, err := c.conn.NewStream(ctx, &proxyPacketStreamDesc, methodName, withStaticMethod(opts)...)
	if err != nil {
		return nil, err
	}

	return &grpc.GenericClientStream[proto.PacketFrame, proto.PacketFrame]{ClientStream: stream}, nil
}
