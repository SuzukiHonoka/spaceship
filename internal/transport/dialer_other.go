//go:build !linux

package transport

import (
	"fmt"
	"syscall"
)

func outboundSocketControl(_, _ string, raw syscall.RawConn) error {
	if mark := bypassMark.Load(); mark != 0 {
		return fmt.Errorf("outbound socket mark %#x is unsupported on this platform", mark)
	}
	if socketProtectorHook() == nil {
		return nil
	}

	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		socketErr = protectOutboundSocket(int(fd))
	}); err != nil {
		return fmt.Errorf("access outbound socket: %w", err)
	}
	return socketErr
}

func verifyBypassMark(mark uint32) error {
	return fmt.Errorf("outbound socket mark %#x is unsupported on this platform", mark)
}
