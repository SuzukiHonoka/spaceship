package transport

import "sync"

var bufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, BufferSize)
		return &buf
	},
}

func Buffer() *[]byte {
	return bufferPool.Get().(*[]byte)
}

func PutBuffer(buf *[]byte) {
	if cap(*buf) != BufferSize {
		return
	}
	bufferPool.Put(buf)
}
