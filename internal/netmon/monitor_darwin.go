//go:build darwin

package netmon

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

type darwinMonitor struct {
	excludeTUN string

	mu        sync.Mutex
	current   DefaultInterface
	routeFD   int
	kq        int
	stopR     int
	stopW     int
	started   bool
	closeOnce sync.Once
	wg        sync.WaitGroup
}

func newPlatformMonitor(excludeTUN string) (Monitor, error) {
	return &darwinMonitor{
		excludeTUN: excludeTUN,
		routeFD:    -1,
		kq:         -1,
		stopR:      -1,
		stopW:      -1,
	}, nil
}

func (m *darwinMonitor) Current() (DefaultInterface, error) {
	return detectDarwinDefaultInterface(m.excludeTUN)
}

func (m *darwinMonitor) Start(onChange func(DefaultInterface)) error {
	cur, err := m.Current()
	if err == nil {
		m.mu.Lock()
		m.current = cur
		m.mu.Unlock()
	}

	fd, err := unix.Socket(unix.AF_ROUTE, unix.SOCK_RAW, 0)
	if err != nil {
		return err
	}
	unix.CloseOnExec(fd)
	_ = unix.SetNonblock(fd, true)

	var pipeFDs [2]int
	if err := unix.Pipe(pipeFDs[:]); err != nil {
		_ = unix.Close(fd)
		return err
	}
	unix.CloseOnExec(pipeFDs[0])
	unix.CloseOnExec(pipeFDs[1])
	_ = unix.SetNonblock(pipeFDs[0], true)
	_ = unix.SetNonblock(pipeFDs[1], true)

	kq, err := unix.Kqueue()
	if err != nil {
		_ = unix.Close(fd)
		_ = unix.Close(pipeFDs[0])
		_ = unix.Close(pipeFDs[1])
		return err
	}
	unix.CloseOnExec(kq)
	changes := []unix.Kevent_t{
		{Ident: uint64(fd), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD},
		{Ident: uint64(pipeFDs[0]), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD},
	}
	if _, err := unix.Kevent(kq, changes, nil, nil); err != nil {
		_ = unix.Close(kq)
		_ = unix.Close(fd)
		_ = unix.Close(pipeFDs[0])
		_ = unix.Close(pipeFDs[1])
		return err
	}

	m.mu.Lock()
	m.routeFD = fd
	m.kq = kq
	m.stopR = pipeFDs[0]
	m.stopW = pipeFDs[1]
	m.started = true
	m.wg.Add(1)
	m.mu.Unlock()

	go m.watchLoop(onChange)
	return nil
}

func (m *darwinMonitor) watchLoop(onChange func(DefaultInterface)) {
	defer m.wg.Done()
	var (
		events [2]unix.Kevent_t
		buf    = make([]byte, 4096)
	)
	for {
		n, err := unix.Kevent(m.kq, nil, events[:], nil)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return
		}
		routeChanged := false
		for i := range n {
			if int(events[i].Ident) == m.stopR {
				return
			}
			if int(events[i].Ident) == m.routeFD {
				for {
					rn, rErr := unix.Read(m.routeFD, buf)
					if rn <= 0 || rErr != nil {
						break
					}
					routeChanged = true
				}
			}
		}
		if !routeChanged {
			continue
		}
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

func (m *darwinMonitor) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		started := m.started
		stopW := m.stopW
		m.mu.Unlock()
		if started && stopW >= 0 {
			_, _ = unix.Write(stopW, []byte{1})
			m.wg.Wait()
		}
		for _, fd := range []int{m.routeFD, m.kq, m.stopR, m.stopW} {
			if fd >= 0 {
				_ = unix.Close(fd)
			}
		}
	})
	return nil
}

func detectDarwinDefaultInterface(excludeTUN string) (DefaultInterface, error) {
	rib, err := route.FetchRIB(syscall.AF_UNSPEC, route.RIBTypeRoute, 0)
	if err != nil {
		return DefaultInterface{}, err
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return DefaultInterface{}, err
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return DefaultInterface{}, err
	}
	byIdx := make(map[int]net.Interface, len(ifaces))
	for _, ifc := range ifaces {
		byIdx[ifc.Index] = ifc
	}

	var best DefaultInterface
	for _, msg := range msgs {
		rm, ok := msg.(*route.RouteMessage)
		if !ok || rm.Flags&syscall.RTF_UP == 0 || rm.Flags&syscall.RTF_GATEWAY == 0 {
			continue
		}
		if len(rm.Addrs) < 2 {
			continue
		}
		ifc, exists := byIdx[rm.Index]
		if !exists || ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if excludeTUN != "" && ifc.Name == excludeTUN {
			continue
		}
		if strings.HasPrefix(ifc.Name, "utun") && excludeTUN != "" {
			// Skip other utun interfaces when searching for physical egress.
			continue
		}

		switch dst := rm.Addrs[syscall.RTAX_DST].(type) {
		case *route.Inet4Addr:
			if dst.IP != [4]byte{} {
				continue
			}
			gw, ok := rm.Addrs[syscall.RTAX_GATEWAY].(*route.Inet4Addr)
			if !ok {
				continue
			}
			if best.Index == 0 {
				best.Index = uint32(ifc.Index)
				best.Name = ifc.Name
			}
			if uint32(ifc.Index) == best.Index && !best.Gateway4.IsValid() {
				best.Gateway4 = netip.AddrFrom4(gw.IP)
			}
		case *route.Inet6Addr:
			if dst.IP != [16]byte{} {
				continue
			}
			gw, ok := rm.Addrs[syscall.RTAX_GATEWAY].(*route.Inet6Addr)
			if !ok {
				continue
			}
			if best.Index == 0 {
				best.Index = uint32(ifc.Index)
				best.Name = ifc.Name
			}
			if uint32(ifc.Index) == best.Index && !best.Gateway6.IsValid() {
				best.Gateway6 = netip.AddrFrom16(gw.IP)
			}
		}
	}
	if best.Index == 0 {
		return DefaultInterface{}, ErrNoDefaultInterface
	}
	return best, nil
}
