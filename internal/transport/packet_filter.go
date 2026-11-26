package transport

import (
	"container/list"
	"net/netip"
	"sync"
)

const defaultPeerFilterCapacity = 1024

// PeerFilter enforces RFC 4787 NAT filtering (EIF, AD, APD) for a single
// unconnected UDP session using a bounded O(1) LRU allowlist.
type PeerFilter struct {
	mode     PacketFiltering
	capacity int

	mu       sync.Mutex
	ips      map[netip.Addr]*list.Element
	ipLRU    *list.List
	peers    map[netip.AddrPort]*list.Element
	peerLRU  *list.List
}

// NewPeerFilter creates a PeerFilter for mode. Capacity defaults to 1024 when
// non-positive.
func NewPeerFilter(mode PacketFiltering, capacity int) *PeerFilter {
	if capacity <= 0 {
		capacity = defaultPeerFilterCapacity
	}
	f := &PeerFilter{
		mode:     mode,
		capacity: capacity,
	}
	switch mode {
	case PacketFilteringAD:
		f.ips = make(map[netip.Addr]*list.Element)
		f.ipLRU = list.New()
	case PacketFilteringAPD:
		f.peers = make(map[netip.AddrPort]*list.Element)
		f.peerLRU = list.New()
	}
	return f
}

// Mode returns the configured filtering mode.
func (f *PeerFilter) Mode() PacketFiltering {
	if f == nil {
		return PacketFilteringEIF
	}
	return f.mode
}

// AllowOutbound records an outbound datagram sent to peer so return traffic
// matching the filtering mode is permitted.
func (f *PeerFilter) AllowOutbound(peer netip.AddrPort) {
	if f == nil || f.mode == PacketFilteringEIF || !peer.IsValid() {
		return
	}
	addr := peer.Addr().Unmap()
	peer = netip.AddrPortFrom(addr, peer.Port())

	f.mu.Lock()
	defer f.mu.Unlock()

	switch f.mode {
	case PacketFilteringAD:
		if elem, ok := f.ips[addr]; ok {
			f.ipLRU.MoveToFront(elem)
			return
		}
		if f.ipLRU.Len() >= f.capacity {
			if back := f.ipLRU.Back(); back != nil {
				delete(f.ips, back.Value.(netip.Addr))
				f.ipLRU.Remove(back)
			}
		}
		f.ips[addr] = f.ipLRU.PushFront(addr)
	case PacketFilteringAPD:
		if elem, ok := f.peers[peer]; ok {
			f.peerLRU.MoveToFront(elem)
			return
		}
		if f.peerLRU.Len() >= f.capacity {
			if back := f.peerLRU.Back(); back != nil {
				delete(f.peers, back.Value.(netip.AddrPort))
				f.peerLRU.Remove(back)
			}
		}
		f.peers[peer] = f.peerLRU.PushFront(peer)
	}
}

// PermitInbound reports whether an inbound datagram from peer passes the
// configured RFC 4787 filter.
func (f *PeerFilter) PermitInbound(peer netip.AddrPort) bool {
	if f == nil || f.mode == PacketFilteringEIF {
		return true
	}
	if !peer.IsValid() {
		return false
	}
	addr := peer.Addr().Unmap()
	peer = netip.AddrPortFrom(addr, peer.Port())

	f.mu.Lock()
	defer f.mu.Unlock()

	switch f.mode {
	case PacketFilteringAD:
		elem, ok := f.ips[addr]
		if ok {
			f.ipLRU.MoveToFront(elem)
		}
		return ok
	case PacketFilteringAPD:
		elem, ok := f.peers[peer]
		if ok {
			f.peerLRU.MoveToFront(elem)
		}
		return ok
	default:
		return true
	}
}
