// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.4.0
// - protoc             v5.27.2
// source: proxy.proto

package proxy

import (
	context "context"
	"fmt"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

var (
	Proxy_DnsResolve_FullMethodName = "/proxy.Proxy/DnsResolve"
	Proxy_Proxy_FullMethodName      = "/proxy.Proxy/Proxy"
)

func SetServiceName(name string) {
	Proxy_ServiceDesc.ServiceName = name
	Proxy_DnsResolve_FullMethodName = fmt.Sprintf("/%s/DnsResolve", Proxy_ServiceDesc.ServiceName)
	Proxy_Proxy_FullMethodName = fmt.Sprintf("/%s/Proxy", Proxy_ServiceDesc.ServiceName)
}

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.62.0 or later.
const _ = grpc.SupportPackageIsVersion8

// ProxyClient is the client API for Proxy service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type ProxyClient interface {
	DnsResolve(ctx context.Context, opts ...grpc.CallOption) (Proxy_DnsResolveClient, error)
	Proxy(ctx context.Context, opts ...grpc.CallOption) (Proxy_ProxyClient, error)
}

type proxyClient struct {
	cc grpc.ClientConnInterface
}

func NewProxyClient(cc grpc.ClientConnInterface) ProxyClient {
	return &proxyClient{cc}
}

func (c *proxyClient) DnsResolve(ctx context.Context, opts ...grpc.CallOption) (Proxy_DnsResolveClient, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	stream, err := c.cc.NewStream(ctx, &Proxy_ServiceDesc.Streams[0], Proxy_DnsResolve_FullMethodName, cOpts...)
	if err != nil {
		return nil, err
	}
	x := &proxyDnsResolveClient{ClientStream: stream}
	return x, nil
}

type Proxy_DnsResolveClient interface {
	Send(*DnsRequest) error
	Recv() (*DnsResponse, error)
	grpc.ClientStream
}

type proxyDnsResolveClient struct {
	grpc.ClientStream
}

func (x *proxyDnsResolveClient) Send(m *DnsRequest) error {
	return x.ClientStream.SendMsg(m)
}

func (x *proxyDnsResolveClient) Recv() (*DnsResponse, error) {
	m := new(DnsResponse)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *proxyClient) Proxy(ctx context.Context, opts ...grpc.CallOption) (Proxy_ProxyClient, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	stream, err := c.cc.NewStream(ctx, &Proxy_ServiceDesc.Streams[1], Proxy_Proxy_FullMethodName, cOpts...)
	if err != nil {
		return nil, err
	}
	x := &proxyProxyClient{ClientStream: stream}
	return x, nil
}

type Proxy_ProxyClient interface {
	Send(*ProxySRC) error
	Recv() (*ProxyDST, error)
	grpc.ClientStream
}

type proxyProxyClient struct {
	grpc.ClientStream
}

func (x *proxyProxyClient) Send(m *ProxySRC) error {
	return x.ClientStream.SendMsg(m)
}

func (x *proxyProxyClient) Recv() (*ProxyDST, error) {
	m := new(ProxyDST)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

// ProxyServer is the server API for Proxy service.
// All implementations must embed UnimplementedProxyServer
// for forward compatibility
type ProxyServer interface {
	DnsResolve(Proxy_DnsResolveServer) error
	Proxy(Proxy_ProxyServer) error
	mustEmbedUnimplementedProxyServer()
}

// UnimplementedProxyServer must be embedded to have forward compatible implementations.
type UnimplementedProxyServer struct {
}

func (UnimplementedProxyServer) DnsResolve(Proxy_DnsResolveServer) error {
	return status.Errorf(codes.Unimplemented, "method DnsResolve not implemented")
}
func (UnimplementedProxyServer) Proxy(Proxy_ProxyServer) error {
	return status.Errorf(codes.Unimplemented, "method Proxy not implemented")
}
func (UnimplementedProxyServer) mustEmbedUnimplementedProxyServer() {}

// UnsafeProxyServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to ProxyServer will
// result in compilation errors.
type UnsafeProxyServer interface {
	mustEmbedUnimplementedProxyServer()
}

func RegisterProxyServer(s grpc.ServiceRegistrar, srv ProxyServer) {
	s.RegisterService(&Proxy_ServiceDesc, srv)
}

func _Proxy_DnsResolve_Handler(srv interface{}, stream grpc.ServerStream) error {
	return srv.(ProxyServer).DnsResolve(&proxyDnsResolveServer{ServerStream: stream})
}

type Proxy_DnsResolveServer interface {
	Send(*DnsResponse) error
	Recv() (*DnsRequest, error)
	grpc.ServerStream
}

type proxyDnsResolveServer struct {
	grpc.ServerStream
}

func (x *proxyDnsResolveServer) Send(m *DnsResponse) error {
	return x.ServerStream.SendMsg(m)
}

func (x *proxyDnsResolveServer) Recv() (*DnsRequest, error) {
	m := new(DnsRequest)
	if err := x.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func _Proxy_Proxy_Handler(srv interface{}, stream grpc.ServerStream) error {
	return srv.(ProxyServer).Proxy(&proxyProxyServer{ServerStream: stream})
}

type Proxy_ProxyServer interface {
	Send(*ProxyDST) error
	Recv() (*ProxySRC, error)
	grpc.ServerStream
}

type proxyProxyServer struct {
	grpc.ServerStream
}

func (x *proxyProxyServer) Send(m *ProxyDST) error {
	return x.ServerStream.SendMsg(m)
}

func (x *proxyProxyServer) Recv() (*ProxySRC, error) {
	m := new(ProxySRC)
	if err := x.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

// Proxy_ServiceDesc is the grpc.ServiceDesc for Proxy service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var Proxy_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "proxy.Proxy",
	HandlerType: (*ProxyServer)(nil),
	Methods:     []grpc.MethodDesc{},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "DnsResolve",
			Handler:       _Proxy_DnsResolve_Handler,
			ServerStreams: true,
			ClientStreams: true,
		},
		{
			StreamName:    "Proxy",
			Handler:       _Proxy_Proxy_Handler,
			ServerStreams: true,
			ClientStreams: true,
		},
	},
	Metadata: "proxy.proto",
}
