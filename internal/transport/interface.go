package transport

import (
	"context"
	"io"
)

type Transport interface {
	String() string
	Proxy(ctx context.Context, localAddr chan<- string, dst io.Writer, src io.Reader) error
	Close() error
}
