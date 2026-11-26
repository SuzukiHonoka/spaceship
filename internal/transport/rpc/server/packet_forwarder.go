package server

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/direct"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/forward"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	config "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	defaultPacketSessionIdleTimeout = 2 * time.Minute
	packetRouteCacheCapacity        = 256
	packetHostResolveCacheTTL       = 30 * time.Second
	packetPeerFilterCapacity        = 1024
	maxForwardedUDPTargets          = 64
)

type packetRouteDecision uint8

const (
	packetRouteDeny packetRouteDecision = iota
	packetRouteDirect
	packetRouteForward
)

// ProxyPacket serves a multiplexed unconnected UDP session (RFC 4787 EIM).
// One unconnected *net.UDPConn is owned per stream so the external mapped
// IP:port stays constant across remote peers.
func (s *Server) ProxyPacket(stream proto.Proxy_ProxyPacketServer) error {
	if stream == nil {
		return status.Error(codes.InvalidArgument, "proxy packet: nil stream")
	}

	globalProxySessionCounters.requests.Add(1)
	userID, _ := rpc.UserIDFromContext(stream.Context())
	release, rejection := s.proxyAdmission.acquire(userID)
	if rejection != admissionAllowed {
		recordProxyAdmissionRejection(rejection)
		return status.Error(codes.ResourceExhausted, "proxy packet: session capacity exhausted")
	}
	defer release()
	globalProxySessionCounters.admitted.Add(1)
	globalProxySessionCounters.active.Add(1)
	defer globalProxySessionCounters.active.Add(-1)

	serverCtx := s.Ctx
	if serverCtx == nil {
		serverCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(stream.Context())
	stopServerCancel := context.AfterFunc(serverCtx, cancel)
	defer stopServerCancel()
	defer cancel()

	if err := stream.SendHeader(admittedHeaderMD); err != nil {
		return err
	}

	handshakeTimeout := s.proxyHandshakeTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = config.DefaultProxyHandshakeTimeout
	}
	initFrame, err := receivePacketInitFrame(ctx, stream, handshakeTimeout)
	if err != nil {
		if errors.Is(err, errProxyFirstMessageTimeout) {
			globalProxySessionCounters.handshakeTimeouts.Add(1)
			return status.Error(codes.DeadlineExceeded, "proxy packet: init timeout")
		}
		return err
	}

	family, err := normalizePacketFamily(initFrame.GetInit().GetFamily())
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if family == "udp6" && transport.PreferIPv4() {
		return status.Error(codes.FailedPrecondition, "proxy packet: IPv6 is disabled on server")
	}

	filtering, err := protoPacketFiltering(initFrame.GetInit().GetFiltering())
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	udpConn, err := net.ListenUDP(family, nil)
	if err != nil {
		return status.Errorf(codes.Internal, "proxy packet: listen %s: %v", family, err)
	}
	var closeOnce sync.Once
	closeUDP := func() {
		closeOnce.Do(func() { _ = udpConn.Close() })
	}
	defer closeUDP()

	localIP, localPort := udpAddrParts(udpConn.LocalAddr())
	if err := stream.Send(&proto.PacketFrame{
		Status: proto.PacketStatus_PacketAccepted,
		Ip:     localIP,
		Port:   uint32(localPort),
	}); err != nil {
		return err
	}

	idleTimeout := defaultPacketSessionIdleTimeout
	if configured := transport.GetIdleTimeout(); configured > 0 && configured < idleTimeout {
		idleTimeout = configured
	}

	pf := &packetForwarder{
		stream:      stream,
		conn:        udpConn,
		family:      family,
		filter:      transport.NewPeerFilter(filtering, packetPeerFilterCapacity),
		idleTimeout: idleTimeout,
		routeCache:  newPacketRouteCache(packetRouteCacheCapacity),
		resolveHost: resolvePacketHost,
		fwdConns:    make(map[netip.AddrPort]net.Conn),
	}
	pf.touch()

	err = pf.run(ctx, closeUDP)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		if ev, ok := status.FromError(err); ok && ev.Code() == codes.Canceled {
			return nil
		}
		log.Printf("rpc: proxy packet [%s] ended: %v", family, err)
	}
	return nil
}

func receivePacketInitFrame(
	ctx context.Context,
	stream proto.Proxy_ProxyPacketServer,
	timeout time.Duration,
) (*proto.PacketFrame, error) {
	type result struct {
		msg *proto.PacketFrame
		err error
	}
	ch := make(chan result, 1)
	go func() {
		msg, err := stream.Recv()
		ch <- result{msg: msg, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.msg, r.err
	case <-timer.C:
		return nil, errProxyFirstMessageTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func normalizePacketFamily(raw string) (string, error) {
	switch raw {
	case "", "udp4":
		return "udp4", nil
	case "udp6":
		return "udp6", nil
	default:
		return "", fmt.Errorf("proxy packet: unsupported family %q", raw)
	}
}

func protoPacketFiltering(raw proto.PacketFiltering) (transport.PacketFiltering, error) {
	switch raw {
	case proto.PacketFiltering_PacketFilteringEIF:
		return transport.PacketFilteringEIF, nil
	case proto.PacketFiltering_PacketFilteringAD:
		return transport.PacketFilteringAD, nil
	case proto.PacketFiltering_PacketFilteringAPD:
		return transport.PacketFilteringAPD, nil
	default:
		return 0, fmt.Errorf("proxy packet: unsupported filtering %d", raw)
	}
}

func udpAddrParts(addr net.Addr) ([]byte, uint16) {
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok || udpAddr == nil {
		return nil, 0
	}
	ap := udpAddr.AddrPort()
	if !ap.IsValid() {
		return nil, 0
	}
	ip := ap.Addr().Unmap()
	return ip.AsSlice(), ap.Port()
}

type packetForwarder struct {
	stream      proto.Proxy_ProxyPacketServer
	sendMu      sync.Mutex
	conn        *net.UDPConn
	family      string
	filter      *transport.PeerFilter
	idleTimeout time.Duration
	lastActive  atomic.Int64
	routeCache  *packetRouteCache
	resolveHost func(ctx context.Context, family, host string) (netip.Addr, error)

	fwdMu     sync.Mutex
	fwdClosed bool
	fwdConns  map[netip.AddrPort]net.Conn
	fwdWG     sync.WaitGroup
}

func (p *packetForwarder) touch() {
	p.lastActive.Store(time.Now().UnixNano())
}

func (p *packetForwarder) sendFrame(frame *proto.PacketFrame) error {
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	return p.stream.Send(frame)
}

func (p *packetForwarder) closeForwarded() {
	p.fwdMu.Lock()
	p.fwdClosed = true
	conns := make([]net.Conn, 0, len(p.fwdConns))
	for dst, c := range p.fwdConns {
		conns = append(conns, c)
		delete(p.fwdConns, dst)
	}
	p.fwdMu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (p *packetForwarder) run(ctx context.Context, closeUDP func()) error {
	eg, runCtx := errgroup.WithContext(ctx)
	stopClose := context.AfterFunc(runCtx, func() {
		closeUDP()
		p.closeForwarded()
	})
	defer stopClose()

	eg.Go(func() error {
		return p.uploadLoop(runCtx)
	})
	eg.Go(func() error {
		return p.downloadLoop(runCtx)
	})
	err := eg.Wait()
	p.closeForwarded()
	p.fwdWG.Wait()
	return err
}

func (p *packetForwarder) uploadLoop(ctx context.Context) error {
	for {
		frame, err := p.stream.Recv()
		if err != nil {
			return err
		}
		if frame.GetStatus() != proto.PacketStatus_PacketSession {
			return transport.ErrInvalidMessage
		}
		payload := frame.GetPayload()
		if len(payload) > maxUDPPacketSize {
			return errors.New("proxy packet: datagram exceeds max UDP payload size")
		}
		port := frame.GetPort()
		if port == 0 || port > 65535 {
			continue
		}

		dst, decision, err := p.resolveDestination(ctx, frame.GetIp(), frame.GetHost(), uint16(port))
		if err != nil || decision == packetRouteDeny {
			continue
		}

		if decision == packetRouteForward {
			p.touch()
			if writeErr := p.writeForwarded(ctx, dst, payload); writeErr != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}

		p.filter.AllowOutbound(dst)
		p.touch()
		if _, err := p.conn.WriteToUDPAddrPort(payload, dst); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Transient ICMP/network errors on an unconnected UDP socket must
			// not tear down the whole multiplexed session.
			if isFatalSocketError(err) {
				return err
			}
		}
	}
}

func (p *packetForwarder) writeForwarded(ctx context.Context, dst netip.AddrPort, payload []byte) error {
	p.fwdMu.Lock()
	if p.fwdClosed {
		p.fwdMu.Unlock()
		return net.ErrClosed
	}
	conn, exists := p.fwdConns[dst]
	if !exists && len(p.fwdConns) >= maxForwardedUDPTargets {
		p.fwdMu.Unlock()
		return nil
	}
	p.fwdMu.Unlock()

	if !exists {
		fwd, ok := forward.New().(*forward.Forward)
		if !ok {
			return nil
		}
		dialed, err := fwd.DialContext(ctx, transport.DialNetwork("udp"), dst.String())
		if err != nil {
			return err
		}
		p.fwdMu.Lock()
		if p.fwdClosed {
			p.fwdMu.Unlock()
			_ = dialed.Close()
			return net.ErrClosed
		}
		if existing, race := p.fwdConns[dst]; race {
			p.fwdMu.Unlock()
			_ = dialed.Close()
			conn = existing
		} else {
			p.fwdConns[dst] = dialed
			conn = dialed
			p.fwdWG.Add(1)
			go p.readForwardedLoop(ctx, dst, dialed)
			p.fwdMu.Unlock()
		}
	}

	_ = conn.SetWriteDeadline(time.Now().Add(p.idleTimeout))
	_, err := conn.Write(payload)
	if err != nil {
		p.removeForwarded(dst, conn)
	}
	return err
}

func (p *packetForwarder) removeForwarded(dst netip.AddrPort, conn net.Conn) {
	p.fwdMu.Lock()
	if p.fwdConns[dst] == conn {
		delete(p.fwdConns, dst)
	}
	p.fwdMu.Unlock()
	_ = conn.Close()
}

func (p *packetForwarder) readForwardedLoop(ctx context.Context, dst netip.AddrPort, conn net.Conn) {
	defer p.fwdWG.Done()
	defer p.removeForwarded(dst, conn)

	buf := make([]byte, maxUDPPacketSize)
	for {
		if ctx.Err() != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(p.idleTimeout))
		n, err := conn.Read(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				if time.Now().Before(time.Unix(0, p.lastActive.Load()).Add(p.idleTimeout)) {
					continue
				}
			}
			return
		}
		p.touch()
		if err := p.sendFrame(&proto.PacketFrame{
			Status:  proto.PacketStatus_PacketSession,
			Ip:      dst.Addr().AsSlice(),
			Port:    uint32(dst.Port()),
			Payload: buf[:n],
		}); err != nil {
			return
		}
	}
}

func (p *packetForwarder) downloadLoop(ctx context.Context) error {
	buf := make([]byte, maxUDPPacketSize)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		deadline := time.Unix(0, p.lastActive.Load()).Add(p.idleTimeout)
		_ = p.conn.SetReadDeadline(deadline)
		n, peer, err := p.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				if time.Now().Before(time.Unix(0, p.lastActive.Load()).Add(p.idleTimeout)) {
					continue
				}
				return io.EOF
			}
			if errors.Is(err, net.ErrClosed) {
				return io.EOF
			}
			return err
		}

		peer = netip.AddrPortFrom(peer.Addr().Unmap(), peer.Port())
		if !p.filter.PermitInbound(peer) {
			continue
		}
		p.touch()
		if err := p.sendFrame(&proto.PacketFrame{
			Status:  proto.PacketStatus_PacketSession,
			Ip:      peer.Addr().AsSlice(),
			Port:    uint32(peer.Port()),
			Payload: buf[:n],
		}); err != nil {
			return err
		}
	}
}

func (p *packetForwarder) resolveDestination(
	ctx context.Context,
	rawIP []byte,
	host string,
	port uint16,
) (netip.AddrPort, packetRouteDecision, error) {
	if len(rawIP) > 0 {
		addr, ok := netip.AddrFromSlice(rawIP)
		if !ok {
			return netip.AddrPort{}, packetRouteDeny, errors.New("invalid IP bytes")
		}
		addr = addr.Unmap()
		if !p.addrAllowedForFamily(addr) {
			return netip.AddrPort{}, packetRouteDeny, nil
		}
		decision := p.checkRoute(addr.String())
		if decision == packetRouteDeny {
			return netip.AddrPort{}, packetRouteDeny, nil
		}
		return netip.AddrPortFrom(addr, port), decision, nil
	}

	if host == "" || len(host) > maxProxyTargetLength || containsControlOrSpace(host) {
		return netip.AddrPort{}, packetRouteDeny, errors.New("invalid host")
	}
	hostDecision := p.checkRoute(host)
	if hostDecision == packetRouteDeny {
		return netip.AddrPort{}, packetRouteDeny, nil
	}
	addr, ok := p.routeCache.getHostIP(host)
	if !ok {
		resolved, err := p.resolveHost(ctx, p.family, host)
		if err != nil || !p.addrAllowedForFamily(resolved) {
			return netip.AddrPort{}, packetRouteDeny, err
		}
		ipDecision := p.checkRoute(resolved.String())
		if ipDecision == packetRouteDeny {
			return netip.AddrPort{}, packetRouteDeny, nil
		}
		if ipDecision == packetRouteForward {
			hostDecision = packetRouteForward
		}
		p.routeCache.setHostIP(host, resolved)
		addr = resolved
	} else if ipDecision := p.checkRoute(addr.String()); ipDecision == packetRouteDeny {
		return netip.AddrPort{}, packetRouteDeny, nil
	} else if ipDecision == packetRouteForward {
		hostDecision = packetRouteForward
	}
	return netip.AddrPortFrom(addr, port), hostDecision, nil
}

func (p *packetForwarder) addrAllowedForFamily(addr netip.Addr) bool {
	if !addr.IsValid() || addr.IsUnspecified() || addr.IsMulticast() {
		return false
	}
	if p.family == "udp4" && !addr.Is4() {
		return false
	}
	if p.family == "udp6" && !addr.Is6() {
		return false
	}
	if addr.Is6() && transport.PreferIPv4() {
		return false
	}
	return true
}

func (p *packetForwarder) checkRoute(key string) packetRouteDecision {
	if decision, hit := p.routeCache.getRoute(key); hit {
		return decision
	}
	route, err := router.GetRoute(key)
	decision := packetRouteDeny
	if err == nil && route != nil {
		switch route.(type) {
		case *direct.Direct:
			decision = packetRouteDirect
		case *forward.Forward:
			decision = packetRouteForward
		}
		_ = route.Close()
	}
	p.routeCache.setRoute(key, decision)
	return decision
}

func resolvePacketHost(ctx context.Context, family, host string) (netip.Addr, error) {
	ipNet := "ip4"
	if family == "udp6" {
		ipNet = "ip6"
	}
	addrs, err := transport.OutboundResolver().LookupNetIP(ctx, ipNet, host)
	if err != nil || len(addrs) == 0 {
		if err == nil {
			err = errors.New("no IP address resolved")
		}
		return netip.Addr{}, err
	}
	return addrs[0].Unmap(), nil
}

func isFatalSocketError(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF)
}

type packetRouteCache struct {
	capacity int
	routes   map[string]*list.Element
	routeLRU *list.List
	hosts    map[string]*list.Element
	hostLRU  *list.List
}

type routeCacheItem struct {
	key      string
	decision packetRouteDecision
}

type hostCacheItem struct {
	host    string
	addr    netip.Addr
	expires time.Time
}

func newPacketRouteCache(capacity int) *packetRouteCache {
	return &packetRouteCache{
		capacity: capacity,
		routes:   make(map[string]*list.Element),
		routeLRU: list.New(),
		hosts:    make(map[string]*list.Element),
		hostLRU:  list.New(),
	}
}

func (c *packetRouteCache) getRoute(key string) (packetRouteDecision, bool) {
	elem, ok := c.routes[key]
	if !ok {
		return packetRouteDeny, false
	}
	c.routeLRU.MoveToFront(elem)
	return elem.Value.(routeCacheItem).decision, true
}

func (c *packetRouteCache) setRoute(key string, decision packetRouteDecision) {
	if elem, ok := c.routes[key]; ok {
		elem.Value = routeCacheItem{key: key, decision: decision}
		c.routeLRU.MoveToFront(elem)
		return
	}
	if c.routeLRU.Len() >= c.capacity {
		if back := c.routeLRU.Back(); back != nil {
			delete(c.routes, back.Value.(routeCacheItem).key)
			c.routeLRU.Remove(back)
		}
	}
	c.routes[key] = c.routeLRU.PushFront(routeCacheItem{key: key, decision: decision})
}

func (c *packetRouteCache) getHostIP(host string) (netip.Addr, bool) {
	elem, ok := c.hosts[host]
	if !ok {
		return netip.Addr{}, false
	}
	item := elem.Value.(hostCacheItem)
	if time.Now().After(item.expires) {
		delete(c.hosts, host)
		c.hostLRU.Remove(elem)
		return netip.Addr{}, false
	}
	c.hostLRU.MoveToFront(elem)
	return item.addr, true
}

func (c *packetRouteCache) setHostIP(host string, addr netip.Addr) {
	item := hostCacheItem{
		host:    host,
		addr:    addr,
		expires: time.Now().Add(packetHostResolveCacheTTL),
	}
	if elem, ok := c.hosts[host]; ok {
		elem.Value = item
		c.hostLRU.MoveToFront(elem)
		return
	}
	if c.hostLRU.Len() >= c.capacity {
		if back := c.hostLRU.Back(); back != nil {
			delete(c.hosts, back.Value.(hostCacheItem).host)
			c.hostLRU.Remove(back)
		}
	}
	c.hosts[host] = c.hostLRU.PushFront(item)
}
