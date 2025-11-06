package transport

import "sync"

var BufferPool = sync.Pool{
	New: func() interface{} {
		return AllocateBuffer()
	},
}

func AllocateBuffer() []byte {
	return make([]byte, BufferSize)
}

func Buffer() []byte {
	return BufferPool.Get().([]byte)
}

func PutBuffer(buf []byte) {
	// Reset the buffer to original capacity to prevent memory bloat
	if cap(buf) == BufferSize {
		buf = buf[:BufferSize]
		//nolint:staticcheck
		BufferPool.Put(buf)
	}
	// Don't put back buffers that have different capacity
}
