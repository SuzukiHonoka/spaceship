package transport

import (
	"context"
	"io"
	"net"
)

type Transport interface {
	String() string
	Proxy(ctx context.Context, addr string, localAddr chan<- string, dst io.Writer, src io.Reader) error
	Dial(network, addr string) (net.Conn, error)
	Close() error
}

// PacketDialer is an optional interface implemented by transports that support
// packet-oriented (UDP) communication. Use a type assertion to check support:
//
//	if pd, ok := t.(transport.PacketDialer); ok { ... }
type PacketDialer interface {
	DialPacket(network, addr string) (net.PacketConn, error)
}
