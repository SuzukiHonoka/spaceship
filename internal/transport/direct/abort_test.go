package direct

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
)

// clientPair returns the proxy's side of a client connection and the client's
// own end, which observes how the proxy ended the stream.
func clientPair(t *testing.T) (proxySide net.Conn, client net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	client, err = net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	proxySide = <-accepted
	if proxySide == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() {
		_ = proxySide.Close()
		_ = client.Close()
	})
	return utils.OnceNetConn(proxySide), client
}

// startTarget answers each connection with payload and then either resets it
// or closes it in an orderly way.
func startTarget(t *testing.T, payload string, reset bool) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = conn.Write([]byte(payload))
				if reset {
					// Let the payload reach the proxy before the reset.
					time.Sleep(100 * time.Millisecond)
					_ = conn.(*net.TCPConn).SetLinger(0)
				}
				_ = conn.Close()
			}()
		}
	}()
	return listener.Addr().String()
}

func proxyOnce(t *testing.T, target string) (string, error) {
	t.Helper()
	proxySide, client := clientPair(t)
	done := make(chan error, 1)
	go func() {
		done <- New().Proxy(context.Background(), target, make(chan string, 1), proxySide, proxySide)
	}()
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(client)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Proxy did not return")
	}
	return string(got), err
}

func TestDirectProxyRelaysTargetResetAsReset(t *testing.T) {
	_, err := proxyOnce(t, startTarget(t, "partial response", true))
	if !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("client saw %v, want a connection reset for a truncated response", err)
	}
}

func TestDirectProxyKeepsOrderlyCloseOrderly(t *testing.T) {
	got, err := proxyOnce(t, startTarget(t, "complete response", false))
	if err != nil {
		t.Fatalf("client saw %v, want a clean EOF", err)
	}
	if got != "complete response" {
		t.Fatalf("client read %q", got)
	}
}
