package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

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

func (f *Forward) Proxy(ctx context.Context, addr string, localAddr chan<- string, dst io.Writer, src io.Reader) (err error) {
	defer close(localAddr)

	conn, err := f.Dial(transport.GetNetwork(), addr)
	if err != nil {
		return err
	}
	localAddr <- conn.LocalAddr().String()
	defer utils.Close(conn)

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var closeOnce sync.Once
	closeSession := func() {
		closeOnce.Do(func() {
			transport.CloseAll(src, dst, conn)
		})
	}

	var responseDone atomic.Bool
	var errGroup errgroup.Group
	errGroup.Go(func() error {
		err := transport.CopyWithContext(sessionCtx, closeSession, conn, src, transport.DirectionOut)
		if err != nil && !errors.Is(err, io.EOF) {
			cancel()
			closeSession()
			return err
		}
		transport.CloseWriteOrClose(conn)
		return err
	})

	errGroup.Go(func() error {
		err := transport.CopyWithContext(sessionCtx, closeSession, dst, conn, transport.DirectionIn)
		if err == nil || errors.Is(err, io.EOF) {
			responseDone.Store(true)
		}
		cancel()
		closeSession()
		return err
	})

	if err = errGroup.Wait(); err != nil && !errors.Is(err, io.EOF) {
		if responseDone.Load() {
			return nil
		}
		return fmt.Errorf("forward: %w", err)
	}
	return nil
}
