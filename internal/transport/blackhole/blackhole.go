package blackhole

import (
	"context"
	"fmt"
	"io"
	"net"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

const TransportName = "blackHole"

// BlackHole is transport that discards all data.
type BlackHole struct{}

func New() transport.Transport {
	return &BlackHole{}
}

func (h *BlackHole) String() string {
	return TransportName
}

func (h *BlackHole) Close() error {
	return nil
}

func (h *BlackHole) Dial(_, _ string) (net.Conn, error) {
	return nil, fmt.Errorf("%s: %w", h, transport.ErrNotImplemented)
}

func (h *BlackHole) Proxy(ctx context.Context, _ string, localAddr chan<- string, _ io.Writer, src io.Reader) error {
	defer close(localAddr)
	localAddr <- "127.0.0.1:0"

	// Do NOT use a pooled buffer here. On context cancellation we return
	// immediately while the drain goroutine may still be executing src.Read.
	// A heap-allocated buffer owned exclusively by the goroutine is safe to
	// abandon without a data race; the pool would hand it to another caller.
	buf := make([]byte, transport.GetBufferSize())

	type readResult struct {
		n   int
		err error
	}
	// Buffered so the goroutine can always send even after we return.
	resultCh := make(chan readResult, 1)

	// Single long-running drain goroutine: reads until EOF or error.
	// Spawning a new goroutine per Read iteration (the old pattern) leaks one
	// goroutine per context cancellation and races on the shared buffer.
	go func() {
		for {
			n, err := src.Read(buf)
			if n > 0 {
				transport.GlobalStats.AddRx(uint64(n))
			}
			if err != nil {
				resultCh <- readResult{n, err}
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-resultCh:
		if result.err == io.EOF {
			return nil
		}
		return result.err
	}
}
