package utils

import (
	"errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"log"
	"net"
)

// PrintErrorIfCritical prints only error that critical
func PrintErrorIfCritical(err error, msg string) {
	switch {
	// native errors
	case errors.Is(err, io.EOF):
		return
	case errors.Is(err, io.ErrClosedPipe):
		return
	case errors.Is(err, net.ErrClosed):
		return
	default:
		// grpc errors
		if s, ok := status.FromError(errors.Unwrap(err)); ok {
			switch s.Code() {
			case codes.Unavailable, codes.Canceled:
				return
			}
		}
	}
	log.Printf("%s: %v", msg, err)
}
