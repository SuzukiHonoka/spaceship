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

var Transport = Direct{}

type Direct struct{}

func (d Direct) String() string {
	return TransportName
}

func (d Direct) Dial(network, addr string) (net.Conn, error) {
	return net.DialTimeout(network, addr, transport.DialTimeout)
}

// Proxy the traffic locally
func (d Direct) Proxy(_ context.Context, req *transport.Request, localAddr chan<- string, dst io.Writer, src io.Reader) error {
	defer close(localAddr)

	target := net.JoinHostPort(req.Host, strconv.Itoa(int(req.Port)))
	conn, err := net.DialTimeout(transport.Network, target, transport.DialTimeout)
	if err != nil {
		return err
	}
	defer utils.Close(conn)
	localAddr <- conn.LocalAddr().String()

	proxyErrCh := make(chan error, 2)

	// src -> dst
	go func() {
		_, err1 := io.CopyBuffer(conn, src, transport.AllocateBuffer())
		proxyErrCh <- err1
	}()

	// src <- dst
	go func() {
		_, err2 := io.CopyBuffer(dst, conn, transport.AllocateBuffer())
		proxyErrCh <- err2
	}()

	if err = <-proxyErrCh; err != nil {
		err = fmt.Errorf("direct: %w", err)
	}

	return err
}

func (d Direct) Close() error {
	return nil
}
