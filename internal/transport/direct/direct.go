package direct

import (
	"context"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"io"
	"net"
	"strconv"
)

const TransportName = "direct"

// Direct is transport that connects directly to the destination
type Direct struct {
	conn net.Conn
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
		return err
	}
	defer utils.Close(d)

	localAddr <- d.conn.LocalAddr().String()
	proxyErrCh := make(chan error, 2)

	// src -> dst
	go func() {
		_, err := io.CopyBuffer(d.conn, src, transport.AllocateBuffer())
		proxyErrCh <- err
	}()

	// src <- dst
	go func() {
		_, err := io.CopyBuffer(dst, d.conn, transport.AllocateBuffer())
		proxyErrCh <- err
	}()

	select {
	case <-ctx.Done():
		return err
	case err = <-proxyErrCh:
		if err != nil && err != io.EOF {
			return fmt.Errorf("direct: %w", err)
		}
	}

	return nil
}

func (d *Direct) Close() error {
	if d.conn != nil {
		return d.conn.Close()
	}
	return nil
}
