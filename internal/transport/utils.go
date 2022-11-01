package transport

import (
	"errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"log"
	"net"
	"strconv"
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
			}
		}
	}
	log.Printf("%s: %v", msg, err)
}

// SplitHostPort uses net.SplitHostPort but converts port to uint16 format
func SplitHostPort(s string) (string, uint16, error) {
	host, sport, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(sport)
	if err != nil {
		return "", 0, err
	}
	return host, uint16(port), nil
}

// ForceClose forces close the closer
func ForceClose(closer io.Closer) {
	_ = closer.Close()
}
