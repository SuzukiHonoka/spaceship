//go:build windows

package tun

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/windows"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

func Supported() bool {
	return true
}

func openTUNDevice(cfg Config, closed func(error)) (openedDevice, error) {
	if cfg.FileDescriptor != nil {
		return openedDevice{}, errors.New("tun: external file_descriptor is not supported on Windows (Wintun uses adapter handles)")
	}
	lib, err := loadWintun(cfg.WintunPath)
	if err != nil {
		return openedDevice{}, err
	}
	adapter, err := lib.CreateOrOpenAdapter(cfg.Name)
	if err != nil {
		return openedDevice{}, err
	}
	session, err := adapter.StartSession(wintunRingCapacity)
	if err != nil {
		_ = adapter.Close()
		return openedDevice{}, err
	}
	stopEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		session.End()
		_ = adapter.Close()
		return openedDevice{}, fmt.Errorf("tun: create windows stop event: %w", err)
	}

	dev := &windowsTUNDevice{
		adapter:   adapter,
		session:   session,
		readWait:  session.ReadWaitEvent(),
		stopEvent: stopEvent,
		luid:      adapter.LUID(),
	}
	mtu := uint32(cfg.MTU)
	ep := newPacketEndpoint(dev, mtu, closed)
	return openedDevice{
		endpoint: ep,
		name:     cfg.Name,
		mtu:      mtu,
		closer:   dev,
	}, nil
}

type windowsTUNDevice struct {
	adapter   *wintunAdapter
	session   *wintunSession
	readWait  windows.Handle
	stopEvent windows.Handle
	luid      uint64

	writeMu   sync.Mutex
	closeMu   sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func (d *windowsTUNDevice) ReadPacket(buf []byte) (int, error) {
	handles := []windows.Handle{d.readWait, d.stopEvent}
	for {
		pkt, err := d.session.ReceivePacket()
		if err == nil {
			n := copy(buf, pkt)
			d.session.ReleaseReceivePacket(pkt)
			return n, nil
		}
		if !errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			return 0, err
		}

		waitRes, waitErr := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
		if waitErr != nil {
			return 0, waitErr
		}
		if waitRes == windows.WAIT_OBJECT_0+1 {
			return 0, errDeviceClosed
		}
	}
}

func (d *windowsTUNDevice) WritePacket(pkt *stack.PacketBuffer) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	d.closeMu.Lock()
	closed := d.closed
	d.closeMu.Unlock()
	if closed {
		return errDeviceClosed
	}

	size := pkt.Size()
	if size <= 0 {
		return nil
	}
	dst, err := d.session.AllocateSendPacket(size)
	if err != nil {
		if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			// Ring full under burst: drop packet rather than tearing down the device.
			return nil
		}
		return err
	}
	offset := 0
	for _, s := range pkt.AsSlices() {
		offset += copy(dst[offset:], s)
	}
	d.session.SendPacket(dst)
	return nil
}

func (d *windowsTUNDevice) Close() error {
	d.closeOnce.Do(func() {
		d.closeMu.Lock()
		d.closed = true
		d.closeMu.Unlock()
		_ = windows.SetEvent(d.stopEvent)
		d.session.End()
		d.closeErr = errors.Join(
			d.adapter.Close(),
			windows.CloseHandle(d.stopEvent),
		)
	})
	return d.closeErr
}
