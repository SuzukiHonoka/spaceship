package direct

import (
	"context"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"golang.org/x/sync/errgroup"
	"io"
	"net"
	"strconv"
	"sync"
)

const TransportName = "direct"

// Direct is transport that connects directly to the destination
type Direct struct {
	conn net.Conn
	once sync.Once
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
		_, err := io.CopyBuffer(d.conn, src, transport.AllocateBuffer())
		if err != nil && err != io.EOF {
			return fmt.Errorf("direct: %w", err)
		}
		return nil
	})
	// src <- dst
	errGroup.Go(func() error {
		_, err := io.CopyBuffer(dst, d.conn, transport.AllocateBuffer())
		if err != nil && err != io.EOF {
			return fmt.Errorf("direct: %w", err)
		}
		return nil
	})

	return errGroup.Wait()
}

func (d *Direct) Close() (err error) {
	d.once.Do(func() {
		if d.conn != nil {
			err = d.conn.Close()
		}
	})
	return err
}
