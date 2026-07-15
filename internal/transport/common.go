package transport

import (
	"sync/atomic"
	"time"
)

var (
	bufferSize atomic.Int64
	network    atomic.Value

	// preferIPv4 is set by DisableIPv6; when true DialNetwork maps dual-stack
	// networks (tcp/udp) and IPv6-only ones (tcp6/udp6) onto their IPv4 forms.
	preferIPv4 atomic.Bool

	// idleTimeout for transport of direct (use GetIdleTimeout/SetIdleTimeout for safe access).
	// Stored as nanoseconds in atomic.Int64 for lock-free reads, matching dialTimeout.
	idleTimeout atomic.Int64

	// dialTimeout for transport of direct (accessed via GetDialTimeout/SetDialTimeout)
	dialTimeout atomic.Int64
)

func init() {
	// BufferSize default: 32K (1K == 1024 Byte)
	bufferSize.Store(int64(32 * 1024))
	network.Store("tcp")
	dialTimeout.Store(int64(3 * time.Minute))
	idleTimeout.Store(int64(30 * time.Minute))
}

// GetBufferSize returns the current buffer size.
func GetBufferSize() int {
	return int(bufferSize.Load())
}

// GetNetwork returns the current network dial option.
func GetNetwork() string {
	return network.Load().(string)
}

// PreferIPv4 reports whether IPv6 has been disabled for dialing.
func PreferIPv4() bool {
	return preferIPv4.Load()
}

// DialNetwork returns a network string suitable for net.Dial / ListenPacket,
// forcing the IPv4-only variant when DisableIPv6 has been applied.
func DialNetwork(network string) string {
	if !preferIPv4.Load() {
		return network
	}
	switch network {
	case "tcp", "tcp6":
		return "tcp4"
	case "udp", "udp6":
		return "udp4"
	default:
		return network
	}
}

// GetIdleTimeout returns the current idle timeout.
func GetIdleTimeout() time.Duration {
	return time.Duration(idleTimeout.Load())
}

// GetDialTimeout returns the current dial timeout.
func GetDialTimeout() time.Duration {
	return time.Duration(dialTimeout.Load())
}
