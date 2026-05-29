package transport

import (
	"sync/atomic"
	"time"
)

var (
	bufferSize atomic.Int64
	network    atomic.Value

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

// GetIdleTimeout returns the current idle timeout.
func GetIdleTimeout() time.Duration {
	return time.Duration(idleTimeout.Load())
}

// GetDialTimeout returns the current dial timeout.
func GetDialTimeout() time.Duration {
	return time.Duration(dialTimeout.Load())
}
