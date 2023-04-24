package transport

import (
	"context"
	"io"
	"net"
)

type Transport interface {
	String() string
	Proxy(ctx context.Context, localAddr chan<- string, dst io.Writer, src io.Reader) error
	Dial(network, addr string) (net.Conn, error)
	Close() error
}
