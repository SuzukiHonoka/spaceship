package transport

import (
	"net/netip"
	"testing"
)

func TestPeerFilter_EIFAllowsAnyPeer(t *testing.T) {
	f := NewPeerFilter(PacketFilteringEIF, 4)
	if !f.PermitInbound(netip.MustParseAddrPort("203.0.113.9:9999")) {
		t.Fatal("EIF must allow unsolicited peers")
	}
	if !f.PermitInbound(netip.MustParseAddrPort("[2001:db8::1]:443")) {
		t.Fatal("EIF must allow unsolicited IPv6 peers")
	}
}

func TestPeerFilter_ADAllowsSameIPAnyPortAndEvictsLRU(t *testing.T) {
	f := NewPeerFilter(PacketFilteringAD, 2)
	p1 := netip.MustParseAddrPort("198.51.100.1:4000")
	p2 := netip.MustParseAddrPort("198.51.100.2:4000")
	p3 := netip.MustParseAddrPort("198.51.100.3:4000")

	f.AllowOutbound(p1)
	f.AllowOutbound(p2)

	// Same IP, different port must be allowed under AD.
	if !f.PermitInbound(netip.MustParseAddrPort("198.51.100.1:5555")) {
		t.Fatal("AD should permit same IP on a different port")
	}
	// p1 was refreshed by PermitInbound, so adding p3 evicts p2, not p1.
	f.AllowOutbound(p3)
	if !f.PermitInbound(netip.MustParseAddrPort("198.51.100.1:6666")) {
		t.Fatal("AD should keep recently refreshed p1")
	}
	if f.PermitInbound(p2) {
		t.Fatal("AD should have evicted LRU p2")
	}
	if !f.PermitInbound(p3) {
		t.Fatal("AD should allow newly added p3")
	}
}

func TestPeerFilter_APDRequiresExactIPAndPort(t *testing.T) {
	f := NewPeerFilter(PacketFilteringAPD, 2)
	p1 := netip.MustParseAddrPort("198.51.100.1:4000")
	p1OtherPort := netip.MustParseAddrPort("198.51.100.1:4001")
	p2 := netip.MustParseAddrPort("198.51.100.2:5000")
	p3 := netip.MustParseAddrPort("198.51.100.3:6000")

	f.AllowOutbound(p1)
	if !f.PermitInbound(p1) {
		t.Fatal("APD should permit exact contacted peer")
	}
	if f.PermitInbound(p1OtherPort) {
		t.Fatal("APD must reject different port on same IP")
	}
	f.AllowOutbound(p2)
	f.AllowOutbound(p3)
	if f.PermitInbound(p1) {
		t.Fatal("APD should evict oldest peer p1")
	}
	if !f.PermitInbound(p2) || !f.PermitInbound(p3) {
		t.Fatal("APD should retain p2 and p3")
	}
}
