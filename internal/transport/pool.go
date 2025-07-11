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
	//nolint:staticcheck
	BufferPool.Put(buf)
}
