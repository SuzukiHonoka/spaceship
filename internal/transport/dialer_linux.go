//go:build linux

package transport

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func outboundSocketControl(_, _ string, raw syscall.RawConn) error {
	mark := bypassMark.Load()
	if mark == 0 {
		return nil
	}

	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		socketErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(mark))
	}); err != nil {
		return fmt.Errorf("access outbound socket for SO_MARK: %w", err)
	}
	if socketErr != nil {
		return fmt.Errorf("set outbound SO_MARK %#x: %w", mark, socketErr)
	}
	return nil
}
