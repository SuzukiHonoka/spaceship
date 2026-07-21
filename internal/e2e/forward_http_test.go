package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	nethttp "net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/http"
	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport/forward"
	"golang.org/x/net/proxy"
)

// startUpstreamSOCKS runs a deliberately minimal SOCKS5 CONNECT server for the
// forward transport to dial through.
//
// It is implemented here rather than reusing spaceship's own SOCKS server
// because that one consults the same process-global router: with routes pointing
// at the forward egress, an upstream built from it would forward straight back
// into itself.
func startUpstreamSOCKS(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("upstream socks listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveUpstreamSOCKSConn(conn)
		}
	}()
	return ln.Addr().String()
}

func serveUpstreamSOCKSConn(conn net.Conn) {
	defer conn.Close()

	// Method negotiation: accept no-auth only.
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil || head[0] != socks5Ver {
		return
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	if _, err := conn.Write([]byte{socks5Ver, authNone}); err != nil {
		return
	}

	// Request.
	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil || req[1] != cmdConnect {
		return
	}
	var host string
	switch req[3] {
	case atypIPv4:
		b := make([]byte, 4)
		if _, err := io.ReadFull(conn, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case atypDomain:
		lb := make([]byte, 1)
		if _, err := io.ReadFull(conn, lb); err != nil {
			return
		}
		b := make([]byte, lb[0])
		if _, err := io.ReadFull(conn, b); err != nil {
			return
		}
		host = string(b)
	default:
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(conn, pb); err != nil {
		return
	}

	port := strconv.FormatUint(uint64(binary.BigEndian.Uint16(pb)), 10)
	target, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 5*time.Second)
	if err != nil {
		_, _ = conn.Write([]byte{socks5Ver, 0x01, 0, atypIPv4, 0, 0, 0, 0, 0, 0})
		return
	}
	defer target.Close()

	if _, err := conn.Write([]byte{socks5Ver, repSuccess, 0, atypIPv4, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}

	go func() { _, _ = io.Copy(target, conn) }()
	_, _ = io.Copy(conn, target)
}

// TestFullStack_ForwardEgressThroughUpstreamProxy covers the forward transport
// over the wire: SOCKS5 client → spaceship → forward egress → upstream SOCKS5
// proxy → echo target.
func TestFullStack_ForwardEgressThroughUpstreamProxy(t *testing.T) {
	upstream := startUpstreamSOCKS(t)

	dialer, err := proxy.SOCKS5("tcp", upstream, nil, proxy.Direct)
	if err != nil {
		t.Fatalf("building upstream dialer: %v", err)
	}
	forward.Attach(dialer)
	t.Cleanup(func() { forward.Attach(nil) })

	routeAll(t, router.EgressForward)
	echo := startTCPEcho(t)
	socksAddr := startSocksServer(t)

	conn := socks5Connect(t, socksAddr)
	rep, _ := socks5Command(t, conn, cmdConnect, echo)
	if rep != repSuccess {
		t.Fatalf("CONNECT through the forward egress: reply = %d, want success", rep)
	}

	payload := []byte("through an upstream proxy")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("writing through the forward egress: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("echoed payload = %q, want %q", got, payload)
	}
}

// TestFullStack_ForwardEgressRejectsUDP verifies the forward egress cannot carry
// UDP, so ASSOCIATE is refused up front and the client falls back to TCP rather
// than holding an association that drops every datagram.
func TestFullStack_ForwardEgressRejectsUDP(t *testing.T) {
	dialer, err := proxy.SOCKS5("tcp", startUpstreamSOCKS(t), nil, proxy.Direct)
	if err != nil {
		t.Fatalf("building upstream dialer: %v", err)
	}
	forward.Attach(dialer)
	t.Cleanup(func() { forward.Attach(nil) })

	routeAll(t, router.EgressForward)
	socksAddr := startSocksServer(t)

	ctrl := socks5Connect(t, socksAddr)
	rep, _ := socks5Command(t, ctrl, cmdUDPAssociate, nil)
	if rep != repCommandNotSupported {
		t.Errorf("UDP ASSOCIATE over a forward egress: reply = %d, want %d "+
			"(command not supported)", rep, repCommandNotSupported)
	}
}

// startHTTPProxy runs spaceship's HTTP proxy front end.
func startHTTPProxy(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := http.New(ctx, &http.Config{})
	addr := freeLoopbackAddr(t)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe("tcp", addr) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		select {
		case <-errCh:
		case <-time.After(10 * time.Second):
			t.Error("http proxy did not shut down")
		}
	})

	waitForListener(t, addr)
	return addr
}

// TestFullStack_HTTPProxyConnect covers the HTTP front end's CONNECT tunnel.
func TestFullStack_HTTPProxyConnect(t *testing.T) {
	routeAll(t, router.EgressDirect)
	echo := startTCPEcho(t)
	proxyAddr := startHTTPProxy(t)

	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial http proxy: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}

	target := echo.String()
	if _, err := conn.Write([]byte("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n")); err != nil {
		t.Fatalf("writing CONNECT: %v", err)
	}

	br := bufio.NewReader(conn)
	resp, err := nethttp.ReadResponse(br, &nethttp.Request{Method: nethttp.MethodConnect})
	if err != nil {
		t.Fatalf("reading CONNECT response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
	}

	payload := []byte("http connect round trip")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("writing through the tunnel: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("echoed payload = %q, want %q", got, payload)
	}
}

// TestFullStack_HTTPProxyPlainRequest covers the non-CONNECT path, where the
// front end forwards an absolute-URI request and relays the response.
func TestFullStack_HTTPProxyPlainRequest(t *testing.T) {
	routeAll(t, router.EgressDirect)

	origin := &nethttp.Server{
		Handler: nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			_, _ = w.Write([]byte("origin-response"))
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("origin listen: %v", err)
	}
	go func() { _ = origin.Serve(ln) }()
	t.Cleanup(func() { _ = origin.Close() })

	proxyAddr := startHTTPProxy(t)
	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatalf("parsing proxy url: %v", err)
	}

	client := &nethttp.Client{
		Transport: &nethttp.Transport{Proxy: nethttp.ProxyURL(proxyURL)},
		Timeout:   20 * time.Second,
	}
	resp, err := client.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("proxied GET: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading proxied body: %v", err)
	}
	if string(body) != "origin-response" {
		t.Errorf("proxied body = %q, want %q", body, "origin-response")
	}
}
