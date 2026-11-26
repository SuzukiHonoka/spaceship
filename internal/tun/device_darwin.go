//go:build darwin

package tun

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

const (
	darwinUTUNControlName = "com.apple.net.utun_control"
	darwinSysProtoControl = 2
	darwinUTUNOptIfName   = 2
	darwinPacketOffset    = 4
)

var (
	darwinHeaderV4 = [4]byte{0, 0, 0, unix.AF_INET}
	darwinHeaderV6 = [4]byte{0, 0, 0, unix.AF_INET6}
)

func Supported() bool {
	return true
}

func openTUNDevice(cfg Config, closed func(error)) (openedDevice, error) {
	var (
		fd   int
		name string
		err  error
	)
	created := cfg.FileDescriptor == nil
	if created {
		fd, name, err = createDarwinUTUN(cfg.Name)
		if err != nil {
			return openedDevice{}, err
		}
		if err := configureDarwinInterface(name, uint32(cfg.MTU)); err != nil {
			_ = unix.Close(fd)
			return openedDevice{}, err
		}
	} else {
		fd, err = unix.FcntlInt(uintptr(*cfg.FileDescriptor), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return openedDevice{}, fmt.Errorf("tun: duplicate darwin file descriptor %d: %w", *cfg.FileDescriptor, err)
		}
		name = cfg.Name
		if queried, qErr := darwinUTUNName(fd); qErr == nil && queried != "" {
			if name != "" && name != queried {
				_ = unix.Close(fd)
				return openedDevice{}, fmt.Errorf("tun: external descriptor belongs to %q, not %q", queried, name)
			}
			name = queried
		} else if name == "" {
			name = "utun"
		}
	}

	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return openedDevice{}, fmt.Errorf("tun: set darwin utun nonblocking: %w", err)
	}

	dev, err := newDarwinUTUNDevice(fd)
	if err != nil {
		_ = unix.Close(fd)
		return openedDevice{}, err
	}

	mtu := uint32(cfg.MTU)
	ep := newPacketEndpoint(dev, mtu, closed)
	return openedDevice{
		endpoint: ep,
		name:     name,
		mtu:      mtu,
		closer:   dev,
	}, nil
}

func createDarwinUTUN(requested string) (int, string, error) {
	unit := uint32(0) // 0 asks the kernel to pick the next free utunN
	if requested != "" && requested != DefaultName {
		if !strings.HasPrefix(requested, "utun") {
			return -1, "", fmt.Errorf("tun: macOS interface name must be \"utun<N>\" or empty, got %q", requested)
		}
		if len(requested) > len("utun") {
			var idx int
			if _, err := fmt.Sscanf(requested, "utun%d", &idx); err != nil || idx < 0 {
				return -1, "", fmt.Errorf("tun: invalid macOS utun name %q", requested)
			}
			unit = uint32(idx + 1)
		}
	}

	fd, err := unix.Socket(unix.AF_SYSTEM, unix.SOCK_DGRAM, darwinSysProtoControl)
	if err != nil {
		return -1, "", fmt.Errorf("tun: open darwin utun control socket: %w", err)
	}
	unix.CloseOnExec(fd)

	ctlInfo := &unix.CtlInfo{}
	copy(ctlInfo.Name[:], darwinUTUNControlName)
	if err := unix.IoctlCtlInfo(fd, ctlInfo); err != nil {
		_ = unix.Close(fd)
		return -1, "", fmt.Errorf("tun: CTLIOCGINFO %s: %w", darwinUTUNControlName, err)
	}

	sc := &unix.SockaddrCtl{
		ID:   ctlInfo.Id,
		Unit: unit,
	}
	if err := unix.Connect(fd, sc); err != nil {
		_ = unix.Close(fd)
		return -1, "", fmt.Errorf("tun: connect darwin utun (unit=%d): %w", unit, err)
	}

	name, err := darwinUTUNName(fd)
	if err != nil {
		_ = unix.Close(fd)
		return -1, "", fmt.Errorf("tun: query darwin utun name: %w", err)
	}
	return fd, name, nil
}

func darwinUTUNName(fd int) (string, error) {
	return unix.GetsockoptString(fd, darwinSysProtoControl, darwinUTUNOptIfName)
}

func configureDarwinInterface(name string, mtu uint32) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("tun: open darwin control socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()

	var mtuReq unix.IfreqMTU
	copy(mtuReq.Name[:], name)
	mtuReq.MTU = int32(mtu)
	if err := unix.IoctlSetIfreqMTU(fd, &mtuReq); err != nil {
		return fmt.Errorf("tun: set %s mtu to %d: %w", name, mtu, err)
	}

	if out, err := exec.Command("ifconfig", name, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("tun: bring %s up: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

type darwinUTUNDevice struct {
	fd      int
	kq      int
	stopR   int
	stopW   int
	writeMu sync.Mutex

	closeOnce sync.Once
	closeErr  error
	closed    bool
	closeMu   sync.Mutex
}

func newDarwinUTUNDevice(fd int) (*darwinUTUNDevice, error) {
	var pipeFDs [2]int
	if err := unix.Pipe(pipeFDs[:]); err != nil {
		return nil, fmt.Errorf("tun: create darwin stop pipe: %w", err)
	}
	unix.CloseOnExec(pipeFDs[0])
	unix.CloseOnExec(pipeFDs[1])
	_ = unix.SetNonblock(pipeFDs[0], true)
	_ = unix.SetNonblock(pipeFDs[1], true)

	kq, err := unix.Kqueue()
	if err != nil {
		_ = unix.Close(pipeFDs[0])
		_ = unix.Close(pipeFDs[1])
		return nil, fmt.Errorf("tun: create kqueue: %w", err)
	}
	unix.CloseOnExec(kq)

	changes := []unix.Kevent_t{
		{Ident: uint64(fd), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD},
		{Ident: uint64(pipeFDs[0]), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD},
	}
	if _, err := unix.Kevent(kq, changes, nil, nil); err != nil {
		_ = unix.Close(kq)
		_ = unix.Close(pipeFDs[0])
		_ = unix.Close(pipeFDs[1])
		return nil, fmt.Errorf("tun: register kqueue events: %w", err)
	}

	return &darwinUTUNDevice{
		fd:    fd,
		kq:    kq,
		stopR: pipeFDs[0],
		stopW: pipeFDs[1],
	}, nil
}

func (d *darwinUTUNDevice) ReadPacket(buf []byte) (int, error) {
	var (
		protoHdr [darwinPacketOffset]byte
		events   [2]unix.Kevent_t
		iovs     = [][]byte{protoHdr[:], buf}
	)
	for {
		n, err := unix.Readv(d.fd, iovs)
		if err == nil {
			if n <= darwinPacketOffset {
				continue
			}
			// Verify valid IPv4/IPv6 utun family header.
			family := binary.BigEndian.Uint32(protoHdr[:])
			if family != unix.AF_INET && family != unix.AF_INET6 {
				continue
			}
			return n - darwinPacketOffset, nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return 0, err
		}

		// Wait in kqueue until either the utun fd is readable or stop pipe fires.
		nev, kErr := unix.Kevent(d.kq, nil, events[:], nil)
		if kErr != nil {
			if errors.Is(kErr, unix.EINTR) {
				continue
			}
			return 0, kErr
		}
		for i := range nev {
			if int(events[i].Ident) == d.stopR {
				return 0, errDeviceClosed
			}
		}
	}
}

func (d *darwinUTUNDevice) WritePacket(pkt *stack.PacketBuffer) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	d.closeMu.Lock()
	closed := d.closed
	d.closeMu.Unlock()
	if closed {
		return errDeviceClosed
	}

	var hdr *[4]byte
	switch pkt.NetworkProtocolNumber {
	case header.IPv4ProtocolNumber:
		hdr = &darwinHeaderV4
	case header.IPv6ProtocolNumber:
		hdr = &darwinHeaderV6
	default:
		return nil
	}

	slices := pkt.AsSlices()
	iovs := make([][]byte, 0, 1+len(slices))
	iovs = append(iovs, hdr[:])
	for _, s := range slices {
		if len(s) == 0 {
			continue
		}
		iovs = append(iovs, s)
	}
	if _, err := unix.Writev(d.fd, iovs); err != nil && !errors.Is(err, unix.ENOBUFS) && !errors.Is(err, unix.EAGAIN) {
		return err
	}
	return nil
}

func (d *darwinUTUNDevice) Close() error {
	d.closeOnce.Do(func() {
		d.closeMu.Lock()
		d.closed = true
		d.closeMu.Unlock()
		_, _ = unix.Write(d.stopW, []byte{1})
		d.closeErr = errors.Join(
			unix.Close(d.fd),
			unix.Close(d.kq),
			unix.Close(d.stopR),
			unix.Close(d.stopW),
		)
	})
	return d.closeErr
}
