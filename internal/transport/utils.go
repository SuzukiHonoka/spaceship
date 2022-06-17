package transport

import (
	"errors"
	"fmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"log"
	"net"
)

// PrintErrorIfNotCritical prints only error that critical
func PrintErrorIfNotCritical(err error, msg string) {
	if err == nil {
		return
	}
	switch {
	// native errors
	case errors.Is(err, io.EOF):
		return
	case errors.Is(err, net.ErrClosed):
		return
	case errors.Is(err, io.ErrClosedPipe):
		return
	default:
		// grpc errors
		if s, ok := status.FromError(err); ok {
			switch s.Code() {
			case codes.Canceled:
				return
			case codes.Unavailable:
				return
			}
		}
	}
	log.Printf("%s: %v", msg, err)
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
