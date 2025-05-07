package blackhole

import (
	"context"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"io"
	"net"
)

const TransportName = "blackHole"

// BlackHole is transport that discards all data
type BlackHole struct {
	cancel func()
}

func New() transport.Transport {
	return &BlackHole{}
}

func (h *BlackHole) String() string {
	return TransportName
}

func (h *BlackHole) Close() error {
	if h.cancel != nil {
		h.cancel()
	}
	return nil
}

func (h *BlackHole) Dial(_, _ string) (c net.Conn, err error) {
	return nil, fmt.Errorf("%s: %w", h, transport.ErrNotImplemented)
}

func (h *BlackHole) Proxy(ctx context.Context, _ *transport.Request, localAddr chan<- string, _ io.Writer, src io.Reader) error {
	localAddr <- "127.0.0.1:0"

	ctx, h.cancel = context.WithCancel(ctx)
	defer utils.Close(h)

	buf := transport.Buffer()
	defer transport.PutBuffer(buf)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			n, err := src.Read(buf)
			if n > 0 {
				transport.GlobalStats.AddRx(uint64(n))
			}

			if err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
		}
	}
}
