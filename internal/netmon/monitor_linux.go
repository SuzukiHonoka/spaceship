//go:build linux

package netmon

import (
	"bufio"
	"encoding/hex"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"
)

type linuxMonitor struct {
	excludeTUN string
	mu         sync.Mutex
	current    DefaultInterface
	stopCh     chan struct{}
	closeOnce  sync.Once
	wg         sync.WaitGroup
}

func newPlatformMonitor(excludeTUN string) (Monitor, error) {
	return &linuxMonitor{
		excludeTUN: excludeTUN,
		stopCh:     make(chan struct{}),
	}, nil
}

func (m *linuxMonitor) Current() (DefaultInterface, error) {
	return detectLinuxDefaultInterface(m.excludeTUN)
}

func (m *linuxMonitor) Start(onChange func(DefaultInterface)) error {
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

func (m *linuxMonitor) Close() error {
	m.closeOnce.Do(func() {
		close(m.stopCh)
		m.wg.Wait()
	})
	return nil
}

func detectLinuxDefaultInterface(excludeTUN string) (DefaultInterface, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return DefaultInterface{}, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	// Skip header
	if !scanner.Scan() {
		return DefaultInterface{}, ErrNoDefaultInterface
	}
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 {
			continue
		}
		ifaceName := fields[0]
		destHex := fields[1]
		gwHex := fields[2]
		maskHex := fields[7]
		if destHex != "00000000" || maskHex != "00000000" {
			continue
		}
		if excludeTUN != "" && ifaceName == excludeTUN {
			continue
		}
		ifc, err := net.InterfaceByName(ifaceName)
		if err != nil || ifc.Index <= 0 {
			continue
		}
		var gw netip.Addr
		if raw, err := hex.DecodeString(gwHex); err == nil && len(raw) == 4 {
			// /proc/net/route stores IPv4 in little-endian host order on x86/arm64.
			gw = netip.AddrFrom4([4]byte{raw[3], raw[2], raw[1], raw[0]})
		}
		return DefaultInterface{
			Index:    uint32(ifc.Index),
			Name:     ifc.Name,
			Gateway4: gw,
		}, nil
	}
	return DefaultInterface{}, ErrNoDefaultInterface
}
