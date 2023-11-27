package direct

import (
	"context"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"io"
	"net"
	"strconv"
	"time"
)

const TransportName = "direct"

const dialTimeout = 3 * time.Minute

var Transport = Direct{}

type Direct struct{}

func (d Direct) String() string {
	return TransportName
}

func (d Direct) Dial(network, addr string) (net.Conn, error) {
	return net.DialTimeout(network, addr, dialTimeout)
}

// Proxy the traffic locally
func (d Direct) Proxy(ctx context.Context, req transport.Request, localAddr chan<- string, dst io.Writer, src io.Reader) error {
	defer close(localAddr)
	target := net.JoinHostPort(req.Host, strconv.Itoa(int(req.Port)))
	conn, err := net.DialTimeout(transport.Network, target, dialTimeout)
	if err != nil {
		return err
	}
	defer utils.ForceClose(conn)
	localAddr <- conn.LocalAddr().String()
	proxyError := make(chan error)
	// src -> dst
	go func() {
		_, err := utils.CopyBuffer(conn, src, nil)
		proxyError <- err
	}()
	// src <- dst
	go func() {
		_, err := utils.CopyBuffer(dst, conn, nil)
		proxyError <- err
	}()
	if err := <-proxyError; err != nil {
		return fmt.Errorf("direct: %w", err)
	}
	return nil
}

func (d Direct) Close() error {
	return nil
}
