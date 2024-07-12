package blackhole

import (
	"context"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"io"
	"net"
)

const TransportName = "blackHole"

var Transport = BlackHole{}

type BlackHole struct{}

func (h BlackHole) String() string {
	return TransportName
}

func (h BlackHole) Close() error {
	return nil
}

func (h BlackHole) Dial(_, _ string) (c net.Conn, err error) {
	return nil, fmt.Errorf("%s: %w", h, transport.ErrNotImplemented)
}

func (h BlackHole) Proxy(_ context.Context, _ *transport.Request, localAddr chan<- string, _ io.Writer, src io.Reader) error {
	localAddr <- "127.0.0.1:0"
	_, err := io.Copy(io.Discard, src)
	return err
}
