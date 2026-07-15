package direct

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

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
	return net.DialTimeout(transport.DialNetwork(network), addr, transport.GetDialTimeout())
}

// DialPacket opens a local UDP socket for packet-oriented communication.
//
// The local socket is bound on the same address family as the target. The
// generic "udp" network yields an IPv6 dual-stack socket even for "0.0.0.0:0",
// and such a socket does not reliably receive replies from an IPv4 peer — the
// return traffic is silently black-holed. We therefore resolve the target and
// bind with the family-specific network ("udp4"/"udp6") to force the family.
// When IPv6 is disabled, resolution and binding are forced onto IPv4.
func (d *Direct) DialPacket(network, addr string) (net.PacketConn, error) {
	conn, _, err := d.DialPacketTarget(network, addr)
	return conn, err
}

// DialPacketTarget resolves addr once, binds a matching socket, and returns the
// exact address that must be used with that socket.
func (d *Direct) DialPacketTarget(network, addr string) (net.PacketConn, net.Addr, error) {
	network = transport.DialNetwork(network)
	raddr, err := net.ResolveUDPAddr(network, addr)
	if err != nil {
		return nil, nil, fmt.Errorf("direct: resolve packet addr %s: %w", addr, err)
	}

	bindNetwork, bindAddr := "udp6", "[::]:0"
	if network == "udp4" || raddr.IP == nil || raddr.IP.To4() != nil {
		bindNetwork, bindAddr = "udp4", "0.0.0.0:0"
	}
	if bindNetwork == "udp6" && transport.PreferIPv4() {
		return nil, nil, fmt.Errorf("direct: IPv6 disabled, cannot dial packet target %s", addr)
	}

	conn, err := net.ListenPacket(bindNetwork, bindAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("direct: listen packet %s: %w", bindNetwork, err)
	}
	return conn, raddr, nil
}

func (d *Direct) Close() error {
	return nil
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

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var closeOnce sync.Once
	closeSession := func() {
		closeOnce.Do(func() {
			transport.CloseAll(src, dst, conn)
		})
	}

	var responseDone atomic.Bool
	var errGroup errgroup.Group
	errGroup.Go(func() error {
		err := transport.CopyWithContext(sessionCtx, closeSession, conn, src, transport.DirectionOut)
		if err != nil && !errors.Is(err, io.EOF) {
			cancel()
			closeSession()
			return err
		}
		transport.CloseWriteOrClose(conn)
		return err
	})

	errGroup.Go(func() error {
		err := transport.CopyWithContext(sessionCtx, closeSession, dst, conn, transport.DirectionIn)
		if err == nil || errors.Is(err, io.EOF) {
			responseDone.Store(true)
		}
		cancel()
		closeSession()
		return err
	})

	if err = errGroup.Wait(); err != nil && !errors.Is(err, io.EOF) {
		if responseDone.Load() {
			return nil
		}
		return fmt.Errorf("direct: %w", err)
	}
	return nil
}
