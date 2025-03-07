package blackhole

import (
	"context"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
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

	buf := transport.AllocateBuffer()

	var err error
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			if _, err = src.Read(buf); err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
		}
	}
}
