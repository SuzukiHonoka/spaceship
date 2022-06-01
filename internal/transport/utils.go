package transport

import (
	"context"
	"errors"
	"fmt"

	"io"
	"log"
	"net"
)

// PrintErrorIfNotCritical prints only error that critical
func PrintErrorIfNotCritical(err error, msg string) {
	switch {
	case errors.Is(err, io.EOF):
	case errors.Is(err, net.ErrClosed):
	case errors.Is(err, context.Canceled):
	default:
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
