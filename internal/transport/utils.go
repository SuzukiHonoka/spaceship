package transport

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
)

func PrintErrorIfNotEOF(err error, msg string) {
	if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		log.Printf("%s: %v", msg, err)
	}
}

// GetTargetDst return dst addr and assume parameter won't be nil
func GetTargetDst(fqdn string, port int) string {
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
