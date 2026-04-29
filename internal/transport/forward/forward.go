package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

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

type Forward struct {
	dialer    proxy.Dialer
	conn      net.Conn
	closeOnce sync.Once
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

func (f *Forward) Close() (err error) {
	f.closeOnce.Do(func() {
		if f.conn != nil {
			err = f.conn.Close()
		}
	})
	return err
}

func (f *Forward) Dial(network, addr string) (net.Conn, error) {
	if f.dialer != nil {
		return f.dialer.Dial(network, addr)
	}
	return nil, errors.New("forward: dialer not attached")
}

type copyResult struct {
	n   int64
	err error
}

func (f *Forward) copyBuffer(ctx context.Context, dst io.Writer, src io.Reader, direction transport.Direction) error {
	buf := transport.Buffer()
	defer transport.PutBuffer(buf)

	// Use a buffered channel so the goroutine never blocks on send,
	// preventing a goroutine leak when we return early on ctx cancellation.
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
		_ = f.Close()
		// Wait for the goroutine to exit before reading n to avoid a data race.
		result := <-resultCh
		transport.GlobalStats.Add(direction, result.n)
		return ctx.Err()
	}
}

func (f *Forward) Proxy(ctx context.Context, addr string, localAddr chan<- string, dst io.Writer, src io.Reader) (err error) {
	defer close(localAddr)

	f.conn, err = f.Dial(transport.GetNetwork(), addr)
	if err != nil {
		return err
	}
	localAddr <- f.conn.LocalAddr().String()
	defer utils.Close(f)

	errGroup, ctx := errgroup.WithContext(ctx)
	// src -> dst
	errGroup.Go(func() error {
		return f.copyBuffer(ctx, f.conn, src, transport.DirectionOut)
	})
	// src <- dst
	errGroup.Go(func() error {
		return f.copyBuffer(ctx, dst, f.conn, transport.DirectionIn)
	})

	if err = errGroup.Wait(); err != nil && err != io.EOF {
		return fmt.Errorf("forward: %w", err)
	}
	return nil
}
