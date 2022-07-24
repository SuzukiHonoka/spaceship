package transport

// BufferSize 32K (1K == 1024 Byte)
var BufferSize = 32 * 1024

// Network is a tcp dial option
var Network = "tcp"

func SetBufferSize(size int) {
	BufferSize = size
}

func SetNetwork(network string) {
	Network = network
}
