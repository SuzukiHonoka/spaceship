package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"golang.org/x/net/proxy"
	"golang.org/x/sync/errgroup"
)

const TransportName = "forward"

var dialer proxy.Dialer

func Attach(d proxy.Dialer) {
	dialer = d
}

// Forward is transport that connects through an upstream proxy.
// Each call to Proxy is fully self-contained — no mutable state is stored.
type Forward struct {
	dialer proxy.Dialer
}

func New() transport.Transport {
	return &Forward{dialer: dialer}
}

func (f *Forward) Attach(dialer proxy.Dialer) {
	f.dialer = dialer
}

func (f *Forward) String() string {
	return TransportName
}

func (f *Forward) Close() error {
	return nil
}

func (f *Forward) Dial(network, addr string) (net.Conn, error) {
	if f.dialer != nil {
		return f.dialer.Dial(network, addr)
	}
	return nil, errors.New("forward: dialer not attached")
}

func copyBuffer(ctx context.Context, conn net.Conn, dst io.Writer, src io.Reader, direction transport.Direction) error {
	buf := transport.Buffer()
	defer transport.PutBuffer(buf)

	// Use a buffered channel so the goroutine never blocks on send,
	// preventing a goroutine leak when we return early on ctx cancellation.
	type copyResult struct {
		n   int64
		err error
	}
	resultCh := make(chan copyResult, 1)
	go func() {
		n, err := io.CopyBuffer(dst, src, *buf)
		resultCh <- copyResult{n, err}
	}()

	select {
	case result := <-resultCh:
		transport.GlobalStats.Add(direction, result.n)
		return result.err
	case <-ctx.Done():
		_ = conn.Close()
		// Wait for the goroutine to exit before reading n to avoid a data race.
		result := <-resultCh
		transport.GlobalStats.Add(direction, result.n)
		return ctx.Err()
	}
}

func (f *Forward) Proxy(ctx context.Context, addr string, localAddr chan<- string, dst io.Writer, src io.Reader) (err error) {
	defer close(localAddr)

	conn, err := f.Dial(transport.GetNetwork(), addr)
	if err != nil {
		return err
	}
	localAddr <- conn.LocalAddr().String()
	defer utils.Close(conn)

	errGroup, ctx := errgroup.WithContext(ctx)
	// src -> dst
	errGroup.Go(func() error {
		return copyBuffer(ctx, conn, conn, src, transport.DirectionOut)
	})
	// src <- dst
	errGroup.Go(func() error {
		return copyBuffer(ctx, conn, dst, conn, transport.DirectionIn)
	})

	if err = errGroup.Wait(); err != nil && err != io.EOF {
		return fmt.Errorf("forward: %w", err)
	}
	return nil
}
