package tun

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
)

// partialRoute delivers part of a response and then fails, the way a tunnel
// does when its upstream resets. abort selects whether it resets the client
// connection (what the transports now do) or leaves it to an orderly close.
type partialRoute struct {
	abort bool
}

func (r *partialRoute) String() string                        { return "test-partial" }
func (r *partialRoute) Dial(string, string) (net.Conn, error) { return nil, errors.New("not used") }
func (r *partialRoute) Close() error                          { return nil }

func (r *partialRoute) Proxy(
	_ context.Context,
	_ string,
	localAddr chan<- string,
	dst io.Writer,
	_ io.Reader,
) error {
	defer close(localAddr)
	localAddr <- "127.0.0.1:1"
	if _, err := dst.Write([]byte("partial")); err != nil {
		return err
	}
	if r.abort {
		transport.Abort(dst)
	}
	return errors.New("upstream failed mid-response")
}

func readTUNStream(t *testing.T, route transport.Transport) error {
	t.Helper()
	service, peer, peerNIC := newLinkedService(t, Config{MaxConnections: 4})
	service.setResolveRoute(func(string) (transport.Transport, error) { return route, nil })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := gonet.DialContextTCP(ctx, peer, tcpip.FullAddress{
		NIC:  peerNIC,
		Addr: tcpip.AddrFrom4([4]byte{198, 51, 100, 20}),
		Port: 443,
	}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("DialContextTCP() error = %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = io.ReadAll(conn)
	return err
}

func TestTCPFlowAbortReachesApplicationAsReset(t *testing.T) {
	err := readTUNStream(t, &partialRoute{abort: true})
	if err == nil || !strings.Contains(err.Error(), "reset") {
		t.Fatalf("application behind the TUN saw %v, want a connection reset", err)
	}
}

func TestTCPFlowOrderlyCloseStaysOrderly(t *testing.T) {
	if err := readTUNStream(t, &partialRoute{abort: false}); err != nil {
		t.Fatalf("application behind the TUN saw %v, want a clean EOF", err)
	}
}

var _ transport.Aborter = (*tcpConn)(nil)
