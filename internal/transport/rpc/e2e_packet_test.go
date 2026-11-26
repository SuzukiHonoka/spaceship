package rpc_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc"
	rpcClient "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	rpcServer "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/server"
	serverConfig "github.com/SuzukiHonoka/spaceship/v2/pkg/config/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func startPacketTestServer(t *testing.T, uuid string, routes router.Routes, opts ...rpcServer.Option) (string, func()) {
	t.Helper()
	if routes == nil {
		routes = router.Routes{router.CloneRoute(router.RouteServerDefault)}
	}
	if err := router.SetRoutes(routes); err != nil {
		t.Fatalf("SetRoutes: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	srv, err := rpcServer.NewServer(ctx, serverConfig.Users{{UUID: uuid}}, nil, nil, opts...)
	if err != nil {
		cancel()
		t.Fatalf("NewServer: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.ListenAndServe(addr)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	return addr, func() {
		cancel()
		<-done
	}
}

func TestEndToEnd_ProxyPacket_FullConeEIF(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	rpc.SetServiceName("custom.PacketTest")
	t.Cleanup(func() { rpc.SetServiceName("") })

	addr, stopServer := startPacketTestServer(t, uuid, nil)
	defer stopServer()

	rpcClient.SetUUID(uuid)
	if err := rpcClient.Init(addr, "", false, 1, nil); err != nil {
		t.Fatalf("rpcClient.Init: %v", err)
	}
	defer rpcClient.Destroy()

	c, err := rpcClient.New()
	if err != nil {
		t.Fatalf("rpcClient.New: %v", err)
	}
	defer func() { _ = c.Close() }()

	sess, err := c.DialPacketSession(context.Background(), "udp4", transport.PacketFilteringEIF)
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

	// Send to peerA, verify peerA sees server socket address, then have peerB
	// (never contacted) send back to that exact mapped server port.
	if err := sess.WritePacketTo([]byte("to-a"), transport.PacketTarget{
		Addr: peerA.LocalAddr().(*net.UDPAddr).AddrPort(),
	}); err != nil {
		t.Fatalf("WritePacketTo peerA: %v", err)
	}

	buf := make([]byte, 64)
	_ = peerA.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, mappedFromA, err := peerA.ReadFromUDPAddrPort(buf)
	if err != nil || string(buf[:n]) != "to-a" {
		t.Fatalf("peerA read: n=%d payload=%q err=%v", n, string(buf[:n]), err)
	}

	// Send to peerB too and assert EIM: mapped port must be identical!
	if err := sess.WritePacketTo([]byte("to-b"), transport.PacketTarget{
		Addr: peerB.LocalAddr().(*net.UDPAddr).AddrPort(),
	}); err != nil {
		t.Fatalf("WritePacketTo peerB: %v", err)
	}
	_ = peerB.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, mappedFromB, err := peerB.ReadFromUDPAddrPort(buf)
	if err != nil || string(buf[:n]) != "to-b" {
		t.Fatalf("peerB read: n=%d payload=%q err=%v", n, string(buf[:n]), err)
	}
	if mappedFromA != mappedFromB {
		t.Fatalf("EIM violated: peerA saw %s, peerB saw %s", mappedFromA, mappedFromB)
	}

	// Now open a 3rd peer C never contacted by client and send to mappedFromA.
	peerC, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen peerC: %v", err)
	}
	defer func() { _ = peerC.Close() }()

	if _, err := peerC.WriteToUDPAddrPort([]byte("unsolicited-c"), mappedFromA); err != nil {
		t.Fatalf("peerC write: %v", err)
	}

	_ = sess.SetReadDeadline(time.Now().Add(3 * time.Second))
	payload, from, err := sess.ReadPacketFrom()
	if err != nil {
		t.Fatalf("ReadPacketFrom: %v", err)
	}
	if string(payload) != "unsolicited-c" {
		t.Fatalf("payload = %q, want unsolicited-c", payload)
	}
	wantC := peerC.LocalAddr().(*net.UDPAddr).AddrPort()
	if from != wantC {
		t.Fatalf("peer = %s, want %s", from, wantC)
	}
}

func TestEndToEnd_ProxyPacket_APDFilteringAndServerRouteBlock(t *testing.T) {
	const uuid = "22222222-2222-3333-4444-555555555555"
	routes := router.Routes{
		{
			Sources:     []string{"127.0.0.2/32"},
			Destination: router.EgressBlock,
			MatchType:   router.TypeCIDR,
		},
		router.CloneRoute(router.RouteServerDefault),
	}
	addr, stopServer := startPacketTestServer(t, uuid, routes)
	defer stopServer()

	rpcClient.SetUUID(uuid)
	if err := rpcClient.Init(addr, "", false, 1, nil); err != nil {
		t.Fatalf("rpcClient.Init: %v", err)
	}
	defer rpcClient.Destroy()

	c, err := rpcClient.New()
	if err != nil {
		t.Fatalf("rpcClient.New: %v", err)
	}
	defer func() { _ = c.Close() }()

	sess, err := c.DialPacketSession(context.Background(), "udp4", transport.PacketFilteringAPD)
	if err != nil {
		t.Fatalf("DialPacketSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	// Blocked destination (127.0.0.2) must be silently dropped without killing session.
	if err := sess.WritePacketTo([]byte("blocked"), transport.PacketTarget{
		Addr: netip.MustParseAddrPort("127.0.0.2:9999"),
	}); err != nil {
		t.Fatalf("WritePacketTo blocked: %v", err)
	}

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

	if err := sess.WritePacketTo([]byte("hello"), transport.PacketTarget{
		Addr: peerA.LocalAddr().(*net.UDPAddr).AddrPort(),
	}); err != nil {
		t.Fatalf("WritePacketTo peerA: %v", err)
	}

	buf := make([]byte, 64)
	_ = peerA.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, mapped, err := peerA.ReadFromUDPAddrPort(buf)
	if err != nil {
		t.Fatalf("peerA read: %v", err)
	}

	// peerB is uncontacted → must be dropped by APD filter; peerA must pass.
	_, _ = peerB.WriteToUDPAddrPort([]byte("drop-me"), mapped)
	_, _ = peerA.WriteToUDPAddrPort([]byte("keep-me"), mapped)

	_ = sess.SetReadDeadline(time.Now().Add(3 * time.Second))
	payload, from, err := sess.ReadPacketFrom()
	if err != nil {
		t.Fatalf("ReadPacketFrom: %v", err)
	}
	if string(payload) != "keep-me" || from != peerA.LocalAddr().(*net.UDPAddr).AddrPort() {
		t.Fatalf("got (%q, %s), want (keep-me, %s)", payload, from, peerA.LocalAddr())
	}
}

type legacyProxyOnlyServer struct {
	proto.UnimplementedProxyServer
}

func (legacyProxyOnlyServer) ProxyPacket(proto.Proxy_ProxyPacketServer) error {
	return status.Error(codes.Unimplemented, "method ProxyPacket not implemented")
}

func TestEndToEnd_ProxyPacket_LegacyUnimplementedFallback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	gs := grpc.NewServer()
	proto.RegisterProxyServer(gs, legacyProxyOnlyServer{})
	go func() { _ = gs.Serve(ln) }()
	defer gs.Stop()

	rpc.SetServiceName("")
	cc, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer func() { _ = cc.Close() }()

	c := &rpcClient.Client{
		ProxyClient: rpcClient.NewDynamicProxyClient(cc),
		DoneFunc:    func() error { return nil },
	}
	_, err = c.DialPacketSession(context.Background(), "udp4", transport.PacketFilteringEIF)
	if !errors.Is(err, transport.ErrPacketSessionUnsupported) {
		t.Fatalf("err = %v, want ErrPacketSessionUnsupported", err)
	}
	// Reset legacy cache so subsequent tests in the same process are unaffected.
	rpcClient.Destroy()
}
