package blackhole

import (
	"context"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"io"
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

func (h BlackHole) Proxy(ctx context.Context, localAddr chan<- string, dst io.Writer, src io.Reader) error {
	localAddr <- "127.0.0.1:0"
	buffer := make([]byte, transport.BufferSize)
	for {
		_, err := src.Read(buffer)
		if err != nil {
			return err
		}
	}
}
