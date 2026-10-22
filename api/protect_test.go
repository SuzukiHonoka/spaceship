package api

import (
	"net"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

func TestSetSocketProtectorReachesOutboundDial(t *testing.T) {
	original := transport.BypassMark()
	t.Cleanup(func() {
		transport.SetBypassMark(original)
		SetSocketProtector(nil)
	})
	transport.SetBypassMark(0)

	var calls int
	SetSocketProtector(func(fd int) error {
		if fd < 0 {
			t.Errorf("protector fd = %d", fd)
		}
		calls++
		return nil
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	conn, err := transport.NewOutboundDialer(3*time.Second).Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
	if calls == 0 {
		t.Fatal("api.SetSocketProtector was not invoked on dial")
	}
}
