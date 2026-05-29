package socks

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestServer_ServeConn_NoAuth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &Config{}
	s := New(ctx, cfg)

	// mock connection
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

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
		c1.Close()
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &Config{
		Credentials: StaticCredentials{"user": "pass"},
	}
	s := New(ctx, cfg)

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

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
		c1.Close()
		errCh <- nil
	}()

	_ = s.ServeConn(c2)

	if err := <-errCh; err != nil {
		t.Fatalf("auth failed: %v", err)
	}
}

func TestServer_ServeConn_UserPass_Failure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &Config{
		Credentials: StaticCredentials{"user": "pass"},
	}
	s := New(ctx, cfg)

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	errCh := make(chan error, 1)
	go func() {
		c1.Write([]byte{socks5Version, 1, UserPassAuth})
		resp := make([]byte, 2)
		io.ReadFull(c1, resp)

		user := "user"
		pass := "wrong"
		authReq := append([]byte{userAuthVersion, byte(len(user))}, []byte(user)...)
		authReq = append(authReq, byte(len(pass)))
		authReq = append(authReq, []byte(pass)...)
		c1.Write(authReq)

		io.ReadFull(c1, resp)
		if resp[0] != userAuthVersion || resp[1] != authFailure {
			errCh <- io.ErrUnexpectedEOF
			return
		}
		c1.Close()
		errCh <- nil
	}()

	_ = s.ServeConn(c2)

	if err := <-errCh; err != nil {
		t.Fatalf("auth failure test failed: %v", err)
	}
}

func TestServer_ServeConn_NoAcceptable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &Config{
		Credentials: StaticCredentials{"user": "pass"},
	}
	s := New(ctx, cfg)

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	errCh := make(chan error, 1)
	go func() {
		// Client only supports NoAuth, but server requires Credentials
		c1.Write([]byte{socks5Version, 1, NoAuth})
		resp := make([]byte, 2)
		io.ReadFull(c1, resp)
		if resp[0] != socks5Version || resp[1] != noAcceptable {
			errCh <- io.ErrUnexpectedEOF
			return
		}
		c1.Close()
		errCh <- nil
	}()

	_ = s.ServeConn(c2)

	if err := <-errCh; err != nil {
		t.Fatalf("no acceptable auth test failed: %v", err)
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
