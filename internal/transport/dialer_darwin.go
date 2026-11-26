//go:build darwin

package transport

import (
	"fmt"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func outboundSocketControl(network, _ string, raw syscall.RawConn) error {
	if mark := bypassMark.Load(); mark != 0 {
		return fmt.Errorf("outbound socket mark %#x is unsupported on darwin", mark)
	}
	ifIdx := outboundIfIndex.Load()
	if ifIdx == 0 && socketProtectorHook() == nil {
		return nil
	}

	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		if err := protectOutboundSocket(int(fd)); err != nil {
			socketErr = err
			return
		}
		if ifIdx == 0 {
			return
		}
		switch {
		case strings.HasSuffix(network, "6"):
			if err := unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, int(ifIdx)); err != nil {
				socketErr = fmt.Errorf("set darwin IPV6_BOUND_IF %d: %w", ifIdx, err)
			}
		default:
			if err := unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, int(ifIdx)); err != nil {
				socketErr = fmt.Errorf("set darwin IP_BOUND_IF %d: %w", ifIdx, err)
			}
		}
	}); err != nil {
		return fmt.Errorf("access outbound socket: %w", err)
	}
	return socketErr
}

func verifyBypassMark(mark uint32) error {
	return fmt.Errorf("outbound socket mark %#x is unsupported on darwin", mark)
}
