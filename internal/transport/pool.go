package transport

import "sync"

var bufferPool = sync.Pool{
	New: func() any {
		b := new([]byte)
		*b = make([]byte, GetBufferSize())
		return b
	},
}

func Buffer() *[]byte {
	want := GetBufferSize()
	buf := bufferPool.Get().(*[]byte)
	if cap(*buf) != want {
		b := make([]byte, want)
		return &b
	}
	*buf = (*buf)[:want]
	return buf
}

// PutBuffer returns a buffer to the pool. Buffers whose capacity no longer
// matches the current GetBufferSize() are silently discarded, allowing the
// pool to gradually migrate to a new size after SetBufferSize() is called.
func PutBuffer(buf *[]byte) {
	if buf == nil || cap(*buf) != GetBufferSize() {
		return
	}
	bufferPool.Put(buf)
}
