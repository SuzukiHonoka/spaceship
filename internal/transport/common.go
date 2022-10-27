package transport

// BufferSize 32K (1K == 1024 Byte)
var BufferSize = 32 * 1024

// Network is a tcp dial option
var Network = "tcp"

func SetBufferSize(size uint16) {
	BufferSize = int(size) * 1024
}

func SetNetwork(network string) {
	Network = network
}

func DisableIPv6() {
	SetNetwork("tcp4")
}
