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

	// DialTimeout for transport of direct
	DialTimeout = 3 * time.Minute
)

func init() {
	// BufferSize default: 32K (1K == 1024 Byte)
	bufferSize.Store(int64(32 * 1024))
	network.Store("tcp")
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
