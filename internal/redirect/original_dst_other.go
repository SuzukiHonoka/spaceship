//go:build !linux

package redirect

import "net"

// Supported reports whether this binary can serve Linux netfilter REDIRECT
// traffic.
func Supported() bool {
	return false
}

func originalDestination(net.Conn) (*net.TCPAddr, error) {
	return nil, ErrUnsupported
}
