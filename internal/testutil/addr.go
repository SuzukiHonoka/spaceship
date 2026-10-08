// Package testutil holds helpers shared by tests. The application does not
// import it.
package testutil

import (
	"math/rand/v2"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// reservedSpan is how many ports below the kernel's ephemeral range
// FreeLoopbackAddr draws from.
const reservedSpan = 16384

// handedOut holds the ports already returned in this process, so two calls
// made before either caller binds never return the same one.
var handedOut sync.Map

// FreeLoopbackAddr returns a loopback address whose port is free for both TCP
// and UDP now, for code under test that has to bind the address itself.
//
// The address is released before the caller binds it, so something else can
// take it in between. Asking the kernel for port 0 makes that likely: it
// returns an ephemeral port, and every outbound connection made meanwhile, by
// this binary or any other process, draws its source port from the same
// range. Ports below the ephemeral range are only ever taken by an explicit
// bind, so they are drawn from there, starting at a random offset so that
// test binaries running in parallel do not walk the same sequence. Where the
// range is unknown, the kernel's choice is used as before.
//
// Both protocols are probed because callers such as the DNS front end bind
// the returned address as UDP, and a port that is free for TCP says nothing
// about UDP, whose ephemeral ports are drawn by every UDP test in the binary.
func FreeLoopbackAddr(t testing.TB) string {
	t.Helper()
	if low, ok := ephemeralLow(); ok {
		first := max(1024, low-reservedSpan)
		if span := low - first; span > 0 {
			start := rand.IntN(span) // #nosec G404 -- spreads test ports, not security
			for i := range min(128, span) {
				port := first + (start+i)%span
				if _, taken := handedOut.LoadOrStore(port, struct{}{}); taken {
					continue
				}
				if addr, ok := probeLoopbackPort(t, port); ok {
					return addr
				}
			}
		}
	}
	for range 16 {
		probe, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserving a loopback port: %v", err)
		}
		port := probe.Addr().(*net.TCPAddr).Port
		if err := probe.Close(); err != nil {
			t.Fatalf("releasing reserved port: %v", err)
		}
		if _, taken := handedOut.LoadOrStore(port, struct{}{}); taken {
			continue
		}
		if addr, ok := probeLoopbackPort(t, port); ok {
			return addr
		}
	}
	t.Fatal("reserving a loopback port free for both TCP and UDP")
	return ""
}

// probeLoopbackPort reports whether port can be bound on loopback as both TCP
// and UDP right now, returning the address to hand out when it can.
func probeLoopbackPort(t testing.TB, port int) (string, bool) {
	t.Helper()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	tcpProbe, err := net.Listen("tcp", addr)
	if err != nil {
		return "", false
	}
	udpProbe, err := net.ListenPacket("udp", addr)
	if err != nil {
		_ = tcpProbe.Close()
		return "", false
	}
	if err := udpProbe.Close(); err != nil {
		t.Fatalf("releasing reserved port: %v", err)
	}
	if err := tcpProbe.Close(); err != nil {
		t.Fatalf("releasing reserved port: %v", err)
	}
	return addr, true
}

// ephemeralLow returns the first port of Linux's ephemeral range.
func ephemeralLow() (int, bool) {
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return 0, false
	}
	low, err := strconv.Atoi(fields[0])
	if err != nil || low <= 1024 || low > 65535 {
		return 0, false
	}
	return low, true
}
