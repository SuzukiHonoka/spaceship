//go:build windows

package netmon

import (
	"encoding/binary"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapiDLL       = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetBestIf     = iphlpapiDLL.NewProc("GetBestInterface")
	procGetBestIfEx   = iphlpapiDLL.NewProc("GetBestInterfaceEx")
)

type windowsMonitor struct {
	excludeTUN string
	mu         sync.Mutex
	current    DefaultInterface
	stopCh     chan struct{}
	closeOnce  sync.Once
	wg         sync.WaitGroup
}

func newPlatformMonitor(excludeTUN string) (Monitor, error) {
	return &windowsMonitor{
		excludeTUN: excludeTUN,
		stopCh:     make(chan struct{}),
	}, nil
}

func (m *windowsMonitor) Current() (DefaultInterface, error) {
	return detectWindowsDefaultInterface(m.excludeTUN)
}

func (m *windowsMonitor) Start(onChange func(DefaultInterface)) error {
	if cur, err := m.Current(); err == nil {
		m.mu.Lock()
		m.current = cur
		m.mu.Unlock()
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-m.stopCh:
				return
			case <-ticker.C:
				next, err := m.Current()
				if err != nil {
					continue
				}
				m.mu.Lock()
				changed := !m.current.Equal(next)
				if changed {
					m.current = next
				}
				m.mu.Unlock()
				if changed && onChange != nil {
					onChange(next)
				}
			}
		}
	}()
	return nil
}

func (m *windowsMonitor) Close() error {
	m.closeOnce.Do(func() {
		close(m.stopCh)
		m.wg.Wait()
	})
	return nil
}

func detectWindowsDefaultInterface(excludeTUN string) (DefaultInterface, error) {
	// Query best interface toward 1.1.1.1 (0x01010101).
	destIP := [4]byte{1, 1, 1, 1}
	destUint := binary.LittleEndian.Uint32(destIP[:])
	var ifIdx uint32
	r0, _, _ := syscall.SyscallN(
		procGetBestIf.Addr(),
		uintptr(destUint),
		uintptr(unsafe.Pointer(&ifIdx)),
	)
	if r0 == 0 && ifIdx != 0 {
		if ifc, err := net.InterfaceByIndex(int(ifIdx)); err == nil && ifc.Name != excludeTUN {
			return DefaultInterface{
				Index: uint32(ifc.Index),
				Name:  ifc.Name,
			}, nil
		}
	}

	// Fallback: first UP, non-loopback, non-TUN interface with a global unicast address.
	ifaces, err := net.Interfaces()
	if err != nil {
		return DefaultInterface{}, err
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Name == excludeTUN {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			addr, ok := netip.AddrFromSlice(ipNet.IP)
			if ok && addr.Unmap().IsGlobalUnicast() {
				return DefaultInterface{
					Index: uint32(ifc.Index),
					Name:  ifc.Name,
				}, nil
			}
		}
	}
	return DefaultInterface{}, ErrNoDefaultInterface
}

var _ = procGetBestIfEx
