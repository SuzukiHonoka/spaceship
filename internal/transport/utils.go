package transport

import (
	"fmt"
	"net"
)

// GetTargetDst return dst addr and assume parameter won't be nil
func GetTargetDst(fqdn string, port uint16) string {
	var target string
	ip := net.ParseIP(fqdn)
	switch {
	case ip == nil:
		fallthrough
	case ip.To4() != nil:
		target = fmt.Sprintf("%s:%d", fqdn, port)
	case ip.To16() != nil:
		target = fmt.Sprintf("[%s]:%d", fqdn, port)
	}
	return target
}
