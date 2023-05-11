package utils

import (
	"errors"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"io"
)

var errInvalidWrite = errors.New("invalid write result")

// CopyBuffer is the actual implementation of Copy and CopyBuffer.
// if buf is nil, one is allocated.
// Note that this is a copy function of io from the built-in,
// the only changes is let preset buffer size respect the transport settings.
func CopyBuffer(dst io.Writer, src io.Reader, buf []byte) (written int64, err error) {
	// If the reader has a WriteTo method, use it to do the copy.
	// Avoids an allocation and a copy.
	if wt, ok := src.(io.WriterTo); ok {
		return wt.WriteTo(dst)
	}
	// Similarly, if the writer has a ReadFrom method, use it to do the copy.
	if rt, ok := dst.(io.ReaderFrom); ok {
		return rt.ReadFrom(src)
	}
	if buf == nil {
		size := transport.BufferSize
		if l, ok := src.(*io.LimitedReader); ok && int64(size) > l.N {
			if l.N < 1 {
				size = 1
			} else {
				size = int(l.N)
			}
		}
		buf = make([]byte, size)
	}
	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					ew = errInvalidWrite
				}
			}
			written += int64(nw)
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return written, err
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
