package direct

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

func TestDirect_PacketSession_FullConeEIF(t *testing.T) {
	d := &Direct{}
	sess, err := d.DialPacketSession(context.Background(), "udp4", transport.PacketFilteringEIF)
	if err != nil {
		t.Fatalf("DialPacketSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	peerA, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen peerA: %v", err)
	}
	defer func() { _ = peerA.Close() }()

	peerB, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen peerB: %v", err)
	}
	defer func() { _ = peerB.Close() }()

	// Client sends to peerA; peerA observes the mapped source port and asks
	// peerB (never contacted by client) to reply to that mapped port.
	if err := sess.WritePacketTo([]byte("hello-a"), transport.PacketTarget{
		Addr: peerA.LocalAddr().(*net.UDPAddr).AddrPort(),
	}); err != nil {
		t.Fatalf("WritePacketTo peerA: %v", err)
	}

	buf := make([]byte, 64)
	_ = peerA.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, mappedAddr, err := peerA.ReadFromUDPAddrPort(buf)
	if err != nil || string(buf[:n]) != "hello-a" {
		t.Fatalf("peerA read: n=%d payload=%q err=%v", n, string(buf[:n]), err)
	}

	// Unsolicited reply from peerB to the same mapped port.
	if _, err := peerB.WriteToUDPAddrPort([]byte("from-b"), mappedAddr); err != nil {
		t.Fatalf("peerB write: %v", err)
	}

	_ = sess.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, from, err := sess.ReadPacketFrom()
	if err != nil {
		t.Fatalf("ReadPacketFrom: %v", err)
	}
	if string(got) != "from-b" {
		t.Fatalf("payload = %q, want from-b", got)
	}
	wantPeerB := peerB.LocalAddr().(*net.UDPAddr).AddrPort()
	if from != wantPeerB {
		t.Fatalf("peer = %s, want %s", from, wantPeerB)
	}
}

func TestDirect_PacketSession_APDFilteringDropsUnsolicitedPort(t *testing.T) {
	d := &Direct{}
	sess, err := d.DialPacketSession(context.Background(), "udp4", transport.PacketFilteringAPD)
	if err != nil {
		t.Fatalf("DialPacketSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	peerA, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen peerA: %v", err)
	}
	defer func() { _ = peerA.Close() }()

	peerB, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen peerB: %v", err)
	}
	defer func() { _ = peerB.Close() }()

	if err := sess.WritePacketTo([]byte("ping"), transport.PacketTarget{
		Addr: peerA.LocalAddr().(*net.UDPAddr).AddrPort(),
	}); err != nil {
		t.Fatalf("WritePacketTo: %v", err)
	}

	buf := make([]byte, 64)
	_ = peerA.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, mappedAddr, err := peerA.ReadFromUDPAddrPort(buf)
	if err != nil {
		t.Fatalf("peerA read: %v", err)
	}

	// PeerB sends first (must be filtered out), then peerA sends (must pass).
	_, _ = peerB.WriteToUDPAddrPort([]byte("spoof"), mappedAddr)
	_, _ = peerA.WriteToUDPAddrPort([]byte("legit"), mappedAddr)

	_ = sess.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, from, err := sess.ReadPacketFrom()
	if err != nil {
		t.Fatalf("ReadPacketFrom: %v", err)
	}
	if string(got) != "legit" || from != peerA.LocalAddr().(*net.UDPAddr).AddrPort() {
		t.Fatalf("got (%q, %s), want (legit, %s)", got, from, peerA.LocalAddr())
	}
}

func TestDirect_PacketSession_RejectsFamilyMismatch(t *testing.T) {
	d := &Direct{}
	sess, err := d.DialPacketSession(context.Background(), "udp4", transport.PacketFilteringEIF)
	if err != nil {
		t.Fatalf("DialPacketSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	err = sess.WritePacketTo([]byte("x"), transport.PacketTarget{
		Addr: netip.MustParseAddrPort("[2001:db8::1]:53"),
	})
	if err == nil {
		t.Fatal("expected error sending IPv6 destination on udp4 session")
	}
}
