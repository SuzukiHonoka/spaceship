package client

import (
	"context"
	"fmt"
	"os"
	"time"

	proxy "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
)

// sendHandshake sends the opening header of a proxy stream under a deadline.
//
// grpc's Send carries no deadline of its own: on a wedged connection, or one
// blocked on HTTP/2 flow control, it can block indefinitely and stall whichever
// caller is driving it. For the UDP relay that caller is a worker from a bounded
// pool, so a single stuck stream would degrade every other flow sharing it.
//
// On timeout the stream context is canceled, which aborts the stream and
// unblocks the send; the channel is buffered so that goroutine can always
// complete and exit rather than leaking.
func sendHandshake(
	stream proxy.Proxy_ProxyClient,
	req *proxy.ProxySRC,
	cancel context.CancelFunc,
	timeout time.Duration,
	addr string,
) error {
	sendErr := make(chan error, 1)
	go func() { sendErr <- stream.Send(req) }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-sendErr:
		return err
	case <-timer.C:
		cancel()
		return fmt.Errorf("handshake to %s timed out after %s: %w", addr, timeout, os.ErrDeadlineExceeded)
	}
}
