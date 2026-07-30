//go:build linux

package server

import (
	"net"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSetTCPUserTimeout(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	accepted := make(chan *net.TCPConn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptTCP()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	client, err := net.DialTCP("tcp4", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	var serverConn *net.TCPConn
	select {
	case serverConn = <-accepted:
		defer func() { _ = serverConn.Close() }()
	case err := <-acceptErr:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("AcceptTCP() did not return")
	}

	const timeout = 1500 * time.Millisecond
	if err := setTCPUserTimeout(serverConn, timeout); err != nil {
		t.Fatalf("setTCPUserTimeout() error = %v", err)
	}

	rawConn, err := serverConn.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var (
		got       int
		socketErr error
	)
	if err := rawConn.Control(func(fd uintptr) {
		maxInt := uintptr(^uint(0) >> 1)
		if fd > maxInt {
			socketErr = unix.EBADF
			return
		}
		got, socketErr = unix.GetsockoptInt(
			int(fd), // #nosec G115 -- explicitly bounded above
			unix.IPPROTO_TCP,
			unix.TCP_USER_TIMEOUT,
		)
	}); err != nil {
		t.Fatalf("RawConn.Control() error = %v", err)
	}
	if socketErr != nil {
		t.Fatalf("GetsockoptInt(TCP_USER_TIMEOUT) error = %v", socketErr)
	}
	if want := int(timeout.Milliseconds()); got != want {
		t.Fatalf("TCP_USER_TIMEOUT = %dms, want %dms", got, want)
	}
}

func TestSetTCPUserTimeoutRejectsInvalidDuration(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	pipeConn, peerConn := net.Pipe()
	defer func() { _ = pipeConn.Close() }()
	defer func() { _ = peerConn.Close() }()
	if err := setTCPUserTimeout(pipeConn, 0); err != nil {
		t.Fatalf("non-TCP input error = %v, want nil", err)
	}

	client, err := net.DialTCP("tcp4", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	serverConn, err := listener.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverConn.Close() }()

	for _, timeout := range []time.Duration{0, -time.Second} {
		if err := setTCPUserTimeout(serverConn, timeout); err == nil {
			t.Fatalf("setTCPUserTimeout(%s) accepted invalid duration", timeout)
		}
	}
}
