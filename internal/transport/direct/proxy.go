package direct

import (
	"context"
	"fmt"
	"io"
	"net"
	"spaceship/internal/transport"
	"strconv"
	"time"
)

type Direct struct{}

func streamCopy(r io.Reader, w io.Writer) error {
	buf := make([]byte, transport.BufferSize)
	for {
		// read from target
		n, err := r.Read(buf)
		if err != nil {
			return err
		}
		// forward data
		wn, err := w.Write(buf[:n])
		if err != nil {
			return err
		}
		if wn != n {
			return fmt.Errorf("forward data to reader failed, received: %d sent: %d loss: %d %w", n, wn, wn/n, transport.ErrorPacketLoss)
		}
	}
}

// Proxy the traffic locally
func (d Direct) Proxy(ctx context.Context, localAddr chan<- string, dst io.Writer, src io.Reader) error {
	req, ok := ctx.Value("request").(*transport.Request)
	if !ok {
		return transport.ErrorRequestNotFound
	}
	ctx, cancel := context.WithCancel(ctx)
	target := net.JoinHostPort(req.Fqdn, strconv.Itoa(int(req.Port)))
	conn, err := net.DialTimeout(transport.Network, target, 3*time.Minute)
	if err != nil {
		cancel()
		localAddr <- ""
		return err
	}
	// src -> dst
	go func() {
		err := streamCopy(src, conn)
		transport.PrintErrorIfNotCritical(err, "direct: src -> dst error")
		cancel()
	}()
	// src <- dst
	go func() {
		err := streamCopy(conn, dst)
		transport.PrintErrorIfNotCritical(err, "direct: src <- dst error")
		cancel()
	}()
	<-ctx.Done()
	return nil
}
