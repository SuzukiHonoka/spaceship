package transport

func SetBufferSize(size uint16) {
	BufferSize = int(size) * 1024
}

func SetNetwork(network string) {
	Network = network
}

func DisableIPv6() {
	SetNetwork("tcp4")
}
