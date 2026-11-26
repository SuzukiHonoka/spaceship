package socks

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

// eimSessionEntry tracks one RFC 4787 Endpoint-Independent Mapping session
// shared across all destinations for a (clientAddr, egress, family) tuple.
type eimSessionEntry struct {
	session   transport.MultiPacketConn
	route     transport.Transport
	limiter   *udpResourceLimiter
	clientKey string
	lastSeen  atomic.Int64
	closeOnce sync.Once
}

func (e *eimSessionEntry) Close() {
	e.closeOnce.Do(func() {
		if e.session != nil {
			_ = e.session.Close()
		}
		if e.route != nil {
			_ = e.route.Close()
		}
		e.limiter.release(e.clientKey)
	})
}

type eimSessionTable struct {
	mu      sync.RWMutex
	entries map[string]*eimSessionEntry
}

func (t *eimSessionTable) Load(key string) (*eimSessionEntry, bool) {
	t.mu.RLock()
	entry, ok := t.entries[key]
	t.mu.RUnlock()
	return entry, ok
}

func (t *eimSessionTable) Install(key string, entry *eimSessionEntry, limit int) (actual, evicted *eimSessionEntry, installed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = make(map[string]*eimSessionEntry)
	}
	if cur, ok := t.entries[key]; ok {
		return cur, nil, false
	}
	if limit > 0 && len(t.entries) >= limit {
		var oldestKey string
		var oldest *eimSessionEntry
		for k, v := range t.entries {
			if oldest == nil || v.lastSeen.Load() < oldest.lastSeen.Load() {
				oldestKey = k
				oldest = v
			}
		}
		if oldest != nil {
			delete(t.entries, oldestKey)
			evicted = oldest
		}
	}
	t.entries[key] = entry
	return entry, evicted, true
}

func (t *eimSessionTable) DeleteIf(key string, expected *eimSessionEntry) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries[key] != expected {
		return false
	}
	delete(t.entries, key)
	return true
}

func (t *eimSessionTable) Drain() []*eimSessionEntry {
	t.mu.Lock()
	entries := make([]*eimSessionEntry, 0, len(t.entries))
	for _, entry := range t.entries {
		entries = append(entries, entry)
	}
	t.entries = nil
	t.mu.Unlock()
	return entries
}

func packetTargetFromAddrSpec(spec *AddrSpec) (target transport.PacketTarget, family string, ok bool) {
	if spec == nil || spec.Port == 0 {
		return transport.PacketTarget{}, "", false
	}
	if len(spec.IP) > 0 {
		addr, parsed := netip.AddrFromSlice(spec.IP)
		if !parsed {
			return transport.PacketTarget{}, "", false
		}
		addr = addr.Unmap()
		if addr.Is4() {
			family = "udp4"
		} else {
			family = "udp6"
		}
		return transport.PacketTarget{
			Addr: netip.AddrPortFrom(addr, spec.Port),
			Port: spec.Port,
		}, family, true
	}
	if spec.FQDN != "" {
		return transport.PacketTarget{
			Host: spec.FQDN,
			Port: spec.Port,
		}, "udp4", true
	}
	return transport.PacketTarget{}, "", false
}

// tryHandlePacketEIM forwards a SOCKS5 UDP datagram through an EIM
// MultiPacketConn when the resolved route supports PacketSessionDialer.
// It returns handled=false when the caller should fall back to legacy 4-tuple NAT.
func (r *UDPRelay) tryHandlePacketEIM(header *UDPHeader, payload []byte, clientAddr net.Addr) (handled bool) {
	dst, family, ok := packetTargetFromAddrSpec(header.Addr)
	if !ok {
		return false
	}
	select {
	case <-r.done:
		return true
	default:
	}

	host := header.Addr.FQDN
	if host == "" && dst.Addr.IsValid() {
		host = dst.Addr.Addr().String()
	}
	getRoute := r.getRoute
	if getRoute == nil {
		getRoute = router.GetRoute
	}
	route, err := getRoute(host)
	if err != nil {
		// Let the legacy path log/handle route errors identically.
		return false
	}
	sessionDialer, supportsEIM := route.(transport.PacketSessionDialer)
	if !supportsEIM {
		_ = route.Close()
		return false
	}

	key := fmt.Sprintf("eim|%s|%s|%s", clientAddr.String(), route.String(), family)
	entry, ok := r.eimTable.Load(key)
	if ok {
		_ = route.Close()
	} else {
		var dialErr error
		entry, dialErr = r.getOrCreateEIMSession(key, route, sessionDialer, family, clientAddr)
		if dialErr != nil {
			if errors.Is(dialErr, transport.ErrPacketSessionUnsupported) {
				return false
			}
			log.Printf("socks5: udp eim session %s failed: %v", key, dialErr)
			return true
		}
	}

	if err := entry.session.WritePacketTo(payload, dst); err != nil {
		log.Printf("socks5: udp eim write to %s: %v", header.Addr.Address(), err)
		return true
	}
	entry.lastSeen.Store(time.Now().UnixNano())
	if len(payload) > 0 {
		transport.GlobalStats.AddTx(uint64(len(payload)))
	}
	return true
}

func (r *UDPRelay) getOrCreateEIMSession(
	key string,
	route transport.Transport,
	dialer transport.PacketSessionDialer,
	family string,
	clientAddr net.Addr,
) (*eimSessionEntry, error) {
	adoptedRoute := false
	defer func() {
		if !adoptedRoute {
			_ = route.Close()
		}
	}()

	res, err, _ := r.dialGroup.Do(key, func() (any, error) {
		if existing, ok := r.eimTable.Load(key); ok {
			return existing, nil
		}

		limiter := r.natLimiter
		if limiter == nil {
			_, limiter, _ = udpLimiters()
		}
		if !limiter.acquire(r.clientKey) {
			return nil, ErrUDPNATLimit
		}

		ctx := r.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		sess, err := dialer.DialPacketSession(ctx, family, transport.PacketFilteringEIF)
		if err != nil {
			limiter.release(r.clientKey)
			return nil, err
		}

		adoptedRoute = true
		entry := &eimSessionEntry{
			session:   sess,
			route:     route,
			limiter:   limiter,
			clientKey: r.clientKey,
		}
		entry.lastSeen.Store(time.Now().UnixNano())

		limit := orDefault(r.maxNATEntries, defaultMaxNATEntries)
		actual, evicted, installed := r.eimTable.Install(key, entry, limit)
		if evicted != nil {
			evicted.Close()
		}
		if !installed {
			entry.Close()
			return actual, nil
		}

		select {
		case <-r.done:
			r.eimTable.DeleteIf(key, entry)
			entry.Close()
			return nil, net.ErrClosed
		default:
		}

		go r.reverseEIMRelay(entry, key, clientAddr)
		return entry, nil
	})
	if err != nil {
		return nil, err
	}
	return res.(*eimSessionEntry), nil
}

func (r *UDPRelay) reverseEIMRelay(entry *eimSessionEntry, key string, clientAddr net.Addr) {
	defer func() {
		entry.Close()
		r.eimTable.DeleteIf(key, entry)
	}()

	for {
		_ = entry.session.SetReadDeadline(time.Now().Add(udpIdleTimeout))
		payload, peer, err := entry.session.ReadPacketFrom()
		if err != nil {
			select {
			case <-r.done:
				return
			default:
			}
			if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
				if cur, ok := r.eimTable.Load(key); ok && cur == entry {
					if time.Since(time.Unix(0, cur.lastSeen.Load())) < udpIdleTimeout {
						continue
					}
				}
				return
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("socks5: udp eim reverse relay %s: %v", key, err)
			return
		}

		if !peer.IsValid() {
			continue
		}
		spec := &AddrSpec{
			IP:   net.IP(peer.Addr().Unmap().AsSlice()),
			Port: peer.Port(),
		}
		hdr, err := MarshalUDPHeader(spec)
		if err != nil || len(hdr)+len(payload) > udpMaxPacketSize {
			continue
		}
		if cur, ok := r.eimTable.Load(key); ok && cur == entry {
			cur.lastSeen.Store(time.Now().UnixNano())
		}

		frame := make([]byte, len(hdr)+len(payload))
		copy(frame, hdr)
		copy(frame[len(hdr):], payload)
		nw, err := r.relay.WriteTo(frame, clientAddr)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("socks5: udp eim reverse relay write: %v", err)
			continue
		}
		if nw > 0 {
			transport.GlobalStats.AddRx(uint64(nw))
		}
	}
}
