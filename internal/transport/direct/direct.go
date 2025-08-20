package direct

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"golang.org/x/sync/errgroup"
)

const TransportName = "direct"

// Direct is transport that connects directly to the destination
type Direct struct {
	conn      net.Conn
	closeOnce sync.Once
}

func New() transport.Transport {
	return &Direct{}
}

func (d *Direct) String() string {
	return TransportName
}

func (d *Direct) Dial(network, addr string) (net.Conn, error) {
	return net.DialTimeout(network, addr, transport.DialTimeout)
}

func (d *Direct) copyBuffer(ctx context.Context, dst io.Writer, src io.Reader, direction transport.Direction) error {
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
				transport.GlobalStats.AddRx(uint64(n))
			case transport.DirectionOut:
				transport.GlobalStats.AddTx(uint64(n))
			}
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Proxy the traffic locally
func (d *Direct) Proxy(ctx context.Context, req *transport.Request, localAddr chan<- string, dst io.Writer, src io.Reader) (err error) {
	defer close(localAddr)
	target := net.JoinHostPort(req.Host, strconv.Itoa(int(req.Port)))
	d.conn, err = d.Dial(transport.Network, target)
	if err != nil {
		return fmt.Errorf("direct: failed to dial: %w", err)
	}
	localAddr <- d.conn.LocalAddr().String()
	defer utils.Close(d)

	errGroup, ctx := errgroup.WithContext(ctx)
	// src -> dst
	errGroup.Go(func() error {
		return d.copyBuffer(ctx, d.conn, src, transport.DirectionOut)
	})
	// src <- dst
	errGroup.Go(func() error {
		return d.copyBuffer(ctx, dst, d.conn, transport.DirectionIn)
	})

	if err = errGroup.Wait(); err != nil && err != io.EOF {
		return fmt.Errorf("direct: %w", err)
	}
	return nil
}

func (d *Direct) Close() (err error) {
	d.closeOnce.Do(func() {
		if d.conn != nil {
			err = d.conn.Close()
		}
	})
	return err
}
