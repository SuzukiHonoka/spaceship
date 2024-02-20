package utils

import (
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"io"
)

// CopyBuffer is the actual implementation of Copy and CopyBuffer.
// if buf is nil, one is allocated.
// Note that this is a copy function of io from the built-in,
// the only changes is let preset buffer size respect the transport settings.
func CopyBuffer(dst io.Writer, src io.Reader, buf []byte) (written int64, err error) {
	if buf == nil {
		buf = make([]byte, transport.BufferSize)
	}
	return io.CopyBuffer(dst, src, buf)
}

// ForceClose forces close the closer
func ForceClose(closer io.Closer) {
	_ = closer.Close()
}

// ForceCloseAll force close all closers
func ForceCloseAll(closers ...io.Closer) {
	for _, closer := range closers {
		ForceClose(closer)
	}
}
