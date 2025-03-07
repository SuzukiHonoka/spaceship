package forward

import (
	"context"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"golang.org/x/net/proxy"
	"golang.org/x/sync/errgroup"
	"io"
	"net"
	"strconv"
	"sync"
)

const TransportName = "forward"

var dialer proxy.Dialer

func Attach(d proxy.Dialer) {
	dialer = d
}

type Forward struct {
	dialer proxy.Dialer
	conn   net.Conn
	once   sync.Once
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
	f.once.Do(func() {
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
	return nil, fmt.Errorf("%s: dialer not attached", f)
}

func (f *Forward) Proxy(ctx context.Context, req *transport.Request, localAddr chan<- string, dst io.Writer, src io.Reader) (err error) {
	defer close(localAddr)

	target := net.JoinHostPort(req.Host, strconv.Itoa(int(req.Port)))
	f.conn, err = f.Dial(transport.Network, target)
	if err != nil {
		return err
	}
	localAddr <- f.conn.LocalAddr().String()
	defer utils.Close(f)

	errGroup, ctx := errgroup.WithContext(ctx)
	// src -> dst
	errGroup.Go(func() error {
		_, err := io.CopyBuffer(f.conn, src, transport.AllocateBuffer())
		if err != nil && err != io.EOF {
			return fmt.Errorf("forward: %w", err)
		}
		return nil
	})
	// src <- dst
	errGroup.Go(func() error {
		_, err := io.CopyBuffer(dst, f.conn, transport.AllocateBuffer())
		if err != nil && err != io.EOF {
			return fmt.Errorf("forward: %w", err)
		}
		return nil
	})

	return errGroup.Wait()
}
