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

	proxyErr := make(chan error)
	defer close(proxyErr)

	// src -> dst
	go func() {
		_, err1 := io.CopyBuffer(conn, src, make([]byte, transport.BufferSize))
		proxyErr <- err1
	}()

	// src <- dst
	go func() {
		_, err2 := io.CopyBuffer(dst, conn, make([]byte, transport.BufferSize))
		proxyErr <- err2
	}()

	for i := 0; i < 2; i++ {
		err = <-proxyErr
	}

	return fmt.Errorf("direct: %w", err)
}

func (d Direct) Close() error {
	return nil
}
