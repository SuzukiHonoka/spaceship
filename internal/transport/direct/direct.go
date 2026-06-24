package direct

import (
	"context"
	"fmt"
	"io"
	"net"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"golang.org/x/sync/errgroup"
)

const TransportName = "direct"

// Direct is transport that connects directly to the destination.
// Each call to Proxy is fully self-contained — no mutable state is stored.
type Direct struct{}

func New() transport.Transport {
	return &Direct{}
}

func (d *Direct) String() string {
	return TransportName
}

func (d *Direct) Dial(network, addr string) (net.Conn, error) {
	return net.DialTimeout(network, addr, transport.GetDialTimeout())
}

// DialPacket opens a local UDP socket for packet-oriented communication.
//
// The local socket is bound on the same address family as the target. The
// generic "udp" network yields an IPv6 dual-stack socket even for "0.0.0.0:0",
// and such a socket does not reliably receive replies from an IPv4 peer — the
// return traffic is silently black-holed. We therefore resolve the target and
// bind with the family-specific network ("udp4"/"udp6") to force the family.
func (d *Direct) DialPacket(network, addr string) (net.PacketConn, error) {
	raddr, err := net.ResolveUDPAddr(network, addr)
	if err != nil {
		return nil, fmt.Errorf("direct: resolve packet addr %s: %w", addr, err)
	}
	if raddr.IP.To4() != nil {
		return net.ListenPacket("udp4", "0.0.0.0:0")
	}
	return net.ListenPacket("udp6", "[::]:0")
}

func (d *Direct) Close() error {
	return nil
}

func copyBuffer(ctx context.Context, conn net.Conn, dst io.Writer, src io.Reader, direction transport.Direction) error {
	buf := transport.Buffer()
	defer transport.PutBuffer(buf)

	// Use a buffered channel so the goroutine never blocks on send,
	// preventing a goroutine leak when we return early on ctx cancellation.
	type copyResult struct {
		n   int64
		err error
	}
	resultCh := make(chan copyResult, 1)
	go func() {
		n, err := io.CopyBuffer(dst, src, *buf)
		resultCh <- copyResult{n, err}
	}()

	select {
	case result := <-resultCh:
		transport.GlobalStats.Add(direction, result.n)
		return result.err
	case <-ctx.Done():
		_ = conn.Close()
		// Wait for the goroutine to exit before reading n to avoid a data race.
		result := <-resultCh
		transport.GlobalStats.Add(direction, result.n)
		return ctx.Err()
	}
}

// Proxy the traffic locally. The connection lifecycle is fully local.
func (d *Direct) Proxy(ctx context.Context, addr string, localAddr chan<- string, dst io.Writer, src io.Reader) (err error) {
	defer close(localAddr)

	conn, err := d.Dial(transport.GetNetwork(), addr)
	if err != nil {
		return fmt.Errorf("direct: failed to dial: %w", err)
	}
	localAddr <- conn.LocalAddr().String()
	defer utils.Close(conn)

	errGroup, ctx := errgroup.WithContext(ctx)
	// src -> dst
	errGroup.Go(func() error {
		return copyBuffer(ctx, conn, conn, src, transport.DirectionOut)
	})
	// src <- dst
	errGroup.Go(func() error {
		return copyBuffer(ctx, conn, dst, conn, transport.DirectionIn)
	})

	if err = errGroup.Wait(); err != nil && err != io.EOF {
		return fmt.Errorf("direct: %w", err)
	}
	return nil
}
