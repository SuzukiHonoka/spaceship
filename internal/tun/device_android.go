//go:build android

package tun

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/link/fdbased"
)

// androidDeviceName is used when the caller does not name the VpnService fd.
// The interface is often invisible to the app, so the name is not queried.
const androidDeviceName = "tun"

func Supported() bool {
	return true
}

// openTUNDevice adopts a VpnService TUN fd. Android delivers a raw IP
// descriptor; the tun interface is often not visible to the app, so the Linux
// TUNGETIFF and SIOCGIFMTU checks do not apply. Spaceship duplicates the fd
// and closes only that duplicate.
func openTUNDevice(cfg Config, closed func(error)) (openedDevice, error) {
	if cfg.FileDescriptor == nil {
		return openedDevice{}, errors.New("tun: Android requires a VpnService file descriptor")
	}
	// NormalizeConfig bounds MTU. Repeat the check so the uint32 conversion
	// below stays in range even if a caller skipped normalization.
	if cfg.MTU < minMTU || cfg.MTU > maxMTU {
		return openedDevice{}, fmt.Errorf(
			"tun: mtu must be between %d and %d: %d", minMTU, maxMTU, cfg.MTU,
		)
	}

	fd, err := unix.FcntlInt(uintptr(*cfg.FileDescriptor), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return openedDevice{}, fmt.Errorf("tun: duplicate file descriptor %d: %w", *cfg.FileDescriptor, err)
	}
	owned := &ownedDescriptor{fd: fd}

	// fdbased drives the descriptor from its own poll loop and requires
	// nonblocking I/O. F_DUPFD_CLOEXEC shares file status flags with the
	// caller's descriptor, so this intentionally makes both views nonblocking.
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = owned.Close()
		return openedDevice{}, fmt.Errorf("tun: make descriptor nonblocking: %w", err)
	}

	// #nosec G115 -- explicitly bounded to minMTU..maxMTU immediately above.
	mtu := uint32(cfg.MTU)
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

	name := cfg.Name
	if name == "" {
		name = androidDeviceName
	}
	return openedDevice{
		endpoint: endpoint,
		name:     name,
		mtu:      mtu,
		closer:   owned,
	}, nil
}
