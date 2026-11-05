//go:build unix

package direct

import (
	"bytes"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
)

func dialTestPacketReader(t *testing.T) (transport.PacketReader, net.PacketConn, net.PacketConn) {
	t.Helper()
	peer, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("peer listen: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })

	pc, err := New().(transport.PacketDialer).DialPacket("udp4", peer.LocalAddr().String())
	if err != nil {
		t.Fatalf("DialPacket() error = %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	reader, ok := pc.(transport.PacketReader)
	if !ok {
		t.Fatalf("direct packet conn %T does not implement transport.PacketReader", pc)
	}
	return reader, pc, peer
}

func TestDirect_ReadPacketReturnsWholeDatagram(t *testing.T) {
	reader, pc, peer := dialTestPacketReader(t)

	// 8 KiB exceeds any Ethernet MTU yet fits the macOS loopback datagram limit.
	want := bytes.Repeat([]byte("0123456789abcdef"), 512)
	if _, err := peer.WriteTo(want, pc.LocalAddr()); err != nil {
		t.Fatalf("peer write: %v", err)
	}
	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, addr, err := reader.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ReadPacket() returned %d bytes, want %d intact", len(got), len(want))
	}
	if cap(got) != len(got) {
		t.Fatalf("ReadPacket() cap = %d, want an exact-size copy, not a pooled buffer", cap(got))
	}
	if addr.String() != peer.LocalAddr().String() {
		t.Fatalf("ReadPacket() addr = %v, want %v", addr, peer.LocalAddr())
	}
}

func TestDirect_ReadPacketHonorsReadDeadline(t *testing.T) {
	reader, pc, _ := dialTestPacketReader(t)

	_ = pc.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	_, _, err := reader.ReadPacket()
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("ReadPacket() error = %v, want os.ErrDeadlineExceeded", err)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("ReadPacket() error = %v, want a net.Error timeout", err)
	}
}

func TestDirect_ReadPacketUnblocksOnClose(t *testing.T) {
	reader, pc, _ := dialTestPacketReader(t)

	done := make(chan error, 1)
	go func() {
		_, _, err := reader.ReadPacket()
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_ = pc.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("ReadPacket() error = %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadPacket() did not return after Close")
	}
}
