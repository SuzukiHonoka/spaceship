package server

import (
	"context"
	"errors"
	"time"

	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
)

var errProxyFirstMessageTimeout = errors.New("proxy: first message timeout")

type proxyFirstMessageResult struct {
	message *proto.ProxySRC
	err     error
}

// receiveProxyFirstMessage puts a strict lifetime on an authenticated stream
// that has not sent its routing header. grpc-go cancels the stream context when
// the handler returns, which releases the Recv goroutine in production. The
// channel is buffered so a result racing the timeout never blocks its sender.
func receiveProxyFirstMessage(
	ctx context.Context,
	stream proto.Proxy_ProxyServer,
	timeout time.Duration,
) (*proto.ProxySRC, error) {
	result := make(chan proxyFirstMessageResult, 1)
	go func() {
		message, err := stream.Recv()
		result <- proxyFirstMessageResult{message: message, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case value := <-result:
		if value.err != nil {
			return nil, value.err
		}
		if value.message == nil {
			return nil, errors.New("proxy: received nil first message")
		}
		return value.message, nil
	case <-timer.C:
		return nil, errProxyFirstMessageTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
