package transport

// SetBufferSize in KB
func SetBufferSize(size uint16) {
	BufferSize = int(size) * 1024
}

// SetNetwork to tcp4 or tcp6
func SetNetwork(network string) {
	Network = network
}

// DisableIPv6 for dial
func DisableIPv6() {
	SetNetwork("tcp4")
}
