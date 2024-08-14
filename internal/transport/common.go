package transport

import "time"

// BufferSize 64K (1K == 1024 Byte)
var BufferSize = 64 * 1024

// Network is a tcp dial option
var Network = "tcp"

// DialTimeout for transport of direct
var DialTimeout = 3 * time.Minute

func AllocateBuffer() []byte {
	return make([]byte, BufferSize)
}
