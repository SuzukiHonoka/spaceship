package transport

import (
	"sync"
	"sync/atomic"
	"time"
)

var (
	bufferSize atomic.Int64
	network    atomic.Value

	// IdleTimeout for transport of direct
	IdleTimeout   = 30 * time.Minute
	idleTimeoutMu sync.RWMutex

	// dialTimeout for transport of direct (accessed via GetDialTimeout/SetDialTimeout)
	dialTimeout atomic.Int64
)

func init() {
	// BufferSize default: 32K (1K == 1024 Byte)
	bufferSize.Store(int64(32 * 1024))
	network.Store("tcp")
	dialTimeout.Store(int64(3 * time.Minute))
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
	idleTimeoutMu.RLock()
	defer idleTimeoutMu.RUnlock()
	return IdleTimeout
}

// GetDialTimeout returns the current dial timeout.
func GetDialTimeout() time.Duration {
	return time.Duration(dialTimeout.Load())
}
