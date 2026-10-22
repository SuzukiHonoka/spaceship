package tun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// udpDatagramSize is the largest UDP payload a session will copy. Buffers of
// this size are borrowed from udpBuffers only while a datagram is in hand, so
// an idle session holds none.
const udpDatagramSize = 65535

var udpBuffers = sync.Pool{
	New: func() any {
		buf := make([]byte, udpDatagramSize)
		return &buf
	},
}

// udpSession is one general UDP 4-tuple. It owns one MaxConnections slot and
// one MaxUDPSessions slot until finishUDPSession releases them, or until a
// newer session takes them over through replaceLRUUDPSessionLocked.
type udpSession struct {
	key      string
	ctx      context.Context
	cancel   context.CancelFunc
	activity atomic.Int64
}

func (u *udpSession) touch() {
	u.activity.Store(time.Now().UnixNano())
}

// udpTUNSide is the gVisor half of a session: reads come from read, which
// lends a buffer only while a datagram is in hand, and replies go to conn.
type udpTUNSide struct {
	conn net.Conn
	read func(deadline time.Time) ([]byte, func(), error)
}

// handleUDPPacket is the gVisor UDP protocol handler. Port 53 stays on the DNS
// hijack path while that feature is enabled. Every other flow, including port
// 53 when hijacking is disabled, is a normal UDP session. Returning false lets
// the stack answer with ICMP unreachable; that is reserved for a flow that was
// not accepted, not for "this is not DNS".
func (s *Service) handleUDPPacket(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
	if pkt == nil {
		return false
	}
	if s.cfg.DNS.Enabled && id.LocalPort == 53 {
		return s.handleUDPDNSPacket(id, pkt)
	}
	return s.handleGeneralUDPPacket(id, pkt)
}

// handleGeneralUDPPacket opens one session for id. Later datagrams for that
// 4-tuple are delivered to the gVisor endpoint created here, not to this
// function.
func (s *Service) handleGeneralUDPPacket(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
	if pkt == nil {
		return false
	}
	if _, err := endpointAddress(id.LocalAddress, id.LocalPort); err != nil {
		return false
	}
	session, admitted := s.admitUDPSession(udpFlowKey(id))
	if !admitted {
		return false
	}
	if session == nil {
		// A duplicate first packet raced endpoint registration. The live
		// session owns the 4-tuple; do not open another or ICMP-reject it.
		return true
	}

	cloned := pkt.Clone()
	request := udp.NewForwarderRequest(s.stack, id, cloned)
	var waitQueue waiter.Queue
	endpoint, udpErr := request.CreateEndpoint(&waitQueue)
	cloned.DecRef()
	if udpErr != nil {
		s.finishUDPSession(session)
		return false
	}

	conn := gonet.NewUDPConn(&waitQueue, endpoint)
	tun := udpTUNSide{conn: conn, read: endpointPacketReader(session.ctx, endpoint, &waitQueue)}
	go func() {
		defer s.finishUDPSession(session)
		defer utils.Close(conn)
		if err := s.proxyUDP(session, tun, id); err != nil &&
			!errors.Is(err, context.Canceled) &&
			!errors.Is(err, io.EOF) &&
			!errors.Is(err, net.ErrClosed) {
			log.Printf("tun: UDP flow failed: %v", err)
		}
	}()
	return true
}

func udpFlowKey(id stack.TransportEndpointID) string {
	return fmt.Sprintf("%s|%d|%s|%d", id.RemoteAddress, id.RemotePort, id.LocalAddress, id.LocalPort)
}

// admitUDPSession reserves slots for key. It returns (nil, true) when key
// already has a live session, and (nil, false) when the flow must be refused.
//
// A session needs a MaxConnections slot and a MaxUDPSessions slot. When either
// is exhausted, the least recently active UDP session is replaced and its two
// slots pass straight to the new one. UDP therefore never takes TCP's share of
// MaxConnections, and a burst of new 4-tuples ages out idle sessions rather
// than being refused while they wait for their idle timeout.
func (s *Service) admitUDPSession(key string) (*udpSession, bool) {
	s.udpMu.Lock()
	defer s.udpMu.Unlock()
	if s.udpFlows == nil {
		s.udpFlows = make(map[string]*udpSession)
	}
	if _, exists := s.udpFlows[key]; exists {
		return nil, true
	}

	if tryAcquireSlot(s.udpSessionSlots) {
		if s.beginFlow() {
			return s.newUDPSessionLocked(key), true
		}
		releaseSlot(s.udpSessionSlots)
	}
	if !s.replaceLRUUDPSessionLocked() {
		return nil, false
	}
	return s.newUDPSessionLocked(key), true
}

func (s *Service) newUDPSessionLocked(key string) *udpSession {
	ctx, cancel := context.WithCancel(s.ctx)
	session := &udpSession{key: key, ctx: ctx, cancel: cancel}
	session.touch()
	s.udpFlows[key] = session
	return session
}

// replaceLRUUDPSessionLocked evicts the least recently active session and
// leaves its two slots reserved for the caller. The evicted session is gone
// from udpFlows, so its finishUDPSession only ends its WaitGroup entry. The
// caller gets a WaitGroup entry of its own from adoptFlow.
func (s *Service) replaceLRUUDPSessionLocked() bool {
	var victim *udpSession
	oldest := int64(math.MaxInt64)
	for _, session := range s.udpFlows {
		if at := session.activity.Load(); at < oldest {
			oldest = at
			victim = session
		}
	}
	if victim == nil || !s.adoptFlow() {
		return false
	}
	delete(s.udpFlows, victim.key)
	victim.cancel()
	return true
}

// finishUDPSession releases the session's slots unless a newer session has
// taken them over, in which case only its WaitGroup entry ends here.
//
// Slots are released under udpMu, so admitUDPSession never sees a full budget
// with no session left to replace.
func (s *Service) finishUDPSession(session *udpSession) {
	session.cancel()
	s.udpMu.Lock()
	defer s.udpMu.Unlock()
	if s.udpFlows[session.key] != session {
		s.flows.Done()
		return
	}
	delete(s.udpFlows, session.key)
	releaseSlot(s.udpSessionSlots)
	s.endFlow()
}

func (s *Service) udpIdleTimeout() time.Duration {
	if s.cfg.UDPIdleTimeout > 0 {
		return s.cfg.UDPIdleTimeout
	}
	return DefaultUDPIdleTimeout
}

// endpointPacketReader reads datagrams straight from the gVisor endpoint. It
// waits on the endpoint's queue without a buffer and borrows one from
// udpBuffers only once a datagram is queued. The release function returns it.
func endpointPacketReader(
	ctx context.Context,
	endpoint tcpip.Endpoint,
	waitQueue *waiter.Queue,
) func(time.Time) ([]byte, func(), error) {
	const events = waiter.ReadableEvents | waiter.EventHUp | waiter.EventErr
	return func(deadline time.Time) ([]byte, func(), error) {
		entry, notify := waiter.NewChannelEntry(events)
		waitQueue.EventRegister(&entry)
		defer waitQueue.EventUnregister(&entry)

		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		for {
			if endpoint.Readiness(events) != 0 {
				bufp := udpBuffers.Get().(*[]byte)
				writer := tcpip.SliceWriter(*bufp)
				result, err := endpoint.Read(&writer, tcpip.ReadOptions{})
				if err == nil {
					return (*bufp)[:result.Count], func() { udpBuffers.Put(bufp) }, nil
				}
				udpBuffers.Put(bufp)
				switch err.(type) {
				case *tcpip.ErrWouldBlock:
				case *tcpip.ErrClosedForReceive:
					return nil, nil, io.EOF
				default:
					return nil, nil, errors.New(err.String())
				}
			}
			select {
			case <-notify:
			case <-timer.C:
				return nil, nil, os.ErrDeadlineExceeded
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
	}
}

// routePacketReader reads from the route's packet connection. A PacketReader
// hands over each datagram without a waiting buffer. Any other connection gets
// one buffer for the session, because ReadFrom must have it while blocked.
func routePacketReader(packet net.PacketConn) func(time.Time) ([]byte, func(), error) {
	if reader, ok := packet.(transport.PacketReader); ok {
		return func(deadline time.Time) ([]byte, func(), error) {
			_ = packet.SetReadDeadline(deadline)
			payload, _, err := reader.ReadPacket()
			return payload, nil, err
		}
	}
	var buf []byte
	return func(deadline time.Time) ([]byte, func(), error) {
		if buf == nil {
			buf = make([]byte, udpDatagramSize)
		}
		_ = packet.SetReadDeadline(deadline)
		n, _, err := packet.ReadFrom(buf)
		return buf[:n], nil, err
	}
}

// proxyUDP forwards one connected gVisor UDP flow through the route's packet
// transport. A route that cannot carry UDP fails the flow the same way a TCP
// route lookup failure does: the error is returned, the route is closed, and
// the session ends.
func (s *Service) proxyUDP(session *udpSession, tun udpTUNSide, id stack.TransportEndpointID) error {
	destination, err := endpointAddress(id.LocalAddress, id.LocalPort)
	if err != nil {
		return err
	}
	route, err := s.routeUDP(destination.Addr().String())
	if err != nil {
		return fmt.Errorf("route %s: %w", destination, err)
	}
	defer utils.Close(route)

	network := "udp4"
	if destination.Addr().Is6() {
		network = "udp6"
	}
	packet, target, err := dialUDPPacket(session.ctx, route, network, destination.String())
	if err != nil {
		return fmt.Errorf("route %s: %w", destination, err)
	}
	defer utils.Close(packet)

	sessionCtx, cancel := context.WithCancel(session.ctx)
	defer cancel()
	go func() {
		<-sessionCtx.Done()
		_ = tun.conn.Close()
		_ = packet.Close()
	}()

	errc := make(chan error, 2)
	go func() {
		errc <- s.pumpUDP(sessionCtx, session, tun.read, func(payload []byte) error {
			_ = packet.SetWriteDeadline(time.Now().Add(s.udpIdleTimeout()))
			n, writeErr := packet.WriteTo(payload, target)
			if writeErr != nil {
				return writeErr
			}
			if n != len(payload) {
				return io.ErrShortWrite
			}
			return nil
		})
	}()
	go func() {
		errc <- s.pumpUDP(sessionCtx, session, routePacketReader(packet), func(payload []byte) error {
			_ = tun.conn.SetWriteDeadline(time.Now().Add(s.udpIdleTimeout()))
			n, writeErr := tun.conn.Write(payload)
			if writeErr != nil {
				return writeErr
			}
			if n != len(payload) {
				return io.ErrShortWrite
			}
			return nil
		})
	}()
	// The first direction to finish, including an idle timeout, cancels the
	// session so the other read cannot sit until its own deadline.
	err1 := <-errc
	cancel()
	err2 := <-errc
	return udpFlowError(err1, err2)
}

func (s *Service) routeUDP(ip string) (transport.Transport, error) {
	fn := s.getResolveRoute()
	if fn == nil {
		return nil, errors.New("tun: no route resolver")
	}
	return fn(ip)
}

func (s *Service) pumpUDP(
	ctx context.Context,
	session *udpSession,
	read func(time.Time) ([]byte, func(), error),
	write func([]byte) error,
) error {
	timeout := s.udpIdleTimeout()
	idleSince := func() time.Time {
		return time.Unix(0, session.activity.Load()).Add(timeout)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		payload, release, err := read(idleSince())
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if isTimeout(err) {
				// The other direction may have moved the activity clock.
				if time.Now().Before(idleSince()) {
					continue
				}
				return nil
			}
			return err
		}
		session.touch()
		err = write(payload)
		if release != nil {
			release()
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if isTimeout(err) && !time.Now().Before(idleSince()) {
				return nil
			}
			return err
		}
	}
}

func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func udpFlowError(errs ...error) error {
	for _, err := range errs {
		if err == nil ||
			errors.Is(err, context.Canceled) ||
			errors.Is(err, io.EOF) ||
			errors.Is(err, io.ErrClosedPipe) ||
			errors.Is(err, net.ErrClosed) {
			continue
		}
		return err
	}
	return nil
}

// dialUDPPacket opens the route's packet connection. A transport without
// PacketDialer is an error: falling back to a local UDP socket would bypass
// the tunnel. Proxy transports keep DNS resolution on the far side when they
// implement PacketTargetDialer; TUN destinations are already IP literals.
func dialUDPPacket(ctx context.Context, route transport.Transport, network, target string) (net.PacketConn, net.Addr, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if _, ok := route.(transport.PacketDialer); !ok {
		return nil, nil, fmt.Errorf("egress %s does not support UDP", route)
	}

	if targetDialer, ok := route.(transport.PacketTargetDialer); ok {
		var (
			conn net.PacketConn
			addr net.Addr
			err  error
		)
		if dialer, ok := route.(transport.ContextPacketTargetDialer); ok {
			conn, addr, err = dialer.DialPacketTargetContext(ctx, network, target)
		} else {
			conn, addr, err = targetDialer.DialPacketTarget(network, target)
		}
		if err != nil {
			if conn != nil {
				_ = conn.Close()
			}
			return nil, nil, err
		}
		if conn == nil {
			return nil, nil, errors.New("packet target dialer returned a nil connection")
		}
		if addr == nil {
			_ = conn.Close()
			return nil, nil, errors.New("packet target dialer returned a nil target address")
		}
		return conn, addr, nil
	}

	packetDialer := route.(transport.PacketDialer)
	var (
		conn net.PacketConn
		err  error
	)
	if dialer, ok := route.(transport.ContextPacketDialer); ok {
		conn, err = dialer.DialPacketContext(ctx, network, target)
	} else {
		conn, err = packetDialer.DialPacket(network, target)
	}
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, nil, err
	}
	if conn == nil {
		return nil, nil, errors.New("packet dialer returned a nil connection")
	}
	addr, err := udpTargetAddr(target)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, addr, nil
}

func udpTargetAddr(target string) (net.Addr, error) {
	parsed, err := netip.ParseAddrPort(target)
	if err != nil {
		return nil, fmt.Errorf("invalid udp target %s: %w", target, err)
	}
	return net.UDPAddrFromAddrPort(parsed), nil
}
