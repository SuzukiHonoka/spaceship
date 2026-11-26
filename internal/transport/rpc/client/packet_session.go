package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const packetSessionLegacyTTL = time.Minute

var (
	packetSessionLegacyUntil atomic.Int64
	packetSessionNotice      atomic.Bool
	packetSessionEpoch       atomic.Uint64
)

func resetPacketSessionLegacy() {
	packetSessionEpoch.Add(1)
	packetSessionLegacyUntil.Store(0)
	packetSessionNotice.Store(false)
}

func markPacketSessionLegacy(epoch uint64, observed int64) {
	if packetSessionEpoch.Load() != epoch {
		return
	}
	until := time.Now().Add(packetSessionLegacyTTL).UnixNano()
	if !packetSessionLegacyUntil.CompareAndSwap(observed, until) {
		return
	}
	if packetSessionEpoch.Load() != epoch {
		packetSessionLegacyUntil.CompareAndSwap(until, 0)
		return
	}
	if packetSessionNotice.CompareAndSwap(false, true) {
		log.Println("rpc: server does not support ProxyPacket (EIM UDP NAT); falling back to per-target UDP streams. Upgrade the server for full-cone NAT")
	}
}

func transportFilteringToProto(f transport.PacketFiltering) proto.PacketFiltering {
	switch f {
	case transport.PacketFilteringAD:
		return proto.PacketFiltering_PacketFilteringAD
	case transport.PacketFilteringAPD:
		return proto.PacketFiltering_PacketFilteringAPD
	default:
		return proto.PacketFiltering_PacketFilteringEIF
	}
}

// DialPacketSession opens a multiplexed unconnected UDP session (RFC 4787 EIM)
// over the server's ProxyPacket RPC. If the server does not implement
// ProxyPacket, it caches that state for a minute and returns
// transport.ErrPacketSessionUnsupported so callers can fall back to 4-tuple UDP.
func (c *Client) DialPacketSession(
	parent context.Context,
	network string,
	filtering transport.PacketFiltering,
) (transport.MultiPacketConn, error) {
	if c == nil || c.ProxyClient == nil {
		return nil, errors.New("rpc client: not initialized")
	}
	if parent == nil {
		parent = context.Background()
	}
	var family string
	switch network {
	case "", "udp", "udp4":
		family = "udp4"
	case "udp6":
		family = "udp6"
	default:
		return nil, fmt.Errorf("rpc client: unsupported packet session network %q", network)
	}

	epoch := packetSessionEpoch.Load()
	observed := packetSessionLegacyUntil.Load()
	if time.Now().UnixNano() < observed {
		return nil, transport.ErrPacketSessionUnsupported
	}

	ctx, cancelCause := context.WithCancelCause(parent)
	cancel := func() { cancelCause(context.Canceled) }
	timeout := transport.GetDialTimeout()
	setupTimer := time.AfterFunc(timeout, func() { cancelCause(context.DeadlineExceeded) })
	defer setupTimer.Stop()

	stream, err := c.ProxyPacket(ctx)
	if err != nil {
		cancel()
		if status.Code(err) == codes.Unimplemented {
			markPacketSessionLegacy(epoch, observed)
			return nil, transport.ErrPacketSessionUnsupported
		}
		return nil, fmt.Errorf("rpc client: open packet session: %w", err)
	}

	if err := stream.Send(&proto.PacketFrame{
		Status: proto.PacketStatus_PacketSession,
		Init: &proto.PacketInit{
			Family:    family,
			Filtering: transportFilteringToProto(filtering),
		},
	}); err != nil {
		// On gRPC, Send returning io.EOF means the server closed the stream
		// with a status (such as Unimplemented). Recv surfaces that status.
		_, recvErr := stream.Recv()
		cancel()
		if status.Code(recvErr) == codes.Unimplemented || status.Code(err) == codes.Unimplemented {
			markPacketSessionLegacy(epoch, observed)
			return nil, transport.ErrPacketSessionUnsupported
		}
		if recvErr != nil {
			err = recvErr
		}
		return nil, fmt.Errorf("rpc client: packet session init: %w", err)
	}

	accepted, err := stream.Recv()
	if err != nil {
		cause := context.Cause(ctx)
		cancel()
		if status.Code(err) == codes.Unimplemented {
			markPacketSessionLegacy(epoch, observed)
			return nil, transport.ErrPacketSessionUnsupported
		}
		if errors.Is(cause, context.DeadlineExceeded) {
			return nil, fmt.Errorf("rpc client: packet session handshake timed out: %w", context.DeadlineExceeded)
		}
		return nil, fmt.Errorf("rpc client: packet session handshake: %w", err)
	}
	if !setupTimer.Stop() {
		cancelCause(context.DeadlineExceeded)
		return nil, fmt.Errorf("rpc client: packet session handshake timed out: %w", context.DeadlineExceeded)
	}
	if accepted.GetStatus() != proto.PacketStatus_PacketAccepted {
		cancel()
		return nil, fmt.Errorf("rpc client: unexpected packet session status %v", accepted.GetStatus())
	}

	localAddr := net.Addr(zeroUDPAddr)
	if ip, ok := netip.AddrFromSlice(accepted.GetIp()); ok && accepted.GetPort() <= 65535 {
		localAddr = net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip.Unmap(), uint16(accepted.GetPort())))
	}
	return newRPCPacketSession(ctx, stream, cancel, localAddr), nil
}

type packetRecvItem struct {
	payload []byte
	peer    netip.AddrPort
	err     error
}

type rpcPacketSession struct {
	stream    proto.Proxy_ProxyPacketClient
	cancel    context.CancelFunc
	ctx       context.Context
	localAddr net.Addr

	recvCh chan packetRecvItem

	readMu    sync.Mutex
	writeMu   sync.Mutex
	rdeadline pipeDeadline
	wdeadline pipeDeadline

	sendMu  sync.Mutex
	sending bool

	closeOnce sync.Once
	closed    chan struct{}
}

func newRPCPacketSession(
	ctx context.Context,
	stream proto.Proxy_ProxyPacketClient,
	cancel context.CancelFunc,
	localAddr net.Addr,
) *rpcPacketSession {
	s := &rpcPacketSession{
		stream:    stream,
		cancel:    cancel,
		ctx:       ctx,
		localAddr: localAddr,
		recvCh:    make(chan packetRecvItem, 16),
		rdeadline: makePipeDeadline(),
		wdeadline: makePipeDeadline(),
		closed:    make(chan struct{}),
	}
	s.wdeadline.onFire = func() {
		s.sendMu.Lock()
		inFlight := s.sending
		s.sendMu.Unlock()
		if inFlight && s.cancel != nil {
			s.cancel()
		}
	}
	go s.recvLoop()
	return s
}

func (s *rpcPacketSession) recvLoop() {
	defer close(s.recvCh)
	for {
		frame, err := s.stream.Recv()
		if err != nil {
			select {
			case s.recvCh <- packetRecvItem{err: err}:
			case <-s.closed:
			}
			return
		}
		if frame.GetStatus() != proto.PacketStatus_PacketSession {
			continue
		}
		ip, ok := netip.AddrFromSlice(frame.GetIp())
		port := frame.GetPort()
		if !ok || port == 0 || port > 65535 {
			continue
		}
		peer := netip.AddrPortFrom(ip.Unmap(), uint16(port))
		select {
		case s.recvCh <- packetRecvItem{payload: frame.GetPayload(), peer: peer}:
		case <-s.closed:
			return
		}
	}
}

func (s *rpcPacketSession) ReadPacketFrom() ([]byte, netip.AddrPort, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()

	select {
	case <-s.closed:
		return nil, netip.AddrPort{}, net.ErrClosed
	case <-s.rdeadline.wait():
		return nil, netip.AddrPort{}, os.ErrDeadlineExceeded
	default:
	}

	select {
	case <-s.closed:
		return nil, netip.AddrPort{}, net.ErrClosed
	case <-s.rdeadline.wait():
		return nil, netip.AddrPort{}, os.ErrDeadlineExceeded
	case item, ok := <-s.recvCh:
		if !ok {
			select {
			case <-s.closed:
				return nil, netip.AddrPort{}, net.ErrClosed
			default:
				return nil, netip.AddrPort{}, io.EOF
			}
		}
		if item.err != nil {
			select {
			case <-s.closed:
				return nil, netip.AddrPort{}, net.ErrClosed
			default:
				return nil, netip.AddrPort{}, item.err
			}
		}
		return item.payload, item.peer, nil
	}
}

func (s *rpcPacketSession) WritePacketTo(payload []byte, dst transport.PacketTarget) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	select {
	case <-s.closed:
		return net.ErrClosed
	case <-s.wdeadline.wait():
		return os.ErrDeadlineExceeded
	default:
	}

	frame := &proto.PacketFrame{
		Status:  proto.PacketStatus_PacketSession,
		Payload: payload,
	}
	if dst.Addr.IsValid() {
		frame.Ip = dst.Addr.Addr().Unmap().AsSlice()
		frame.Port = uint32(dst.Addr.Port())
	} else if dst.Host != "" && dst.Port > 0 {
		frame.Host = dst.Host
		frame.Port = uint32(dst.Port)
	} else {
		return errors.New("rpc client: invalid packet destination")
	}

	s.sendMu.Lock()
	select {
	case <-s.closed:
		s.sendMu.Unlock()
		return net.ErrClosed
	case <-s.wdeadline.wait():
		s.sendMu.Unlock()
		return os.ErrDeadlineExceeded
	default:
		s.sending = true
	}
	s.sendMu.Unlock()

	err := s.stream.Send(frame)

	s.sendMu.Lock()
	s.sending = false
	s.sendMu.Unlock()

	if err != nil {
		select {
		case <-s.closed:
			return net.ErrClosed
		case <-s.wdeadline.wait():
			return os.ErrDeadlineExceeded
		default:
			return err
		}
	}
	return nil
}

func (s *rpcPacketSession) LocalAddr() net.Addr {
	return s.localAddr
}

func (s *rpcPacketSession) SetDeadline(t time.Time) error {
	_ = s.SetReadDeadline(t)
	return s.SetWriteDeadline(t)
}

func (s *rpcPacketSession) SetReadDeadline(t time.Time) error {
	s.rdeadline.set(t)
	return nil
}

func (s *rpcPacketSession) SetWriteDeadline(t time.Time) error {
	s.wdeadline.set(t)
	return nil
}

func (s *rpcPacketSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.rdeadline.set(time.Time{})
		s.wdeadline.set(time.Time{})
		if s.cancel != nil {
			s.cancel()
		}
	})
	return nil
}
