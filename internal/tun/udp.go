package tun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"sync/atomic"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// udpDatagramSize is the largest UDP payload a session will copy. Sessions are
// bounded by MaxConnections, so this buffer cannot grow without that budget.
const udpDatagramSize = 65535

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
// function. The session holds one MaxConnections slot for its whole life.
func (s *Service) handleGeneralUDPPacket(id stack.TransportEndpointID, pkt *stack.PacketBuffer) bool {
	if pkt == nil {
		return false
	}
	if _, err := endpointAddress(id.LocalAddress, id.LocalPort); err != nil {
		return false
	}
	key := udpFlowKey(id)
	if s.udpFlowTracked(key) {
		// A duplicate first packet raced endpoint registration. The live
		// session owns the 4-tuple; do not open another or ICMP-reject it.
		return true
	}
	if !s.beginFlow() {
		return false
	}
	if !s.trackUDPFlow(key) {
		s.endFlow()
		return true
	}

	cloned := pkt.Clone()
	request := udp.NewForwarderRequest(s.stack, id, cloned)
	var waitQueue waiter.Queue
	endpoint, udpErr := request.CreateEndpoint(&waitQueue)
	cloned.DecRef()
	if udpErr != nil {
		s.untrackUDPFlow(key)
		s.endFlow()
		return false
	}

	conn := gonet.NewUDPConn(&waitQueue, endpoint)
	go func() {
		defer s.untrackUDPFlow(key)
		defer s.endFlow()
		defer utils.Close(conn)
		if err := s.proxyUDP(conn, id); err != nil &&
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

func (s *Service) udpFlowTracked(key string) bool {
	s.udpMu.Lock()
	defer s.udpMu.Unlock()
	_, ok := s.udpFlows[key]
	return ok
}

// trackUDPFlow records key. The caller must already hold a flow slot, so the
// map cannot grow past MaxConnections.
func (s *Service) trackUDPFlow(key string) bool {
	s.udpMu.Lock()
	defer s.udpMu.Unlock()
	if s.udpFlows == nil {
		s.udpFlows = make(map[string]struct{})
	}
	if _, exists := s.udpFlows[key]; exists {
		return false
	}
	s.udpFlows[key] = struct{}{}
	return true
}

func (s *Service) untrackUDPFlow(key string) {
	s.udpMu.Lock()
	defer s.udpMu.Unlock()
	delete(s.udpFlows, key)
}

func (s *Service) udpIdleTimeout() time.Duration {
	if s.cfg.UDPIdleTimeout > 0 {
		return s.cfg.UDPIdleTimeout
	}
	return DefaultUDPIdleTimeout
}

// proxyUDP forwards one connected gVisor UDP flow through the route's packet
// transport. A route that cannot carry UDP fails the flow the same way a TCP
// route lookup failure does: the error is returned, the route is closed, and
// the session ends.
func (s *Service) proxyUDP(conn net.Conn, id stack.TransportEndpointID) error {
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
	packet, target, err := dialUDPPacket(s.ctx, route, network, destination.String())
	if err != nil {
		return fmt.Errorf("route %s: %w", destination, err)
	}
	defer utils.Close(packet)

	sessionCtx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	go func() {
		<-sessionCtx.Done()
		_ = conn.Close()
		_ = packet.Close()
	}()

	var activity atomic.Int64
	activity.Store(time.Now().UnixNano())
	errc := make(chan error, 2)
	go func() {
		errc <- s.pumpUDP(sessionCtx, &activity, func(buf []byte, deadline time.Time) (int, error) {
			_ = conn.SetReadDeadline(deadline)
			return conn.Read(buf)
		}, func(payload []byte) error {
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
		errc <- s.pumpUDP(sessionCtx, &activity, func(buf []byte, deadline time.Time) (int, error) {
			_ = packet.SetReadDeadline(deadline)
			n, _, readErr := packet.ReadFrom(buf)
			return n, readErr
		}, func(payload []byte) error {
			_ = conn.SetWriteDeadline(time.Now().Add(s.udpIdleTimeout()))
			return writeAll(conn, payload)
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
	activity *atomic.Int64,
	read func([]byte, time.Time) (int, error),
	write func([]byte) error,
) error {
	buf := make([]byte, udpDatagramSize)
	timeout := s.udpIdleTimeout()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		deadline := time.Unix(0, activity.Load()).Add(timeout)
		n, err := read(buf, deadline)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if isTimeout(err) {
				if time.Now().Before(time.Unix(0, activity.Load()).Add(timeout)) {
					continue
				}
				return nil
			}
			return err
		}
		activity.Store(time.Now().UnixNano())
		if err := write(buf[:n]); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if isTimeout(err) && !time.Now().Before(time.Unix(0, activity.Load()).Add(timeout)) {
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
