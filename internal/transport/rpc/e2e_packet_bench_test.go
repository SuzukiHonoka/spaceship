package rpc_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

// BenchmarkEndToEnd_UDPPacketSession measures datagram round trips over a
// multiplexed EIM ProxyPacket session (client DialPacketSession -> gRPC
// ProxyPacket stream -> server unconnected UDP socket -> echo and back),
// matching BenchmarkEndToEnd_UDPTunnel's payload sizes and RTT reporting for
// direct apples-to-apples comparison.
func BenchmarkEndToEnd_UDPPacketSession(b *testing.B) {
	quietLogs(b)
	routeAllDirect(b)
	echoAddr := startUDPEcho(b)
	connectClient(b, startProxyServer(b))
	c := benchClient(b)
	targetAP, err := netip.ParseAddrPort(echoAddr)
	if err != nil {
		b.Fatal(err)
	}
	target := transport.PacketTarget{Addr: targetAP, Port: targetAP.Port()}

	for _, size := range []int{64, 512, 1400, 8192} {
		b.Run(fmt.Sprintf("%d", size), func(b *testing.B) {
			sess, err := c.DialPacketSession(context.Background(), "udp4", transport.PacketFilteringEIF)
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = sess.Close() }()

			payload := bytes.Repeat([]byte{0x5a}, size)
			samples := make([]time.Duration, b.N)
			b.SetBytes(int64(size) * 2)
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				start := time.Now()
				if err := sess.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					b.Fatal(err)
				}
				if err := sess.WritePacketTo(payload, target); err != nil {
					b.Fatal(err)
				}
				got, peer, err := sess.ReadPacketFrom()
				if err != nil {
					b.Fatal(err)
				}
				if len(got) != size || peer != targetAP {
					b.Fatalf("echoed %d bytes from %v, want %d from %v", len(got), peer, size, targetAP)
				}
				samples[i] = time.Since(start)
			}
			b.StopTimer()
			reportRTT(b, samples)
		})
	}
}

// BenchmarkEndToEnd_UDPMultiPeer compares sending a single datagram to each of
// 4 distinct peers (mimicking STUN/ICE/WebRTC/gaming discovery) via:
//   - EIM: one reused ProxyPacket stream (zero extra RPC handshakes per peer)
//   - Legacy: one DialPacket stream opened per destination peer
func BenchmarkEndToEnd_UDPMultiPeer(b *testing.B) {
	quietLogs(b)
	routeAllDirect(b)
	const numPeers = 4
	addrs := make([]string, numPeers)
	aps := make([]netip.AddrPort, numPeers)
	udpAddrs := make([]*net.UDPAddr, numPeers)
	for i := range numPeers {
		addrs[i] = startUDPEcho(b)
		ap, err := netip.ParseAddrPort(addrs[i])
		if err != nil {
			b.Fatal(err)
		}
		aps[i] = ap
		ua, err := net.ResolveUDPAddr("udp", addrs[i])
		if err != nil {
			b.Fatal(err)
		}
		udpAddrs[i] = ua
	}
	connectClient(b, startProxyServer(b))
	c := benchClient(b)
	payload := bytes.Repeat([]byte{0x5a}, 512)

	b.Run("EIM_ProxyPacket", func(b *testing.B) {
		sess, err := c.DialPacketSession(context.Background(), "udp4", transport.PacketFilteringEIF)
		if err != nil {
			b.Fatal(err)
		}
		defer func() { _ = sess.Close() }()

		samples := make([]time.Duration, b.N)
		b.SetBytes(int64(len(payload)) * 2 * numPeers)
		b.ReportAllocs()
		b.ResetTimer()
		for i := range b.N {
			start := time.Now()
			for j := range numPeers {
				target := transport.PacketTarget{Addr: aps[j], Port: aps[j].Port()}
				if err := sess.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					b.Fatal(err)
				}
				if err := sess.WritePacketTo(payload, target); err != nil {
					b.Fatal(err)
				}
				got, peer, err := sess.ReadPacketFrom()
				if err != nil {
					b.Fatal(err)
				}
				if len(got) != len(payload) || peer != aps[j] {
					b.Fatalf("peer %d: got %d bytes from %v", j, len(got), peer)
				}
			}
			samples[i] = time.Since(start)
		}
		b.StopTimer()
		reportRTT(b, samples)
	})

	b.Run("Legacy_DialPacketPerPeer", func(b *testing.B) {
		buf := make([]byte, 65535)
		samples := make([]time.Duration, b.N)
		b.SetBytes(int64(len(payload)) * 2 * numPeers)
		b.ReportAllocs()
		b.ResetTimer()
		for i := range b.N {
			start := time.Now()
			for j := range numPeers {
				pc, err := c.DialPacket("udp", addrs[j])
				if err != nil {
					b.Fatal(err)
				}
				if err := pc.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					_ = pc.Close()
					b.Fatal(err)
				}
				if _, err := pc.WriteTo(payload, udpAddrs[j]); err != nil {
					_ = pc.Close()
					b.Fatal(err)
				}
				n, _, err := pc.ReadFrom(buf)
				_ = pc.Close()
				if err != nil {
					b.Fatal(err)
				}
				if n != len(payload) {
					b.Fatalf("peer %d: got %d bytes", j, n)
				}
			}
			samples[i] = time.Since(start)
		}
		b.StopTimer()
		reportRTT(b, samples)
	})
}
