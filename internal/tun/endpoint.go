package tun

import (
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// tunPacketDevice is the platform-independent packet I/O contract implemented
// by macOS utun, Windows Wintun, and test/in-memory TUN devices.
type tunPacketDevice interface {
	ReadPacket(buf []byte) (int, error)
	WritePacket(pkt *stack.PacketBuffer) error
	Close() error
}

// packetEndpoint adapts any tunPacketDevice to gVisor's stack.LinkEndpoint.
// It enables RXChecksumOffload (TUN packets are kernel-originated) and drops
// non-IP, broadcast, and multicast/non-global-unicast noise before gVisor
// allocates per-flow state.
type packetEndpoint struct {
	dev    tunPacketDevice
	mtu    atomic.Uint32
	closed func(error)

	mu            sync.RWMutex
	dispatcher    stack.NetworkDispatcher
	onCloseAction func()
	stopOnce      sync.Once
	stopped       atomic.Bool
	wg            sync.WaitGroup
}

func newPacketEndpoint(dev tunPacketDevice, mtu uint32, closed func(error)) *packetEndpoint {
	ep := &packetEndpoint{
		dev:    dev,
		closed: closed,
	}
	ep.mtu.Store(mtu)
	return ep
}

func (e *packetEndpoint) MTU() uint32 {
	return e.mtu.Load()
}

func (e *packetEndpoint) SetMTU(mtu uint32) {
	e.mtu.Store(mtu)
}

func (e *packetEndpoint) MaxHeaderLength() uint16 {
	return 0
}

func (e *packetEndpoint) LinkAddress() tcpip.LinkAddress {
	return ""
}

func (e *packetEndpoint) SetLinkAddress(tcpip.LinkAddress) {}

func (e *packetEndpoint) Capabilities() stack.LinkEndpointCapabilities {
	return stack.CapabilityRXChecksumOffload
}

func (e *packetEndpoint) ARPHardwareType() header.ARPHardwareType {
	return header.ARPHardwareNone
}

func (e *packetEndpoint) AddHeader(*stack.PacketBuffer) {}

func (e *packetEndpoint) ParseHeader(*stack.PacketBuffer) bool {
	return true
}

func (e *packetEndpoint) SetOnCloseAction(f func()) {
	e.mu.Lock()
	e.onCloseAction = f
	e.mu.Unlock()
}

func (e *packetEndpoint) Attach(dispatcher stack.NetworkDispatcher) {
	e.mu.Lock()
	start := e.dispatcher == nil && dispatcher != nil && !e.stopped.Load()
	stopping := dispatcher == nil && e.dispatcher != nil
	e.dispatcher = dispatcher
	if start {
		e.wg.Add(1)
	}
	e.mu.Unlock()

	if start {
		go e.dispatchLoop()
		return
	}
	if stopping {
		e.Close()
	}
}

func (e *packetEndpoint) IsAttached() bool {
	e.mu.RLock()
	attached := e.dispatcher != nil
	e.mu.RUnlock()
	return attached
}

func (e *packetEndpoint) Close() {
	e.stopOnce.Do(func() {
		e.stopped.Store(true)
		e.mu.Lock()
		e.dispatcher = nil
		onClose := e.onCloseAction
		e.mu.Unlock()
		if e.dev != nil {
			_ = e.dev.Close()
		}
		if onClose != nil {
			onClose()
		}
	})
}

func (e *packetEndpoint) Wait() {
	e.wg.Wait()
}

func (e *packetEndpoint) WritePackets(pkts stack.PacketBufferList) (int, tcpip.Error) {
	if e.stopped.Load() {
		return 0, &tcpip.ErrClosedForSend{}
	}
	written := 0
	for _, pkt := range pkts.AsSlice() {
		if err := e.dev.WritePacket(pkt); err != nil {
			if e.stopped.Load() {
				return written, &tcpip.ErrClosedForSend{}
			}
			return written, &tcpip.ErrAborted{}
		}
		written++
	}
	return written, nil
}

func (e *packetEndpoint) dispatchLoop() {
	defer e.wg.Done()

	bufSize := max(int(e.MTU())+64, 65536)
	readBuf := make([]byte, bufSize)

	for {
		n, err := e.dev.ReadPacket(readBuf)
		if err != nil {
			if !e.stopped.Load() && e.closed != nil {
				e.closed(err)
			}
			return
		}
		if n <= 0 {
			continue
		}
		raw := readBuf[:n]
		proto, ok := classifyInboundIPPacket(raw)
		if !ok {
			continue
		}

		e.mu.RLock()
		dispatcher := e.dispatcher
		e.mu.RUnlock()
		if dispatcher == nil {
			return
		}

		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload:           buffer.MakeWithData(raw),
			IsForwardedPacket: true,
		})
		dispatcher.DeliverNetworkPacket(proto, pkt)
		pkt.DecRef()
	}
}

// classifyInboundIPPacket determines the IP version and filters out malformed,
// broadcast, unspecified, and multicast/link-local-multicast packets early.
func classifyInboundIPPacket(raw []byte) (tcpip.NetworkProtocolNumber, bool) {
	if len(raw) < header.IPv4MinimumSize {
		return 0, false
	}
	switch header.IPVersion(raw) {
	case header.IPv4Version:
		ipHdr := header.IPv4(raw)
		dstSlice := ipHdr.DestinationAddressSlice()
		if len(dstSlice) != 4 {
			return 0, false
		}
		dst := netip.AddrFrom4([4]byte(dstSlice))
		if dst.IsUnspecified() || dst.IsMulticast() || dst == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
			return 0, false
		}
		return header.IPv4ProtocolNumber, true
	case header.IPv6Version:
		if len(raw) < header.IPv6MinimumSize {
			return 0, false
		}
		ipHdr := header.IPv6(raw)
		dstSlice := ipHdr.DestinationAddressSlice()
		if len(dstSlice) != 16 {
			return 0, false
		}
		dst := netip.AddrFrom16([16]byte(dstSlice))
		if dst.IsUnspecified() || dst.IsMulticast() {
			return 0, false
		}
		return header.IPv6ProtocolNumber, true
	default:
		return 0, false
	}
}

// Compile-time interface check.
var _ stack.LinkEndpoint = (*packetEndpoint)(nil)

var errDeviceClosed = errors.New("tun: device closed")
