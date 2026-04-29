package transport

import "time"

// SetBufferSize in KB
func SetBufferSize(size uint16) {
	bufferSize.Store(int64(size) * 1024)
}

// SetNetwork to tcp4 or tcp6
func SetNetwork(net string) {
	network.Store(net)
}

// DisableIPv6 for dial
func DisableIPv6() {
	SetNetwork("tcp4")
}

// SetDialTimeout for transport of direct
func SetDialTimeout(timeout time.Duration) {
	dialTimeout.Store(int64(timeout))
}

// SetIdleTimeout for transport of direct
func SetIdleTimeout(timeout time.Duration) {
	idleTimeoutMu.Lock()
	idleTimeout = timeout
	idleTimeoutMu.Unlock()
}
