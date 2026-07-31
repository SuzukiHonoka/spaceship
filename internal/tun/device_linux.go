//go:build linux

package tun

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/link/fdbased"
)

func Supported() bool {
	return true
}

func openTUNDevice(cfg Config, closed func(error)) (openedDevice, error) {
	fd := -1
	name := cfg.Name
	requestedName := cfg.Name
	created := cfg.FileDescriptor == nil

	var err error
	if created {
		fd, name, err = createTUN(name)
		if err != nil {
			return openedDevice{}, err
		}
	} else {
		fd, err = unix.FcntlInt(uintptr(*cfg.FileDescriptor), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return openedDevice{}, fmt.Errorf("tun: duplicate file descriptor %d: %w", *cfg.FileDescriptor, err)
		}
		name, err = validateTUNDescriptor(fd)
		if err != nil {
			_ = unix.Close(fd)
			return openedDevice{}, err
		}
		if requestedName != "" && requestedName != name {
			_ = unix.Close(fd)
			return openedDevice{}, fmt.Errorf(
				"tun: external descriptor belongs to interface %q, not requested interface %q",
				name,
				requestedName,
			)
		}
	}

	owned := &ownedDescriptor{fd: fd}
	if created {
		if err := configureCreatedInterface(name, uint32(cfg.MTU)); err != nil {
			_ = owned.Close()
			return openedDevice{}, err
		}
	}

	mtu, err := interfaceMTU(name)
	if err != nil {
		_ = owned.Close()
		return openedDevice{}, err
	}
	if mtu < minMTU || mtu > maxMTU {
		_ = owned.Close()
		return openedDevice{}, fmt.Errorf(
			"tun: interface %s mtu must be between %d and %d: %d",
			name,
			minMTU,
			maxMTU,
			mtu,
		)
	}
	// fdbased drives the descriptor from its own poll loop and requires
	// nonblocking I/O. F_DUPFD_CLOEXEC shares file status flags with the
	// caller's descriptor, so this intentionally makes both views nonblocking.
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = owned.Close()
		return openedDevice{}, fmt.Errorf("tun: make descriptor nonblocking: %w", err)
	}

	endpoint, err := fdbased.New(&fdbased.Options{
		FDs:                []int{fd},
		MTU:                mtu,
		EthernetHeader:     false,
		PacketDispatchMode: fdbased.Readv,
		ClosedFunc: func(linkErr tcpip.Error) {
			if linkErr == nil {
				closed(nil)
				return
			}
			closed(fmt.Errorf("%s", linkErr))
		},
	})
	if err != nil {
		_ = owned.Close()
		return openedDevice{}, fmt.Errorf("tun: create file-descriptor endpoint: %w", err)
	}

	return openedDevice{
		endpoint: endpoint,
		name:     name,
		mtu:      mtu,
		closer:   owned,
	}, nil
}

func createTUN(name string) (int, string, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return -1, "", fmt.Errorf("tun: open /dev/net/tun: %w", err)
	}

	ifreq, err := unix.NewIfreq(name)
	if err != nil {
		_ = unix.Close(fd)
		return -1, "", fmt.Errorf("tun: prepare interface %q: %w", name, err)
	}
	ifreq.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI | unix.IFF_TUN_EXCL)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifreq); err != nil {
		_ = unix.Close(fd)
		return -1, "", fmt.Errorf("tun: create interface %s: %w", name, err)
	}
	return fd, ifreq.Name(), nil
}

func validateTUNDescriptor(fd int) (string, error) {
	ifreq, err := unix.NewIfreq("")
	if err != nil {
		return "", fmt.Errorf("tun: prepare descriptor validation: %w", err)
	}
	if err := unix.IoctlIfreq(fd, unix.TUNGETIFF, ifreq); err != nil {
		return "", fmt.Errorf("tun: descriptor is not an attached TUN device: %w", err)
	}

	flags := ifreq.Uint16()
	if flags&unix.IFF_TUN == 0 || flags&unix.IFF_TAP != 0 || flags&unix.IFF_NO_PI == 0 {
		return "", fmt.Errorf(
			"tun: descriptor must use IFF_TUN|IFF_NO_PI (flags=%#x)",
			flags,
		)
	}
	if flags&(unix.IFF_VNET_HDR|unix.IFF_MULTI_QUEUE) != 0 {
		return "", fmt.Errorf(
			"tun: descriptor cannot use IFF_VNET_HDR or IFF_MULTI_QUEUE (flags=%#x)",
			flags,
		)
	}
	if ifreq.Name() == "" {
		return "", errors.New("tun: descriptor returned an empty interface name")
	}
	return ifreq.Name(), nil
}

func configureCreatedInterface(name string, mtu uint32) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("tun: open interface control socket: %w", err)
	}
	defer unix.Close(fd)

	ifreq, err := unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("tun: prepare interface %s: %w", name, err)
	}
	ifreq.SetUint32(mtu)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFMTU, ifreq); err != nil {
		return fmt.Errorf("tun: set %s mtu to %d: %w", name, mtu, err)
	}

	ifreq, err = unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("tun: prepare interface %s flags: %w", name, err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifreq); err != nil {
		return fmt.Errorf("tun: get %s flags: %w", name, err)
	}
	ifreq.SetUint16(ifreq.Uint16() | unix.IFF_UP)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifreq); err != nil {
		return fmt.Errorf("tun: bring %s up: %w", name, err)
	}
	return nil
}

func interfaceMTU(name string) (uint32, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("tun: open MTU control socket: %w", err)
	}
	defer unix.Close(fd)

	ifreq, err := unix.NewIfreq(name)
	if err != nil {
		return 0, fmt.Errorf("tun: prepare interface %s MTU query: %w", name, err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFMTU, ifreq); err != nil {
		return 0, fmt.Errorf("tun: get %s mtu: %w", name, err)
	}
	return ifreq.Uint32(), nil
}

type ownedDescriptor struct {
	mu  sync.Mutex
	fd  int
	err error
}

func (d *ownedDescriptor) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fd < 0 {
		return d.err
	}
	d.err = unix.Close(d.fd)
	d.fd = -1
	return d.err
}
