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

var Transport = Direct{}

type Direct struct{}

func (d Direct) String() string {
	return TransportName
}

func (d Direct) Dial(network, addr string) (net.Conn, error) {
	return net.DialTimeout(network, addr, 3*time.Minute)
}

// Proxy the traffic locally
func (d Direct) Proxy(ctx context.Context, localAddr chan<- string, dst io.Writer, src io.Reader) error {
	req, ok := ctx.Value("request").(*transport.Request)
	if !ok {
		return transport.ErrorRequestNotFound
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	target := net.JoinHostPort(req.Host, strconv.Itoa(int(req.Port)))
	conn, err := net.DialTimeout(transport.Network, target, 3*time.Minute)
	if err != nil {
		close(localAddr)
		return err
	}
	localAddr <- conn.LocalAddr().String()
	defer utils.ForceClose(conn)
	// src -> dst
	go func() {
		if _, err := utils.CopyBuffer(conn, src, nil); err != nil {
			err = fmt.Errorf("src -> dst error: %w", err)
			utils.PrintErrorIfCritical(err, "direct")
			cancel()
		}
	}()
	// src <- dst
	go func() {
		if _, err := utils.CopyBuffer(dst, conn, nil); err != nil {
			err = fmt.Errorf("src <- dst error: %w", err)
			utils.PrintErrorIfCritical(err, "direct")
			cancel()
		}
	}()
	<-ctx.Done()
	return nil
}

func (d Direct) Close() error {
	return nil
}
