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
	defer cancel()
	target := net.JoinHostPort(req.Fqdn, strconv.Itoa(int(req.Port)))
	conn, err := net.DialTimeout(transport.Network, target, 3*time.Minute)
	if err != nil {
		localAddr <- ""
		return err
	}
	defer transport.ForceClose(conn)
	// src -> dst
	go func() {
		err := fmt.Errorf("src -> dst error: %w", streamCopy(src, conn))
		transport.PrintErrorIfCritical(err, "direct")
		cancel()
	}()
	// src <- dst
	go func() {
		err := fmt.Errorf("src <- dst error: %w", streamCopy(conn, dst))
		transport.PrintErrorIfCritical(err, "direct")
		cancel()
	}()
	<-ctx.Done()
	return nil
}
