package tun

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/checksum"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

const udpNATSendQueueSize = 64

type udpNATKey struct {
	clientAddr  netip.Addr
	clientPort  uint16
	dstAddr     netip.Addr // set only in AD mode
	familyV6    bool
	egressClass string
}

type udpNATOutbound struct {
	payload []byte
	target  transport.PacketTarget
}

type udpNATSession struct {
	key        udpNATKey
	clientIP   tcpip.Address
	clientPort uint16
	netProto   tcpip.NetworkProtocolNumber
	family     string

	ctx       context.Context
	cancel    context.CancelFunc
	sendCh    chan udpNATOutbound
	activity atomic.Int64
	lruElem  *list.Element
}

func (u *udpNATSession) touch() {
	u.activity.Store(time.Now().UnixNano())
}

// udpNATManager implements packet-level RFC 4787 EIM/AD UDP NAT on top of
// gVisor without creating per-flow gVisor UDP endpoints. Inbound replies from
// any allowed peer are injected back via writeUDPBack with their real peer
// IP:port, enabling true Full Cone (NAT Type 1) and Restricted Cone traversal.
type udpNATManager struct {
	service *Service

	mu             sync.Mutex
	sessions       map[udpNATKey]*udpNATSession
	lru            *list.List
	legacyFallback atomic.Bool
}

func newUDPNATManager(s *Service) *udpNATManager {
	return &udpNATManager{
		service:  s,
		sessions: make(map[udpNATKey]*udpNATSession),
		lru:      list.New(),
	}
}

func (m *udpNATManager) resetAll() {
	m.mu.Lock()
	sessions := make([]*udpNATSession, 0, len(m.sessions))
	for _, sess := range m.sessions {
		sessions = append(sessions, sess)
	}
	m.mu.Unlock()
	for _, sess := range sessions {
		sess.cancel()
	}
}

// handlePacket tries to process a general UDP datagram through EIM/AD NAT.
// It returns (handled, ok):
//   - (true, true): packet was accepted into EIM/AD NAT.
//   - (true, false): packet was rejected/dropped (invalid or out of slots).
//   - (false, false): caller should fall back to legacy 4-tuple UDP forwarding
//     (e.g., APD mode, non-EIM egress, or legacy server).
func (m *udpNATManager) handlePacket(
	id stack.TransportEndpointID,
	pkt *stack.PacketBuffer,
	destination netip.AddrPort,
	target transport.PacketTarget,
	route transport.Transport,
	egressClass string,
) (handled bool, ok bool) {
	if m == nil || m.legacyFallback.Load() {
		return false, false
	}
	sessionDialer, supportsSession := route.(transport.PacketSessionDialer)
	if !supportsSession {
		return false, false
	}
	clientAddr, validClient := netip.AddrFromSlice(id.RemoteAddress.AsSlice())
	if !validClient {
		return true, false
	}
	clientAddr = clientAddr.Unmap()

	familyV6 := destination.Addr().Is6()
	family := "udp4"
	if familyV6 {
		family = "udp6"
	}

	key := udpNATKey{
		clientAddr:  clientAddr,
		clientPort:  id.RemotePort,
		familyV6:    familyV6,
		egressClass: egressClass,
	}
	if m.service.cfg.UDPMapping == UDPMappingAD {
		key.dstAddr = destination.Addr()
	}

	payload := append([]byte(nil), pkt.Data().AsRange().ToSlice()...)
	if !target.Addr.IsValid() && target.Host == "" {
		target = transport.PacketTarget{Addr: destination, Port: destination.Port()}
	}

	sess, created, admitted := m.getOrCreateSession(key, id, pkt.NetworkProtocolNumber, family)
	if !admitted {
		_ = route.Close()
		return true, false
	}
	if !created {
		_ = route.Close()
	} else {
		go m.runSession(sess, route, sessionDialer)
	}

	sess.touch()
	select {
	case sess.sendCh <- udpNATOutbound{payload: payload, target: target}:
	default:
		// Per-session queue full under burst: drop datagram cleanly (UDP semantics).
	}
	return true, true
}

func (m *udpNATManager) getOrCreateSession(
	key udpNATKey,
	id stack.TransportEndpointID,
	netProto tcpip.NetworkProtocolNumber,
	family string,
) (sess *udpNATSession, created bool, admitted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, exists := m.sessions[key]; exists {
		m.lru.MoveToFront(existing.lruElem)
		return existing, false, true
	}

	s := m.service
	if tryAcquireSlot(s.udpSessionSlots) {
		if s.beginFlow() {
			return m.insertSessionLocked(key, id, netProto, family), true, true
		}
		releaseSlot(s.udpSessionSlots)
	}

	// Evict least recently used session from udpNAT (O(1)) and transfer its
	// flowSlots + udpSessionSlots directly to the new session.
	if back := m.lru.Back(); back != nil {
		victim := back.Value.(*udpNATSession)
		if !s.adoptFlow() {
			return nil, false, false
		}
		delete(m.sessions, victim.key)
		m.lru.Remove(back)
		victim.lruElem = nil
		victim.cancel()
		return m.insertSessionLocked(key, id, netProto, family), true, true
	}

	// If udpNAT holds no sessions, try evicting a legacy 4-tuple UDP session.
	s.udpMu.Lock()
	evicted := s.replaceLRUUDPSessionLocked()
	s.udpMu.Unlock()
	if !evicted {
		return nil, false, false
	}
	return m.insertSessionLocked(key, id, netProto, family), true, true
}

func (m *udpNATManager) insertSessionLocked(
	key udpNATKey,
	id stack.TransportEndpointID,
	netProto tcpip.NetworkProtocolNumber,
	family string,
) *udpNATSession {
	ctx, cancel := context.WithCancel(m.service.ctx)
	sess := &udpNATSession{
		key:        key,
		clientIP:   id.RemoteAddress,
		clientPort: id.RemotePort,
		netProto:   netProto,
		family:     family,
		ctx:        ctx,
		cancel:     cancel,
		sendCh:     make(chan udpNATOutbound, udpNATSendQueueSize),
	}
	sess.touch()
	sess.lruElem = m.lru.PushFront(sess)
	m.sessions[key] = sess
	return sess
}

func (m *udpNATManager) finishSession(sess *udpNATSession) {
	sess.cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[sess.key] != sess {
		m.service.flows.Done()
		return
	}
	delete(m.sessions, sess.key)
	if sess.lruElem != nil {
		m.lru.Remove(sess.lruElem)
		sess.lruElem = nil
	}
	releaseSlot(m.service.udpSessionSlots)
	m.service.endFlow()
}

func (m *udpNATManager) runSession(
	sess *udpNATSession,
	route transport.Transport,
	dialer transport.PacketSessionDialer,
) {
	defer m.finishSession(sess)
	defer utils.Close(route)

	filtering := m.service.cfg.UDPFiltering.transportFiltering()
	conn, err := dialer.DialPacketSession(sess.ctx, sess.family, filtering)
	if err != nil {
		if errors.Is(err, transport.ErrPacketSessionUnsupported) {
			m.legacyFallback.Store(true)
			m.relayViaLegacyFallback(sess, route)
			return
		}
		if !errors.Is(err, context.Canceled) {
			log.Printf("tun: UDP NAT session %s (%s) failed: %v", sess.key.egressClass, sess.family, err)
		}
		return
	}
	defer utils.Close(conn)

	sessionCtx, cancel := context.WithCancel(sess.ctx)
	defer cancel()
	stopClose := context.AfterFunc(sessionCtx, func() {
		_ = conn.Close()
	})
	defer stopClose()

	errc := make(chan error, 2)
	// Writer goroutine: TUN -> outbound MultiPacketConn
	go func() {
		timeout := m.service.udpIdleTimeout()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			idleUntil := time.Unix(0, sess.activity.Load()).Add(timeout)
			wait := time.Until(idleUntil)
			if wait <= 0 {
				errc <- nil
				return
			}
			timer.Reset(wait)

			select {
			case <-sessionCtx.Done():
				errc <- sessionCtx.Err()
				return
			case <-timer.C:
				if time.Now().Before(time.Unix(0, sess.activity.Load()).Add(timeout)) {
					continue
				}
				errc <- nil
				return
			case item := <-sess.sendCh:
				sess.touch()
				_ = conn.SetWriteDeadline(time.Now().Add(timeout))
				if writeErr := conn.WritePacketTo(item.payload, item.target); writeErr != nil {
					if sessionCtx.Err() != nil {
						errc <- sessionCtx.Err()
						return
					}
					if isTimeout(writeErr) && !time.Now().Before(time.Unix(0, sess.activity.Load()).Add(timeout)) {
						errc <- nil
						return
					}
					errc <- writeErr
					return
				}
				if len(item.payload) > 0 {
					transport.GlobalStats.AddTx(uint64(len(item.payload)))
				}
			}
		}
	}()

	// Reader goroutine: outbound MultiPacketConn -> TUN (writeUDPBack with true peer IP:port)
	go func() {
		timeout := m.service.udpIdleTimeout()
		for {
			if err := sessionCtx.Err(); err != nil {
				errc <- err
				return
			}
			idleUntil := time.Unix(0, sess.activity.Load()).Add(timeout)
			_ = conn.SetReadDeadline(idleUntil)
			payload, peer, readErr := conn.ReadPacketFrom()
			if readErr != nil {
				if sessionCtx.Err() != nil {
					errc <- sessionCtx.Err()
					return
				}
				if isTimeout(readErr) {
					if time.Now().Before(time.Unix(0, sess.activity.Load()).Add(timeout)) {
						continue
					}
					errc <- nil
					return
				}
				errc <- readErr
				return
			}
			if !peer.IsValid() {
				continue
			}
			sess.touch()
			if len(payload) > 0 {
				transport.GlobalStats.AddRx(uint64(len(payload)))
			}
			peerIP := tcpipAddressFromNetip(peer.Addr().Unmap())
			if err := m.service.writeUDPBack(
				peerIP,
				peer.Port(),
				sess.clientIP,
				sess.clientPort,
				sess.netProto,
				payload,
			); err != nil && sessionCtx.Err() == nil {
				log.Printf("tun: UDP NAT writeback to %s:%d failed: %v", sess.key.clientAddr, sess.clientPort, err)
			}
		}
	}()

	err1 := <-errc
	cancel()
	err2 := <-errc
	if err := udpFlowError(err1, err2); err != nil {
		log.Printf("tun: UDP NAT flow failed: %v", err)
	}
}

// relayViaLegacyFallback forwards the already-queued first datagram(s) over
// legacy per-target PacketDialer when a server responds with Unimplemented to
// ProxyPacket, avoiding losing the initial packet on legacy servers.
func (m *udpNATManager) relayViaLegacyFallback(sess *udpNATSession, route transport.Transport) {
	select {
	case first := <-sess.sendCh:
		targetStr := ""
		if first.target.Addr.IsValid() {
			targetStr = first.target.Addr.String()
		} else if first.target.Host != "" && first.target.Port > 0 {
			targetStr = net.JoinHostPort(first.target.Host, fmt.Sprintf("%d", first.target.Port))
		} else {
			return
		}
		packet, targetAddr, err := dialUDPPacket(sess.ctx, route, sess.family, targetStr)
		if err != nil {
			return
		}
		defer utils.Close(packet)
		_ = packet.SetWriteDeadline(time.Now().Add(m.service.udpIdleTimeout()))
		if _, err := packet.WriteTo(first.payload, targetAddr); err != nil {
			return
		}
		buf := make([]byte, udpDatagramSize)
		_ = packet.SetReadDeadline(time.Now().Add(min(m.service.udpIdleTimeout(), 5*time.Second)))
		n, _, err := packet.ReadFrom(buf)
		if err != nil || !first.target.Addr.IsValid() {
			return
		}
		peerIP := tcpipAddressFromNetip(first.target.Addr.Addr().Unmap())
		_ = m.service.writeUDPBack(peerIP, first.target.Addr.Port(), sess.clientIP, sess.clientPort, sess.netProto, buf[:n])
	default:
	}
}

func tcpipAddressFromNetip(addr netip.Addr) tcpip.Address {
	addr = addr.Unmap()
	if addr.Is4() {
		return tcpip.AddrFrom4(addr.As4())
	}
	return tcpip.AddrFrom16(addr.As16())
}

// writeUDPBack injects a UDP datagram directly into the gVisor stack from
// (srcAddr, srcPort) to (dstAddr, dstPort) without requiring a registered
// gVisor UDP endpoint. This powers both stateless UDP DNS hijacking and
// full-cone (EIM) UDP NAT replies.
func (s *Service) writeUDPBack(
	srcAddr tcpip.Address,
	srcPort uint16,
	dstAddr tcpip.Address,
	dstPort uint16,
	netProto tcpip.NetworkProtocolNumber,
	payload []byte,
) error {
	if s.stack == nil {
		return errors.New("tun: netstack is nil")
	}
	route, tcpErr := s.stack.FindRoute(s.nicID, srcAddr, dstAddr, netProto, false)
	if tcpErr != nil {
		return fmt.Errorf("tun: find route for UDP reply: %s", tcpErr)
	}
	defer route.Release()

	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		ReserveHeaderBytes: header.UDPMinimumSize + int(route.MaxHeaderLength()),
		Payload:            buffer.MakeWithData(payload),
	})
	defer pkt.DecRef()

	pkt.TransportProtocolNumber = header.UDPProtocolNumber
	udpHdr := header.UDP(pkt.TransportHeader().Push(header.UDPMinimumSize))
	pLen := uint16(pkt.Size())
	udpHdr.Encode(&header.UDPFields{
		SrcPort: srcPort,
		DstPort: dstPort,
		Length:  pLen,
	})

	// Compute the UDP checksum for both IPv4 and IPv6 so both kernel TUN peers
	// and in-memory link/pipe stacks accept the packet unconditionally.
	xsum := udpHdr.CalculateChecksum(checksum.Combine(
		route.PseudoHeaderChecksum(header.UDPProtocolNumber, pLen),
		pkt.Data().Checksum(),
	))
	if xsum != math.MaxUint16 {
		xsum = ^xsum
	}
	udpHdr.SetChecksum(xsum)

	if writeErr := route.WritePacket(stack.NetworkHeaderParams{
		Protocol: header.UDPProtocolNumber,
		TTL:      route.DefaultTTL(),
		TOS:      0,
	}, pkt); writeErr != nil {
		return fmt.Errorf("tun: write UDP reply: %s", writeErr)
	}
	return nil
}

// suppress unused import warning if io is not referenced elsewhere in this file.
var _ = io.EOF
