package rpc

import (
	"fmt"
	"sync"
	"sync/atomic"
)

const defaultServiceName = "proxy.Proxy"

type serviceSnapshot struct {
	epoch       uint64
	serviceName string
	dnsResolve  string
	dnsExchange string
	proxy       string
	proxyPacket string
}

func newServiceSnapshot(name string, epoch uint64) *serviceSnapshot {
	if name == "" {
		name = defaultServiceName
	}
	return &serviceSnapshot{
		epoch:       epoch,
		serviceName: name,
		dnsResolve:  "/" + name + "/DnsResolve",
		dnsExchange: "/" + name + "/DnsExchange",
		proxy:       "/" + name + "/Proxy",
		proxyPacket: "/" + name + "/ProxyPacket",
	}
}

var (
	serviceWriteMu sync.Mutex
	serviceCfg     atomic.Pointer[serviceSnapshot]
)

func init() {
	serviceCfg.Store(newServiceSnapshot(defaultServiceName, 1))
}

// SetServiceName configures the gRPC service name dynamically.
// This should be called before creating clients or servers.
func SetServiceName(name string) {
	serviceWriteMu.Lock()
	defer serviceWriteMu.Unlock()

	prev := serviceCfg.Load()
	nextEpoch := uint64(1)
	if prev != nil {
		nextEpoch = prev.epoch + 1
	}
	serviceCfg.Store(newServiceSnapshot(name, nextEpoch))
}

// ServiceEpoch returns a monotonic counter incremented on each SetServiceName call.
func ServiceEpoch() uint64 {
	return serviceCfg.Load().epoch
}

// ServiceMethods returns the current epoch and all standard gRPC full method
// names from a single atomic snapshot.
func ServiceMethods() (epoch uint64, dnsResolve, dnsExchange, proxy string) {
	snap := serviceCfg.Load()
	return snap.epoch, snap.dnsResolve, snap.dnsExchange, snap.proxy
}

// GetServiceName returns the current service name.
func GetServiceName() string {
	return serviceCfg.Load().serviceName
}

// GetMethodName returns the full method name for a given method.
func GetMethodName(method string) string {
	snap := serviceCfg.Load()
	switch method {
	case "DnsResolve":
		return snap.dnsResolve
	case "DnsExchange":
		return snap.dnsExchange
	case "Proxy":
		return snap.proxy
	case "ProxyPacket":
		return snap.proxyPacket
	default:
		return fmt.Sprintf("/%s/%s", snap.serviceName, method)
	}
}

// GetDnsResolveMethodName returns the full method name for DnsResolve.
func GetDnsResolveMethodName() string {
	return serviceCfg.Load().dnsResolve
}

// GetDnsExchangeMethodName returns the full method name for DnsExchange.
func GetDnsExchangeMethodName() string {
	return serviceCfg.Load().dnsExchange
}

// GetProxyMethodName returns the full method name for Proxy.
func GetProxyMethodName() string {
	return serviceCfg.Load().proxy
}

// GetProxyPacketMethodName returns the full method name for ProxyPacket.
func GetProxyPacketMethodName() string {
	return serviceCfg.Load().proxyPacket
}
