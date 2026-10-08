//go:build !linux

package transport

import "io"

// spliceStreams reports that this platform has no kernel socket-to-socket
// copy, so copyStream runs its buffered loop.
func spliceStreams(io.Writer, io.Reader) (int64, error, bool) {
	return 0, nil, false
}
