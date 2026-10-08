package socks

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
)

func TestServer_ServeConn_NoAuth(t *testing.T) {
	ctx := t.Context()

	cfg := &Config{}
	s := New(ctx, cfg)

	// mock connection
	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	defer func() { _ = c2.Close() }()

	errCh := make(chan error, 1)
	go func() {
		// client side
		// 1. version & methods
		if _, err := c1.Write([]byte{socks5Version, 1, NoAuth}); err != nil {
			errCh <- err
			return
		}
		// 2. read server response
		resp := make([]byte, 2)
		if _, err := io.ReadFull(c1, resp); err != nil {
			errCh <- err
			return
		}
		if resp[0] != socks5Version || resp[1] != NoAuth {
			errCh <- io.ErrUnexpectedEOF
			return
		}
		if err := c1.Close(); err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	// server side handles the request until it tries to read the SOCKS request
	// ServeConn will fail at NewRequest because we don't send one, but that's fine for testing auth.
	_ = s.ServeConn(c2)

	if err := <-errCh; err != nil {
		t.Fatalf("auth failed: %v", err)
	}
}

func TestServer_ServeConn_UserPass(t *testing.T) {
	ctx := t.Context()

	cfg := &Config{
		Credentials: StaticCredentials{"user": "pass"},
	}
	s := New(ctx, cfg)

	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	defer func() { _ = c2.Close() }()

	errCh := make(chan error, 1)
	go func() {
		// client side
		// 1. version & methods (only UserPassAuth)
		if _, err := c1.Write([]byte{socks5Version, 1, UserPassAuth}); err != nil {
			errCh <- err
			return
		}
		// 2. read server response
		resp := make([]byte, 2)
		if _, err := io.ReadFull(c1, resp); err != nil {
			errCh <- err
			return
		}
		if resp[0] != socks5Version || resp[1] != UserPassAuth {
			errCh <- io.ErrUnexpectedEOF
			return
		}

		// 3. UserPass Auth Request
		user := "user"
		pass := "pass"
		authReq := append([]byte{userAuthVersion, byte(len(user))}, []byte(user)...)
		authReq = append(authReq, byte(len(pass)))
		authReq = append(authReq, []byte(pass)...)
		if _, err := c1.Write(authReq); err != nil {
			errCh <- err
			return
		}

		// 4. read auth response
		if _, err := io.ReadFull(c1, resp); err != nil {
			errCh <- err
			return
		}
		if resp[0] != userAuthVersion || resp[1] != authSuccess {
			errCh <- io.ErrUnexpectedEOF
			return
		}
		if err := c1.Close(); err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	_ = s.ServeConn(c2)

	if err := <-errCh; err != nil {
		t.Fatalf("auth failed: %v", err)
	}
}

func TestServer_ServeConn_UserPass_Failure(t *testing.T) {
	ctx := t.Context()

	cfg := &Config{
		Credentials: StaticCredentials{"user": "pass"},
	}
	s := New(ctx, cfg)

	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	defer func() { _ = c2.Close() }()

	errCh := make(chan error, 1)
	go func() {
		if _, err := c1.Write([]byte{socks5Version, 1, UserPassAuth}); err != nil {
			errCh <- err
			return
		}
		resp := make([]byte, 2)
		if _, err := io.ReadFull(c1, resp); err != nil {
			errCh <- err
			return
		}

		user := "user"
		pass := "wrong"
		authReq := append([]byte{userAuthVersion, byte(len(user))}, []byte(user)...)
		authReq = append(authReq, byte(len(pass)))
		authReq = append(authReq, []byte(pass)...)
		if _, err := c1.Write(authReq); err != nil {
			errCh <- err
			return
		}

		if _, err := io.ReadFull(c1, resp); err != nil {
			errCh <- err
			return
		}
		if resp[0] != userAuthVersion || resp[1] != authFailure {
			errCh <- io.ErrUnexpectedEOF
			return
		}
		if err := c1.Close(); err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	_ = s.ServeConn(c2)

	if err := <-errCh; err != nil {
		t.Fatalf("auth failure test failed: %v", err)
	}
}

func TestServer_ServeConn_NoAcceptable(t *testing.T) {
	ctx := t.Context()

	cfg := &Config{
		Credentials: StaticCredentials{"user": "pass"},
	}
	s := New(ctx, cfg)

	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	defer func() { _ = c2.Close() }()

	errCh := make(chan error, 1)
	go func() {
		// Client only supports NoAuth, but server requires Credentials
		if _, err := c1.Write([]byte{socks5Version, 1, NoAuth}); err != nil {
			errCh <- err
			return
		}
		resp := make([]byte, 2)
		if _, err := io.ReadFull(c1, resp); err != nil {
			errCh <- err
			return
		}
		if resp[0] != socks5Version || resp[1] != noAcceptable {
			errCh <- io.ErrUnexpectedEOF
			return
		}
		if err := c1.Close(); err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	_ = s.ServeConn(c2)

	if err := <-errCh; err != nil {
		t.Fatalf("no acceptable auth test failed: %v", err)
	}
}

// A client may send tunnel bytes right after its CONNECT request without
// waiting for the reply. Those bytes arrive in the same segments as the
// request and end up in the handshake's buffered reader; once the connection
// itself is handed to the transport, they must still reach the target first.
func TestServer_ServeConn_EarlyDataAfterConnect(t *testing.T) {
	if err := router.SetRoutes(router.Routes{
		{MatchType: router.TypeDefault, Destination: router.EgressDirect},
	}); err != nil {
		t.Fatal(err)
	}

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echo.Close() })
	go func() {
		c, err := echo.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.Copy(c, c)
	}()
	target := echo.Addr().(*net.TCPAddr)

	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, &Config{})
	served := make(chan error, 1)
	go func() {
		c, err := proxy.Accept()
		if err != nil {
			served <- err
			return
		}
		served <- s.ServeConn(c)
	}()

	client, err := net.Dial("tcp", proxy.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))

	// Greeting, then read the method selection.
	if _, err := client.Write([]byte{socks5Version, 1, NoAuth}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(client, make([]byte, 2)); err != nil {
		t.Fatalf("read method selection: %v", err)
	}

	// CONNECT request and the early tunnel bytes in one write, so they share
	// a segment and the handshake reader buffers them past the request.
	early := []byte("early-tunnel-bytes")
	request := []byte{socks5Version, ConnectCommand, 0, ipv4Address}
	request = append(request, target.IP.To4()...)
	request = append(request, byte(target.Port>>8), byte(target.Port))
	if _, err := client.Write(append(request, early...)); err != nil {
		t.Fatal(err)
	}

	reply := make([]byte, 10) // VER REP RSV ATYP(IPv4) BND.ADDR(4) BND.PORT(2)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if reply[1] != successReply {
		t.Fatalf("reply code = %d, want success", reply[1])
	}

	late := []byte("-then-more")
	if _, err := client.Write(late); err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte(nil), early...), late...)
	got := make([]byte, len(want))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatalf("read echo: %v (got %q so far)", err, got)
	}
	if string(got) != string(want) {
		t.Fatalf("echo = %q, want %q", got, want)
	}

	_ = client.Close()
	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("ServeConn did not return after the client closed")
	}
}

func TestServer_ListenAndServe_Cancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, &Config{})

	// Run in a goroutine
	done := make(chan error, 1)
	go func() {
		done <- s.ListenAndServe("tcp", "127.0.0.1:0")
	}()

	// Wait a bit for it to start
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for server to stop")
	}
}
