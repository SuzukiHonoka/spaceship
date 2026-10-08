package testutil

import (
	"net"
	"strconv"
	"testing"
)

func TestFreeLoopbackAddrIsBindableAndNotRepeated(t *testing.T) {
	low, known := ephemeralLow()
	seen := make(map[string]bool)
	for range 64 {
		addr := FreeLoopbackAddr(t)
		if seen[addr] {
			t.Fatalf("%s returned twice", addr)
		}
		seen[addr] = true
		_, portText, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatal(err)
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			t.Fatal(err)
		}
		if known && port >= low {
			t.Fatalf("port %d is inside the ephemeral range starting at %d", port, low)
		}
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("binding %s: %v", addr, err)
		}
		_ = listener.Close()
		// The DNS front end binds the same address as UDP.
		packet, err := net.ListenPacket("udp", addr)
		if err != nil {
			t.Fatalf("binding %s as udp: %v", addr, err)
		}
		_ = packet.Close()
	}
}
