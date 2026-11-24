package client

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	proxy "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
)

// sendHandshake sends the opening header of a proxy stream under a deadline.
//
// grpc's Send carries no deadline of its own: on a wedged connection, or one
// blocked on HTTP/2 flow control, it can block indefinitely and stall whichever
// caller is driving it.
//
// On timeout the stream context is canceled, which aborts the stream and
// unblocks Send in place without spawning an extra helper goroutine.
func sendHandshake(
	stream proxy.Proxy_ProxyClient,
	req *proxy.ProxySRC,
	cancel context.CancelFunc,
	timeout time.Duration,
	addr string,
) error {
	var timedOut atomic.Bool
	timerDone := make(chan struct{})
	timer := time.AfterFunc(timeout, func() {
		defer close(timerDone)
		timedOut.Store(true)
		cancel()
	})

	err := stream.Send(req)
	if !timer.Stop() {
		<-timerDone
	}
	if timedOut.Load() {
		return fmt.Errorf("handshake to %s timed out after %s: %w", addr, timeout, os.ErrDeadlineExceeded)
	}
	return err
}
