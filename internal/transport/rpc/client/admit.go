package client

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
)

// admittedProxy is a tunnel the server has authenticated and admitted.
// The target address is not sent until Proxy, so the front end can read the
// client flight after it has told that client the connection is open.
type admittedProxy struct {
	client   *Client
	ctx      context.Context
	cancel   context.CancelFunc
	stream   proto.Proxy_ProxyClient
	once     sync.Once
	closeErr error
}

// Admit opens a proxy stream and waits until the server has authenticated the
// user and admitted the session. The returned session owns c: Close releases
// the pooled connection, including when Admit itself fails.
func (c *Client) Admit(ctx context.Context) (transport.AdmittedSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	stream, err := c.ProxyClient.Proxy(sessionCtx)
	if err != nil {
		cancel()
		_ = c.Close()
		return nil, err
	}
	// Header blocks until the server sends admission headers. On refusal it
	// returns a nil map and swallows the status; Recv then returns that status
	// without waiting for a target address.
	md, err := stream.Header()
	if err != nil || md == nil {
		_, recvErr := stream.Recv()
		cancel()
		_ = c.Close()
		if err != nil {
			return nil, err
		}
		if recvErr != nil {
			return nil, recvErr
		}
		return nil, errors.New("proxy admission failed")
	}
	return &admittedProxy{
		client: c,
		ctx:    sessionCtx,
		cancel: cancel,
		stream: stream,
	}, nil
}

func (a *admittedProxy) Proxy(ctx context.Context, addr string, localAddr chan<- string, w io.Writer, r io.Reader) error {
	defer close(localAddr)
	stop := context.AfterFunc(ctx, a.cancel)
	defer stop()
	return a.client.proxyStream(a.ctx, a.cancel, a.stream, addr, localAddr, w, r)
}

func (a *admittedProxy) Close() error {
	a.once.Do(func() {
		a.cancel()
		a.closeErr = a.client.Close()
	})
	return a.closeErr
}
