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

func (f *Forward) copyBuffer(ctx context.Context, dst io.Writer, src io.Reader, direction transport.Direction) error {
	buf := transport.Buffer()
	defer transport.PutBuffer(buf)

	var n int64
	var err error

	copyDone := make(chan struct{})
	go func() {
		n, err = io.CopyBuffer(dst, src, buf)
		close(copyDone)
	}()

	select {
	case <-copyDone:
		if n > 0 {
			switch direction {
			case transport.DirectionIn:
				transport.GlobalStats.AddRx(uint64(n)) // #nosec G115
			case transport.DirectionOut:
				transport.GlobalStats.AddTx(uint64(n)) // #nosec G115
			}
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *Forward) Proxy(ctx context.Context, addr string, localAddr chan<- string, dst io.Writer, src io.Reader) (err error) {
	defer close(localAddr)

	f.conn, err = f.Dial(transport.Network, addr)
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
