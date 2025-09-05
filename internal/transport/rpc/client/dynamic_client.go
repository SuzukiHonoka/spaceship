package client

import (
	"context"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"google.golang.org/grpc"
)

// DynamicProxyClient wraps the generated client to use configurable service names
type DynamicProxyClient struct {
	conn   grpc.ClientConnInterface
	client proto.ProxyClient
}

// Ensure DynamicProxyClient implements proto.ProxyClient interface
var _ proto.ProxyClient = (*DynamicProxyClient)(nil)

// NewDynamicProxyClient creates a client that respects the configured service name
func NewDynamicProxyClient(conn grpc.ClientConnInterface) *DynamicProxyClient {
	return &DynamicProxyClient{
		conn:   conn,
		client: proto.NewProxyClient(conn),
	}
}

// DnsResolve calls DnsResolve using the configured service name
func (c *DynamicProxyClient) DnsResolve(ctx context.Context, in *proto.DnsRequest, opts ...grpc.CallOption) (*proto.DnsResponse, error) {
	// Use dynamic method name from configuration
	methodName := rpc.GetDnsResolveMethodName()

	// Add static method option with dynamic name
	opts = append([]grpc.CallOption{grpc.StaticMethod()}, opts...)

	out := new(proto.DnsResponse)
	err := c.conn.Invoke(ctx, methodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Proxy calls Proxy using the configured service name
func (c *DynamicProxyClient) Proxy(ctx context.Context, opts ...grpc.CallOption) (grpc.BidiStreamingClient[proto.ProxySRC, proto.ProxyDST], error) {
	// Use dynamic method name from configuration
	methodName := rpc.GetProxyMethodName()

	// Create custom stream descriptor with dynamic service name
	streamDesc := &grpc.StreamDesc{
		StreamName:    "Proxy",
		ServerStreams: true,
		ClientStreams: true,
	}

	opts = append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	stream, err := c.conn.NewStream(ctx, streamDesc, methodName, opts...)
	if err != nil {
		return nil, err
	}

	x := &grpc.GenericClientStream[proto.ProxySRC, proto.ProxyDST]{ClientStream: stream}
	return x, nil
}
