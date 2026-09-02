package server

import (
	"context"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"google.golang.org/grpc"
)

// DynamicProxyServer allows registration with configurable service names
type DynamicProxyServer struct {
	impl proto.ProxyServer
}

// NewDynamicProxyServer creates a server wrapper that uses configurable service names
func NewDynamicProxyServer(impl proto.ProxyServer) *DynamicProxyServer {
	return &DynamicProxyServer{impl: impl}
}

// RegisterWithGRPC registers the service with a gRPC server using the configured service name
func (s *DynamicProxyServer) RegisterWithGRPC(grpcServer *grpc.Server) {
	// Create a custom service descriptor with the dynamic name
	serviceName := rpc.GetServiceName()

	customDesc := &grpc.ServiceDesc{
		ServiceName: serviceName,
		HandlerType: (*proto.ProxyServer)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "DnsResolve",
				Handler:    s.createDnsResolveHandler(),
			},
			{
				MethodName: "DnsExchange",
				Handler:    s.createDnsExchangeHandler(),
			},
		},
		Streams: []grpc.StreamDesc{
			{
				StreamName:    "Proxy",
				Handler:       s.createProxyHandler(),
				ServerStreams: true,
				ClientStreams: true,
			},
		},
		Metadata: "proxy.proto",
	}

	grpcServer.RegisterService(customDesc, s.impl)
}

// createDnsExchangeHandler creates a handler for DnsExchange.
func (s *DynamicProxyServer) createDnsExchangeHandler() grpc.MethodHandler {
	return func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		in := new(proto.DnsExchangeRequest)
		if err := dec(in); err != nil {
			return nil, err
		}
		if interceptor == nil {
			return srv.(proto.ProxyServer).DnsExchange(ctx, in)
		}
		info := &grpc.UnaryServerInfo{
			Server:     srv,
			FullMethod: rpc.GetDnsExchangeMethodName(),
		}
		handler := func(ctx context.Context, req any) (any, error) {
			return srv.(proto.ProxyServer).DnsExchange(ctx, req.(*proto.DnsExchangeRequest))
		}
		return interceptor(ctx, in, info, handler)
	}
}

// createDnsResolveHandler creates a handler for DnsResolve method
func (s *DynamicProxyServer) createDnsResolveHandler() grpc.MethodHandler {
	return func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		in := new(proto.DnsRequest)
		if err := dec(in); err != nil {
			return nil, err
		}
		if interceptor == nil {
			return srv.(proto.ProxyServer).DnsResolve(ctx, in)
		}
		info := &grpc.UnaryServerInfo{
			Server:     srv,
			FullMethod: rpc.GetDnsResolveMethodName(),
		}
		handler := func(ctx context.Context, req any) (any, error) {
			return srv.(proto.ProxyServer).DnsResolve(ctx, req.(*proto.DnsRequest))
		}
		return interceptor(ctx, in, info, handler)
	}
}

// createProxyHandler creates a handler for Proxy method
func (s *DynamicProxyServer) createProxyHandler() grpc.StreamHandler {
	return func(srv any, stream grpc.ServerStream) error {
		return srv.(proto.ProxyServer).Proxy(&grpc.GenericServerStream[proto.ProxySRC, proto.ProxyDST]{ServerStream: stream})
	}
}
