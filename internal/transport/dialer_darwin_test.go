//go:build darwin

package transport

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestDarwin_OutboundInterfaceBinding(t *testing.T) {
	origIdx := OutboundInterfaceIndex()
	origMark := BypassMark()
	t.Cleanup(func() {
		SetOutboundInterfaceIndex(origIdx)
		SetBypassMark(origMark)
	})
	SetBypassMark(0)

	lo, err := net.InterfaceByName("lo0")
	if err != nil {
		t.Fatalf("InterfaceByName(lo0): %v", err)
	}
	if err := SetOutboundInterface(lo.Name); err != nil {
		t.Fatalf("SetOutboundInterface(lo0): %v", err)
	}
	if got := OutboundInterfaceIndex(); got != uint32(lo.Index) {
		t.Fatalf("OutboundInterfaceIndex = %d, want %d", got, lo.Index)
	}

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp4: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.Close()
		}
	}()

	dialer := NewOutboundDialer(2 * time.Second)
	conn, err := dialer.DialContext(context.Background(), "tcp4", ln.Addr().String())
	if err != nil {
		t.Fatalf("DialContext with IP_BOUND_IF=lo0 failed: %v", err)
	}
	_ = conn.Close()

	// Unconnected UDP listen with outboundSocketControl must also succeed.
	lc := NewOutboundListenConfig()
	pc, err := lc.ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket with IP_BOUND_IF=lo0 failed: %v", err)
	}
	_ = pc.Close()
}
