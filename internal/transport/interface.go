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

// ContextDialer is implemented by transports whose connection establishment can
// be canceled. Long-lived frontends should prefer it so shutdown is not held by
// an in-progress network dial.
type ContextDialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// PacketDialer is an optional interface implemented by transports that support
// packet-oriented (UDP) communication. Use a type assertion to check support:
//
//	if pd, ok := t.(transport.PacketDialer); ok { ... }
type PacketDialer interface {
	DialPacket(network, addr string) (net.PacketConn, error)
}

// PacketTargetDialer extends PacketDialer for transports that resolve a packet
// target locally. Returning the address selected while opening the socket keeps
// address-family selection and the destination address consistent, and avoids
// a second DNS lookup in callers.
type PacketTargetDialer interface {
	PacketDialer
	DialPacketTarget(network, addr string) (net.PacketConn, net.Addr, error)
}
