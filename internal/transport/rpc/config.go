package rpc

import (
	"fmt"
	"sync"
)

// ServiceConfig holds dynamic service configuration
type ServiceConfig struct {
	mu          sync.RWMutex
	serviceName string
	methodNames map[string]string
}

var (
	// Global service configuration
	globalServiceConfig = &ServiceConfig{
		serviceName: "proxy.Proxy", // Default service name
		methodNames: make(map[string]string),
	}
)

// SetServiceName configures the gRPC service name dynamically
// This should be called before creating clients or servers
func SetServiceName(name string) {
	globalServiceConfig.mu.Lock()
	defer globalServiceConfig.mu.Unlock()

	if name == "" {
		name = "proxy.Proxy" // fallback to default
	}

	globalServiceConfig.serviceName = name

	// Update method names
	globalServiceConfig.methodNames = map[string]string{
		"DnsResolve": fmt.Sprintf("/%s/DnsResolve", name),
		"Proxy":      fmt.Sprintf("/%s/Proxy", name),
	}
}

// GetServiceName returns the current service name
func GetServiceName() string {
	globalServiceConfig.mu.RLock()
	defer globalServiceConfig.mu.RUnlock()
	return globalServiceConfig.serviceName
}

// GetMethodName returns the full method name for a given method
func GetMethodName(method string) string {
	globalServiceConfig.mu.RLock()
	defer globalServiceConfig.mu.RUnlock()

	if fullName, exists := globalServiceConfig.methodNames[method]; exists {
		return fullName
	}

	// Fallback to default naming
	return fmt.Sprintf("/proxy.Proxy/%s", method)
}

// GetDnsResolveMethodName returns the full method name for DnsResolve
func GetDnsResolveMethodName() string {
	return GetMethodName("DnsResolve")
}

// GetProxyMethodName returns the full method name for Proxy
func GetProxyMethodName() string {
	return GetMethodName("Proxy")
}

// Initialize with default values
func init() {
	SetServiceName("proxy.Proxy")
}
