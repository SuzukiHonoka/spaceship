package tun

import (
	"errors"
	"sync"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

type fakePacketDevice struct {
	in      chan []byte
	out     chan []byte
	closeCh chan struct{}
	once    sync.Once
}

func newFakePacketDevice() *fakePacketDevice {
	return &fakePacketDevice{
		in:      make(chan []byte, 8),
		out:     make(chan []byte, 8),
		closeCh: make(chan struct{}),
	}
}

func (d *fakePacketDevice) ReadPacket(buf []byte) (int, error) {
	select {
	case <-d.closeCh:
		return 0, errDeviceClosed
	case pkt := <-d.in:
		return copy(buf, pkt), nil
	}
}

func (d *fakePacketDevice) WritePacket(pkt *stack.PacketBuffer) error {
	select {
	case <-d.closeCh:
		return errDeviceClosed
	default:
	}
	var raw []byte
	for _, s := range pkt.AsSlices() {
		raw = append(raw, s...)
	}
	d.out <- raw
	return nil
}

func (d *fakePacketDevice) Close() error {
	d.once.Do(func() { close(d.closeCh) })
	return nil
}

type recordingDispatcher struct {
	mu       sync.Mutex
	received []tcpip.NetworkProtocolNumber
	notify   chan struct{}
}

func (r *recordingDispatcher) DeliverNetworkPacket(proto tcpip.NetworkProtocolNumber, _ *stack.PacketBuffer) {
	r.mu.Lock()
	r.received = append(r.received, proto)
	r.mu.Unlock()
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func (*recordingDispatcher) DeliverLinkPacket(tcpip.NetworkProtocolNumber, *stack.PacketBuffer) {}

func buildMinimalIPv4Packet(dst [4]byte) []byte {
	b := make([]byte, header.IPv4MinimumSize)
	hdr := header.IPv4(b)
	hdr.Encode(&header.IPv4Fields{
		TotalLength: header.IPv4MinimumSize,
		TTL:         64,
		Protocol:    uint8(header.UDPProtocolNumber),
		SrcAddr:     tcpip.AddrFrom4([4]byte{10, 0, 0, 2}),
		DstAddr:     tcpip.AddrFrom4(dst),
	})
	return b
}

func buildMinimalIPv6Packet(dst [16]byte) []byte {
	b := make([]byte, header.IPv6MinimumSize)
	hdr := header.IPv6(b)
	hdr.Encode(&header.IPv6Fields{
		PayloadLength:     0,
		TransportProtocol: header.UDPProtocolNumber,
		HopLimit:          64,
		SrcAddr:           tcpip.AddrFrom16([16]byte{0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}),
		DstAddr:           tcpip.AddrFrom16(dst),
	})
	return b
}

func TestPacketEndpoint_FiltersNoiseAndDeliversUnicast(t *testing.T) {
	dev := newFakePacketDevice()
	ep := newPacketEndpoint(dev, 1500, func(error) {})
	if ep.Capabilities()&stack.CapabilityRXChecksumOffload == 0 {
		t.Fatal("expected CapabilityRXChecksumOffload on packetEndpoint")
	}

	disp := &recordingDispatcher{notify: make(chan struct{}, 8)}
	ep.Attach(disp)
	defer func() {
		ep.Attach(nil)
		ep.Wait()
	}()

	// Noise packets that must be dropped before gVisor:
	dev.in <- []byte{0x00, 0x01}                                 // truncated
	dev.in <- buildMinimalIPv4Packet([4]byte{255, 255, 255, 255}) // broadcast
	dev.in <- buildMinimalIPv4Packet([4]byte{224, 0, 0, 251})     // IPv4 multicast (mDNS)
	dev.in <- buildMinimalIPv4Packet([4]byte{0, 0, 0, 0})         // unspecified
	dev.in <- buildMinimalIPv6Packet([16]byte{0xff, 0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xfb}) // IPv6 multicast

	// Valid global unicast IPv4 and IPv6 packets:
	dev.in <- buildMinimalIPv4Packet([4]byte{1, 1, 1, 1})
	dev.in <- buildMinimalIPv6Packet([16]byte{0x26, 0x06, 0x47, 0x00, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x11, 0x11})

	for range 2 {
		select {
		case <-disp.notify:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for unicast packets")
		}
	}

	disp.mu.Lock()
	got := append([]tcpip.NetworkProtocolNumber(nil), disp.received...)
	disp.mu.Unlock()
	if len(got) != 2 || got[0] != header.IPv4ProtocolNumber || got[1] != header.IPv6ProtocolNumber {
		t.Fatalf("delivered protocols = %v, want [IPv4, IPv6]", got)
	}

	// Verify WritePackets forwards to device.
	var list stack.PacketBufferList
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData([]byte{1, 2, 3, 4}),
	})
	list.PushBack(pkt)
	n, tcpErr := ep.WritePackets(list)
	pkt.DecRef()
	if tcpErr != nil || n != 1 {
		t.Fatalf("WritePackets = (%d, %v), want (1, nil)", n, tcpErr)
	}
	select {
	case out := <-dev.out:
		if len(out) != 4 {
			t.Fatalf("written len = %d, want 4", len(out))
		}
	case <-time.After(time.Second):
		t.Fatal("WritePackets did not reach device")
	}
}

type errPacketDevice struct {
	err error
}

func (e errPacketDevice) ReadPacket([]byte) (int, error)   { return 0, e.err }
func (e errPacketDevice) WritePacket(*stack.PacketBuffer) error { return e.err }
func (e errPacketDevice) Close() error                     { return nil }

func TestPacketEndpoint_ReportsUnexpectedReadFailure(t *testing.T) {
	want := errors.New("link failed")
	gotErr := make(chan error, 1)
	ep := newPacketEndpoint(errPacketDevice{err: want}, 1500, func(err error) {
		gotErr <- err
	})
	disp := &recordingDispatcher{notify: make(chan struct{}, 1)}
	ep.Attach(disp)
	select {
	case err := <-gotErr:
		if !errors.Is(err, want) {
			t.Fatalf("closed callback err = %v, want %v", err, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for closed callback")
	}
	ep.Close()
	ep.Wait()
}
