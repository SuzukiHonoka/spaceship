package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"time"
)

// PacketFiltering selects RFC 4787 NAT filtering behavior for unconnected UDP
// sessions.
type PacketFiltering uint8

const (
	// PacketFilteringEIF allows replies from any remote IP:port (Full Cone /
	// Endpoint-Independent Filtering).
	PacketFilteringEIF PacketFiltering = iota
	// PacketFilteringAD allows replies only from remote IPs the session has
	// previously sent to (Address-Dependent Filtering).
	PacketFilteringAD
	// PacketFilteringAPD allows replies only from remote IP:port pairs the
	// session has previously sent to (Address-and-Port-Dependent Filtering).
	PacketFilteringAPD
)

func (f PacketFiltering) String() string {
	switch f {
	case PacketFilteringEIF:
		return "eif"
	case PacketFilteringAD:
		return "ad"
	case PacketFilteringAPD:
		return "apd"
	default:
		return "unknown"
	}
}

// ErrPacketSessionUnsupported indicates that a transport or remote peer does
// not support multiplexed unconnected UDP sessions (for example an older
// Spaceship server that predates ProxyPacket).
var ErrPacketSessionUnsupported = errors.New("transport: packet session unsupported")

// PacketTarget identifies a destination or source peer for a datagram in a
// MultiPacketConn session. Either Addr (IP:port) is valid, or Host + Port are
// supplied for remote DNS resolution.
type PacketTarget struct {
	Addr netip.AddrPort
	Host string
	Port uint16
}

// MultiPacketConn is a multiplexed unconnected UDP session (RFC 4787 EIM) that
// preserves a single outbound socket across multiple remote peers.
type MultiPacketConn interface {
	ReadPacketFrom() (payload []byte, peer netip.AddrPort, err error)
	WritePacketTo(payload []byte, dst PacketTarget) error
	LocalAddr() net.Addr
	SetDeadline(t time.Time) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
	Close() error
}

// PacketSessionDialer is an optional interface implemented by transports that
// support Endpoint-Independent Mapping (full-cone / restricted-cone) UDP
// sessions.
type PacketSessionDialer interface {
	DialPacketSession(ctx context.Context, network string, filtering PacketFiltering) (MultiPacketConn, error)
}

type Transport interface {
	String() string
	Proxy(ctx context.Context, addr string, localAddr chan<- string, dst io.Writer, src io.Reader) error
	Dial(network, addr string) (net.Conn, error)
	Close() error
}

// AdmittedSession is a tunnel the far end has already authenticated and
// admitted. The target address is sent when Proxy is called, so a front end
// can answer its own client before that address is known.
type AdmittedSession interface {
	Proxy(ctx context.Context, addr string, localAddr chan<- string, w io.Writer, r io.Reader) error
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

// ContextPacketDialer allows owners to cancel packet setup. Stream-based
// implementations may also bind the returned stream's lifetime to ctx.
// The legacy interface remains available for source compatibility.
type ContextPacketDialer interface {
	DialPacketContext(ctx context.Context, network, addr string) (net.PacketConn, error)
}

type ContextPacketTargetDialer interface {
	DialPacketTargetContext(ctx context.Context, network, addr string) (net.PacketConn, net.Addr, error)
}

// PacketReader is an optional net.PacketConn extension for long-lived relays.
// ReadPacket blocks for one datagram, honoring the read deadline, without the
// caller lending a buffer while it waits. The payload is never truncated and
// belongs to the caller. A relay with thousands of idle sessions then holds no
// per-session receive buffer.
type PacketReader interface {
	ReadPacket() ([]byte, net.Addr, error)
}

// PacketTargetDialer extends PacketDialer for transports that resolve a packet
// target locally. Returning the address selected while opening the socket keeps
// address-family selection and the destination address consistent, and avoids
// a second DNS lookup in callers.
type PacketTargetDialer interface {
	PacketDialer
	DialPacketTarget(network, addr string) (net.PacketConn, net.Addr, error)
}
