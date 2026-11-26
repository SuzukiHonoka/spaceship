//go:build windows

package transport

import (
	"encoding/binary"
	"fmt"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

const (
	winIPUnicastIf   = 31
	winIPv6UnicastIf = 31
)

func outboundSocketControl(network, _ string, raw syscall.RawConn) error {
	if mark := bypassMark.Load(); mark != 0 {
		return fmt.Errorf("outbound socket mark %#x is unsupported on windows", mark)
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
		handle := windows.Handle(fd)
		if strings.HasSuffix(network, "6") {
			// Windows IPV6_UNICAST_IF takes host byte order interface index.
			if err := windows.SetsockoptInt(handle, windows.IPPROTO_IPV6, winIPv6UnicastIf, int(ifIdx)); err != nil {
				socketErr = fmt.Errorf("set windows IPV6_UNICAST_IF %d: %w", ifIdx, err)
			}
			return
		}
		// Windows IP_UNICAST_IF takes the interface index in network (big-endian) byte order.
		var be [4]byte
		binary.BigEndian.PutUint32(be[:], ifIdx)
		val := int(binary.NativeEndian.Uint32(be[:]))
		if err := windows.SetsockoptInt(handle, windows.IPPROTO_IP, winIPUnicastIf, val); err != nil {
			socketErr = fmt.Errorf("set windows IP_UNICAST_IF %d: %w", ifIdx, err)
		}
	}); err != nil {
		return fmt.Errorf("access outbound socket: %w", err)
	}
	return socketErr
}

func verifyBypassMark(mark uint32) error {
	return fmt.Errorf("outbound socket mark %#x is unsupported on windows", mark)
}
