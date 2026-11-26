package direct

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

const directPacketSessionMaxSize = 65535

var directPacketBuffers = sync.Pool{
	New: func() any {
		b := make([]byte, directPacketSessionMaxSize)
		return &b
	},
}

// Compile-time guarantee that Direct supports RFC 4787 EIM UDP sessions.
var _ transport.PacketSessionDialer = (*Direct)(nil)

// DialPacketSession opens an unconnected UDP socket bound to the requested
// address family ("udp4" or "udp6") using Spaceship's outbound socket control
// (SO_MARK / IP_BOUND_IF / IP_UNICAST_IF / protector).
func (d *Direct) DialPacketSession(
	ctx context.Context,
	network string,
	filtering transport.PacketFiltering,
) (transport.MultiPacketConn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	family := normalizeSessionNetwork(network)
	if family == "" {
		return nil, fmt.Errorf("direct: unsupported packet session network %q", network)
	}
	if family == "udp6" && transport.PreferIPv4() {
		return nil, errors.New("direct: IPv6 disabled, cannot open udp6 packet session")
	}

	lc := transport.NewOutboundListenConfig()
	pc, err := lc.ListenPacket(ctx, family, ":0")
	if err != nil {
		return nil, fmt.Errorf("direct: listen %s packet session: %w", family, err)
	}
	udpConn, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return nil, fmt.Errorf("direct: packet session listen returned %T", pc)
	}
	return &directPacketSession{
		conn:   udpConn,
		family: family,
		filter: transport.NewPeerFilter(filtering, 1024),
	}, nil
}

func normalizeSessionNetwork(network string) string {
	switch transport.DialNetwork(network) {
	case "udp", "udp4":
		return "udp4"
	case "udp6":
		return "udp6"
	default:
		return ""
	}
}

type directPacketSession struct {
	conn   *net.UDPConn
	family string
	filter *transport.PeerFilter
}

func (s *directPacketSession) ReadPacketFrom() ([]byte, netip.AddrPort, error) {
	bufPtr := directPacketBuffers.Get().(*[]byte)
	defer directPacketBuffers.Put(bufPtr)
	buf := *bufPtr

	for {
		n, peer, err := s.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return nil, netip.AddrPort{}, err
		}
		peer = netip.AddrPortFrom(peer.Addr().Unmap(), peer.Port())
		if !s.filter.PermitInbound(peer) {
			continue
		}
		out := make([]byte, n)
		copy(out, buf[:n])
		return out, peer, nil
	}
}

func (s *directPacketSession) WritePacketTo(payload []byte, dst transport.PacketTarget) error {
	target, err := s.resolveTarget(dst)
	if err != nil {
		return err
	}
	s.filter.AllowOutbound(target)
	n, err := s.conn.WriteToUDPAddrPort(payload, target)
	if err != nil {
		return err
	}
	if n != len(payload) {
		return fmt.Errorf("direct: short UDP write: %d < %d", n, len(payload))
	}
	return nil
}

func (s *directPacketSession) resolveTarget(dst transport.PacketTarget) (netip.AddrPort, error) {
	if dst.Addr.IsValid() {
		addr := dst.Addr.Addr().Unmap()
		if addr.IsUnspecified() || addr.IsMulticast() || dst.Addr.Port() == 0 {
			return netip.AddrPort{}, fmt.Errorf("direct: invalid packet destination %s", dst.Addr)
		}
		if s.family == "udp4" && !addr.Is4() {
			return netip.AddrPort{}, fmt.Errorf("direct: cannot send IPv6 destination %s on udp4 session", dst.Addr)
		}
		if s.family == "udp6" && !addr.Is6() {
			return netip.AddrPort{}, fmt.Errorf("direct: cannot send IPv4 destination %s on udp6 session", dst.Addr)
		}
		return netip.AddrPortFrom(addr, dst.Addr.Port()), nil
	}
	if dst.Host == "" || dst.Port == 0 {
		return netip.AddrPort{}, errors.New("direct: empty packet destination")
	}
	ipNet := "ip4"
	if s.family == "udp6" {
		ipNet = "ip6"
	}
	ctx, cancel := context.WithTimeout(context.Background(), transport.GetDialTimeout())
	defer cancel()
	addrs, err := transport.OutboundResolver().LookupNetIP(ctx, ipNet, dst.Host)
	if err != nil || len(addrs) == 0 {
		if err == nil {
			err = fmt.Errorf("no %s address for %s", ipNet, dst.Host)
		}
		return netip.AddrPort{}, err
	}
	addr := addrs[0].Unmap()
	if addr.IsUnspecified() || addr.IsMulticast() {
		return netip.AddrPort{}, fmt.Errorf("direct: invalid resolved address %s for %s", addr, dst.Host)
	}
	return netip.AddrPortFrom(addr, dst.Port), nil
}

func (s *directPacketSession) LocalAddr() net.Addr {
	return s.conn.LocalAddr()
}

func (s *directPacketSession) SetDeadline(t time.Time) error {
	return s.conn.SetDeadline(t)
}

func (s *directPacketSession) SetReadDeadline(t time.Time) error {
	return s.conn.SetReadDeadline(t)
}

func (s *directPacketSession) SetWriteDeadline(t time.Time) error {
	return s.conn.SetWriteDeadline(t)
}

func (s *directPacketSession) Close() error {
	return s.conn.Close()
}
