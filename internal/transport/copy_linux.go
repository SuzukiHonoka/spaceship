//go:build linux

package transport

import (
	"io"
	"net"
)

// spliceStreams moves src to dst inside the kernel with splice(2) when both
// are stream sockets, following pass-through wrappers to find them. The data
// never enters user space, so a relay between two sockets costs no copy and no
// transport buffer. It reports false, having moved nothing, when the pair is
// not one the net package can splice.
//
// The net package exposes splice through TCPConn.ReadFrom, whose source may be
// a TCP or stream Unix connection, and TCPConn.WriteTo, whose destination may
// be a stream Unix connection. Those cover every pairing a front end produces:
// a TCP or Unix-socket client on one side and a TCP egress on the other.
func spliceStreams(dst io.Writer, src io.Reader) (int64, error, bool) {
	w, ok := kernelConn(dst)
	if !ok {
		return 0, nil, false
	}
	r, ok := kernelConn(src)
	if !ok {
		return 0, nil, false
	}
	switch w := w.(type) {
	case *net.TCPConn:
		n, err := w.ReadFrom(r)
		return n, err, true
	case *net.UnixConn:
		if r, ok := r.(*net.TCPConn); ok {
			n, err := r.WriteTo(w)
			return n, err, true
		}
	}
	return 0, nil, false
}
