package proxy

import (
	"fmt"

	"google.golang.org/grpc"
)

// DynamicProxyService allows configurable service names
type DynamicProxyService struct {
	serviceName string
	server      ProxyServer
}

// NewDynamicProxyService creates a proxy service with configurable name
func NewDynamicProxyService(serviceName string, server ProxyServer) *DynamicProxyService {
	if serviceName == "" {
		serviceName = "proxy.Proxy" // Default service name
	}
	return &DynamicProxyService{
		serviceName: serviceName,
		server:      server,
	}
}

// GetServiceName returns the current service name
func (d *DynamicProxyService) GetServiceName() string {
	return d.serviceName
}

// GetMethodNames returns the full method names with current service name
func (d *DynamicProxyService) GetMethodNames() (dnsResolve, proxy string) {
	return fmt.Sprintf("/%s/DnsResolve", d.serviceName),
		fmt.Sprintf("/%s/Proxy", d.serviceName)
}

// RegisterWithServer registers the service with a gRPC server using the dynamic name
func (d *DynamicProxyService) RegisterWithServer(s *grpc.Server) {
	// Create a custom service descriptor with the dynamic name
	customDesc := grpc.ServiceDesc{
		ServiceName: d.serviceName,
		HandlerType: (*ProxyServer)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "DnsResolve",
				Handler:    _Proxy_DnsResolve_Handler,
			},
		},
		Streams: []grpc.StreamDesc{
			{
				StreamName:    "Proxy",
				Handler:       _Proxy_Proxy_Handler,
				ServerStreams: true,
				ClientStreams: true,
			},
		},
		Metadata: "proxy.proto",
	}

	s.RegisterService(&customDesc, d.server)
}

// ClientMethodNames provides method names for client calls
type ClientMethodNames struct {
	DnsResolve string
	Proxy      string
}

// GetClientMethodNames returns method names for a given service name
func GetClientMethodNames(serviceName string) ClientMethodNames {
	if serviceName == "" {
		serviceName = "proxy.Proxy"
	}
	return ClientMethodNames{
		DnsResolve: fmt.Sprintf("/%s/DnsResolve", serviceName),
		Proxy:      fmt.Sprintf("/%s/Proxy", serviceName),
	}
}
