package transport

import (
	"context"
	"io"
)

type Transport interface {
	Proxy(ctx context.Context, localAddr chan<- string, dst io.Writer, src io.Reader)
}
