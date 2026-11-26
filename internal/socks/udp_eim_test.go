package socks

import (
	"net"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/direct"
)

// TestUDPRelay_FullConeEIM verifies that SOCKS5 UDP ASSOCIATE preserves one
// external mapped socket across peers and delivers unsolicited replies from a
// second peer back to the client tagged with that peer's true IP:port.
func TestUDPRelay_FullConeEIM(t *testing.T) {
	relay, err := NewUDPRelay(net.IPv4(127, 0, 0, 1), net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatalf("NewUDPRelay: %v", err)
	}
	relay.getRoute = func(string) (transport.Transport, error) {
		return direct.New(), nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = relay.Run()
	}()
	defer func() {
		_ = relay.Close()
		<-done
	}()

	clientSock, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen client: %v", err)
	}
	defer func() { _ = clientSock.Close() }()

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

	peerAAddr := peerA.LocalAddr().(*net.UDPAddr)
	hdr, err := MarshalUDPHeader(&AddrSpec{IP: peerAAddr.IP, Port: uint16(peerAAddr.Port)})
	if err != nil {
		t.Fatalf("MarshalUDPHeader: %v", err)
	}
	pkt := append(hdr, []byte("probe-a")...)
	if _, err := clientSock.WriteTo(pkt, relay.RelayAddr()); err != nil {
		t.Fatalf("client write: %v", err)
	}

	buf := make([]byte, 256)
	_ = peerA.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, mappedAddr, err := peerA.ReadFromUDPAddrPort(buf)
	if err != nil || string(buf[:n]) != "probe-a" {
		t.Fatalf("peerA read: n=%d payload=%q err=%v", n, string(buf[:n]), err)
	}

	// Peer B (never contacted) replies to the mapped port.
	if _, err := peerB.WriteToUDPAddrPort([]byte("fullcone-b"), mappedAddr); err != nil {
		t.Fatalf("peerB write: %v", err)
	}

	_ = clientSock.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err = clientSock.ReadFrom(buf)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	respHdr, err := ParseUDPHeader(buf[:n])
	if err != nil {
		t.Fatalf("ParseUDPHeader: %v", err)
	}
	if string(buf[respHdr.DataOffset:n]) != "fullcone-b" {
		t.Fatalf("payload = %q, want fullcone-b", string(buf[respHdr.DataOffset:n]))
	}
	peerBAddr := peerB.LocalAddr().(*net.UDPAddr)
	if !respHdr.Addr.IP.Equal(peerBAddr.IP) || int(respHdr.Addr.Port) != peerBAddr.Port {
		t.Fatalf("response addr = %s:%d, want %s:%d", respHdr.Addr.IP, respHdr.Addr.Port, peerBAddr.IP, peerBAddr.Port)
	}
}
