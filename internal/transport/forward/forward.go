package forward

import (
	"context"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"golang.org/x/net/proxy"
	"io"
	"net"
)

const TransportName = "forward"

var Transport = &Forward{}

type Forward struct {
	dialer proxy.Dialer
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
	return nil, fmt.Errorf("%s: dialer not attached", f)
}

func (f *Forward) Proxy(_ context.Context, _ transport.Request, localAddr chan<- string, _ io.Writer, _ io.Reader) error {
	close(localAddr)
	return fmt.Errorf("%s: %w", f, transport.ErrNotImplemented)
}
