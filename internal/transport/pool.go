package transport

import "sync"

var BufferPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, BufferSize)
	},
}

func Buffer() []byte {
	return BufferPool.Get().([]byte)
}

func PutBuffer(buf []byte) {
	BufferPool.Put(buf)
}
