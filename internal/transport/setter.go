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

// DisableIPv6 forces IPv4-only dialing for both TCP and UDP.
// TCP uses "tcp4"; callers that dial UDP should pass DialNetwork("udp") so
// the dual-stack "udp" network is rewritten to "udp4".
func DisableIPv6() {
	SetNetwork("tcp4")
	preferIPv4.Store(true)
}

// EnableIPv6 restores dual-stack dialing (primarily for tests).
func EnableIPv6() {
	SetNetwork("tcp")
	preferIPv4.Store(false)
}

// SetDialTimeout for transport of direct
func SetDialTimeout(timeout time.Duration) {
	dialTimeout.Store(int64(timeout))
}

// SetIdleTimeout for transport of direct
func SetIdleTimeout(timeout time.Duration) {
	idleTimeout.Store(int64(timeout))
}
